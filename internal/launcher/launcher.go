// Package launcher assembles one agent: the ADK agent with its gateway
// tools, the governance plugin, session storage, and the handlers of
// the agent's socket (CONTRACT.md §5, §10, Part II).
package launcher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/artifact"
	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/server/adkrest"
	"google.golang.org/adk/session"
	"google.golang.org/adk/session/database"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/sdrahn/custos/internal/agui"
	"github.com/sdrahn/custos/internal/clienttools"
	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gateway"
	"github.com/sdrahn/custos/internal/governance"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/openai"
	"github.com/sdrahn/custos/internal/peercred"
)

// Version is set by the build.
var Version = "dev"

type Options struct {
	Config *config.Config
	Log    journal.Logger
	// StateDir holds sessions.db; empty: sessions in memory.
	StateDir string
	// Credentials is $CREDENTIALS_DIRECTORY.
	Credentials string
	// Model replaces the configured model (tests).
	Model model.LLM
	// PlaybookRoot is prefixed to the playbooks' directories (tests); ""
	// on a host.
	PlaybookRoot string
}

type Launcher struct {
	Handler  http.Handler
	Governor *governance.Governor
	Runner   *runner.Runner
	Sessions session.Service

	cfg     *config.Config
	log     journal.Logger
	started time.Time
	db      *gorm.DB
	// tools are the gateway's tools the model is shown; playbookRoot as
	// Options.
	tools        tool.Toolset
	playbookRoot string

	mu       sync.Mutex
	requests int
	lastSeen time.Time
	runs     sync.WaitGroup
}

func New(ctx context.Context, o Options) (*Launcher, error) {
	cfg := o.Config
	l := &Launcher{cfg: cfg, log: o.Log, started: time.Now(), lastSeen: time.Now(), playbookRoot: o.PlaybookRoot}
	l.Governor = governance.New(cfg, o.Log)

	llm := o.Model
	if llm == nil {
		var err error
		if llm, err = newModel(ctx, cfg, o.Credentials); err != nil {
			return nil, err
		}
	}

	var err error
	if l.Sessions, l.db, err = openSessions(o.StateDir); err != nil {
		return nil, err
	}
	// The ended activities outlive an idle exit, next to the sessions.
	if o.StateDir != "" {
		if err := l.Governor.Keep(filepath.Join(o.StateDir, governance.HistoryFile)); err != nil {
			return nil, err
		}
	}

	gw, err := mcptoolset.New(mcptoolset.Config{
		Client:    gateway.NewClient(cfg.Name, Version, l.Governor.OnGatewayLog, l.Governor.Proposal),
		Transport: gatewayTransport(cfg),
	})
	if err != nil {
		return nil, err
	}
	// A playbook run shows only the playbook's tools (§14c, #155).
	tools := tool.FilterToolset(gw, func(ctx agent.ReadonlyContext, t tool.Tool) bool {
		return cfg.ShowsTool(t.Name()) && (ctx == nil || l.Governor.Shows(ctx.InvocationID(), t.Name()))
	})
	l.tools = tools
	ct := clienttools.NewRegistry()

	gcc := &genai.GenerateContentConfig{}
	if cfg.Model.Temperature != nil {
		gcc.Temperature = cfg.Model.Temperature
	}
	// model.max_output_tokens: each provider takes it from the request
	// (the OpenAI-compatible one as max_tokens).
	if n := cfg.Model.MaxOutputTokens; n > 0 {
		gcc.MaxOutputTokens = int32(n)
	}
	report, err := l.Governor.NewReportTool()
	if err != nil {
		return nil, err
	}
	a, err := llmagent.New(llmagent.Config{
		Name:        strings.ReplaceAll(cfg.Name, "-", "_"),
		Description: cfg.Description,
		Model:       llm,
		// The host's notice that tool outputs are data (#155) follows
		// the agent's own instruction.
		Instruction:           strings.TrimRight(cfg.Instruction, "\n") + "\n\n" + governance.DataNotice,
		Tools:                 []tool.Tool{report},
		Toolsets:              []tool.Toolset{tools, ct.Toolset()},
		GenerateContentConfig: gcc,
	})
	if err != nil {
		return nil, err
	}
	plug, err := l.Governor.Plugin()
	if err != nil {
		return nil, err
	}
	pluginCfg := runner.PluginConfig{Plugins: []*plugin.Plugin{plug}}
	l.Runner, err = runner.New(runner.Config{AppName: cfg.Name, Agent: a, SessionService: l.Sessions, PluginConfig: pluginCfg})
	if err != nil {
		return nil, err
	}
	rest, err := adkrest.NewServer(adkrest.ServerConfig{
		SessionService:  l.Sessions,
		AgentLoader:     agent.NewSingleLoader(a),
		ArtifactService: artifact.InMemoryService(),
		PluginConfig:    pluginCfg,
		SSEWriteTimeout: 30 * time.Minute,
	})
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/", l.chatOrigin(rest))
	if cfg.Interaction.AGUI {
		mux.Handle("/agui", l.track(&agui.Handler{App: cfg.Name, Config: cfg, Runner: l.Runner, Sessions: l.Sessions, Governor: l.Governor, ClientTools: ct, Log: o.Log}))
	}
	mux.HandleFunc("GET /v1/status", l.status)
	mux.HandleFunc("GET /v1/playbooks", l.playbooks)
	mux.HandleFunc("GET /v1/activities", l.activities)
	mux.HandleFunc("GET /v1/activities/{id}", l.activity)
	mux.HandleFunc("POST /v1/activities", l.startActivity)
	mux.HandleFunc("POST /v1/activities/{id}/cancel", l.cancel)
	mux.HandleFunc("POST /v1/pause", l.pause(true))
	mux.HandleFunc("POST /v1/resume", l.pause(false))
	l.Handler = l.track(peercred.Guard(cfg.Name, mux))

	if err := l.Expire(ctx); err != nil {
		o.Log.Log(journal.AgentStart, "Expiring old sessions failed: "+err.Error(), journal.Fields{"OUTCOME": "error"})
	}
	return l, nil
}

func gatewayTransport(cfg *config.Config) mcp.Transport {
	if cfg.Gateway.URL != "" {
		return gateway.HTTPTransport(cfg.Gateway.URL, cfg.Gateway.TokenEnv, nil)
	}
	return &gateway.Transport{Socket: cfg.Gateway.Socket}
}

func newModel(ctx context.Context, cfg *config.Config, creds string) (model.LLM, error) {
	key := ""
	if c := cfg.Model.Credential; c != "" {
		// Without a stored credential systemd may set up no credentials
		// directory at all (ImportCredential=agent-%i.* matched nothing).
		var b []byte
		err := os.ErrNotExist
		if creds != "" {
			b, err = os.ReadFile(filepath.Join(creds, "agent-"+cfg.Name+"."+c))
		}
		if errors.Is(err, os.ErrNotExist) {
			// The name may be the key itself: it must not reach the journal.
			if config.LooksLikeSecret(c) {
				return nil, errors.New(config.SecretAsName)
			}
			return nil, fmt.Errorf("model.credential %[1]s is not stored for agent %[2]s: "+
				"agent-admin set-credential %[2]s %[1]s (the secret on stdin)", c, cfg.Name)
		}
		if err != nil {
			return nil, fmt.Errorf("model.credential: %w", err)
		}
		key = strings.TrimSpace(string(b))
	}
	// model.timeout_seconds bounds one request; without it, the
	// activity's max_invocation_seconds does.
	var httpOpts genai.HTTPOptions
	if t := cfg.Model.Timeout(); t > 0 {
		httpOpts.Timeout = &t
	}
	switch cfg.Model.Provider {
	case "gemini":
		return gemini.NewModel(ctx, cfg.Model.Name, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI, HTTPOptions: httpOpts})
	case "vertex":
		return gemini.NewModel(ctx, cfg.Model.Name, &genai.ClientConfig{Backend: genai.BackendVertexAI,
			Project: os.Getenv("GOOGLE_CLOUD_PROJECT"), Location: os.Getenv("GOOGLE_CLOUD_LOCATION"), HTTPOptions: httpOpts})
	case "openai":
		return (&openai.Model{Endpoint: cfg.Model.Endpoint, Name: cfg.Model.Name, APIKey: key, Temperature: cfg.Model.Temperature,
			Timeout: cfg.Model.Timeout(), ReasoningEffort: cfg.Model.ReasoningEffort}).LLM(), nil
	}
	return nil, fmt.Errorf("model.provider %q", cfg.Model.Provider)
}

func openSessions(dir string) (session.Service, *gorm.DB, error) {
	dsn := "file::memory:?cache=shared"
	if dir != "" {
		// systemd makes StateDirectory=; in a sandbox, nobody does.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("state directory: %w", err)
		}
		// _txlock=immediate: a transaction takes the write lock when it
		// begins, waiting busy_timeout for it. A deferred one that reads
		// first fails at once with SQLITE_BUSY when another run writes
		// (a schedule's and an AG-UI client's runs at the same time).
		dsn = filepath.Join(dir, "sessions.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	}
	dial := sqlite.Open(dsn)
	svc, err := database.NewSessionService(dial, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, nil, err
	}
	if err := database.AutoMigrate(svc); err != nil {
		return nil, nil, fmt.Errorf("sessions.db: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, nil, err
	}
	return svc, db, nil
}

// Expire deletes sessions untouched for retention_days.
func (l *Launcher) Expire(ctx context.Context) error {
	var rows []struct{ AppName, UserID, ID string }
	cut := time.Now().AddDate(0, 0, -l.cfg.Sessions.RetentionDays)
	if err := l.db.WithContext(ctx).Table("sessions").Select("app_name, user_id, id").Where("update_time < ?", cut).Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		if err := l.Sessions.Delete(ctx, &session.DeleteRequest{AppName: r.AppName, UserID: r.UserID, SessionID: r.ID}); err != nil {
			return err
		}
	}
	return nil
}

// Idle reports how long the agent has had no request and no activity.
func (l *Launcher) Idle() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.requests > 0 || len(l.Governor.InFlight()) > 0 || l.running() {
		return 0
	}
	return time.Since(l.lastSeen)
}

func (l *Launcher) running() bool {
	for _, a := range l.Governor.Activities() {
		if a.Ended == nil {
			return true
		}
	}
	return false
}

// Wait waits for activities started through /v1/activities.
func (l *Launcher) Wait() { l.runs.Wait() }

func (l *Launcher) track(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.requests++
		l.mu.Unlock()
		defer func() {
			l.mu.Lock()
			l.requests--
			l.lastSeen = time.Now()
			l.mu.Unlock()
		}()
		h.ServeHTTP(w, r)
	})
}

// chatOrigin registers ADK REST runs as chat activities with a deadline
// and a cancel function.
func (l *Launcher) chatOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != "/run" && r.URL.Path != "/run_sse") {
			next.ServeHTTP(w, r)
			return
		}
		c, _ := peercred.From(r.Context())
		var body struct {
			SessionID string `json:"sessionId"`
		}
		r2, err := peekJSON(r, &body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), l.cfg.Limits.MaxInvocation())
		defer cancel()
		l.Governor.Expect(body.SessionID, governance.Start{Origin: governance.Origin{Kind: "chat", By: c.Name}, Cancel: cancel})
		next.ServeHTTP(w, r2.WithContext(ctx))
	})
}
