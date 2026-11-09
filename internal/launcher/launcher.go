// Package launcher assembles one agent: the ADK agent with its gateway
// tools, the governance plugin, session storage, and the handlers of
// the agent's socket (CONTRACT.md §5, §10, Part II).
package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/sdrahn/custos/internal/playbook"
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
	tools := tool.FilterToolset(gw, func(_ agent.ReadonlyContext, t tool.Tool) bool { return cfg.ShowsTool(t.Name()) })
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
		Name:                  strings.ReplaceAll(cfg.Name, "-", "_"),
		Description:           cfg.Description,
		Model:                 llm,
		Instruction:           cfg.Instruction,
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

// Status is GET /v1/status (CONTRACT.md §5).
type Status struct {
	Contract       int               `json:"contract"`
	Implementation string            `json:"implementation"`
	Units          map[string]string `json:"units,omitempty"`
	Lifecycle      map[string]string `json:"lifecycle"`
	Name           string            `json:"name"`
	Version        string            `json:"version"`
	Model          string            `json:"model"`
	Paused         bool              `json:"paused"`
	Gateway        GatewayStatus     `json:"gateway"`
	InFlight       []governance.Call `json:"in_flight"`
	Running        int               `json:"running"`
	Started        time.Time         `json:"started"`
	Limits         config.Limits     `json:"limits"`
	Interaction    map[string]any    `json:"interaction"`
	Schedules      []config.Schedule `json:"schedules"`
	Events         []config.Event    `json:"events"`
}

type GatewayStatus struct {
	Socket    string `json:"socket,omitempty"`
	URL       string `json:"url,omitempty"`
	Reachable bool   `json:"reachable"`
}

func (l *Launcher) status(w http.ResponseWriter, r *http.Request) {
	running := 0
	for _, a := range l.Governor.Activities() {
		if a.Ended == nil {
			running++
		}
	}
	var units map[string]string
	if lc := l.cfg.Lifecycle(); lc["kind"] == "systemd" {
		units = map[string]string{"service": lc["service"], "socket": lc["socket"]}
	}
	reachable := false
	if l.cfg.Gateway.URL != "" {
		reachable = true // not checked without a request; the calls show it
	} else if _, err := os.Stat(l.cfg.Gateway.Socket); err == nil {
		reachable = true
	}
	writeJSON(w, http.StatusOK, Status{
		Contract: config.ContractVersion, Implementation: "custos/adk-go", Name: l.cfg.Name, Version: Version,
		Units: units, Lifecycle: l.cfg.Lifecycle(),
		Model: l.cfg.Model.Provider + ":" + l.cfg.Model.Name, Paused: l.Governor.Paused(),
		Gateway:  GatewayStatus{Socket: l.cfg.Gateway.Socket, URL: l.cfg.Gateway.URL, Reachable: reachable},
		InFlight: nonNil(l.Governor.InFlight()), Running: running, Started: l.started, Limits: l.cfg.Limits,
		Interaction: map[string]any{"agui": l.cfg.Interaction.AGUI, "client_tools": nonNil(l.cfg.Interaction.ClientTools.Allow)},
		Schedules:   nonNil(l.cfg.Schedules),
		Events:      nonNil(l.cfg.Events),
	})
}

func (l *Launcher) activities(w http.ResponseWriter, r *http.Request) {
	c, _ := peercred.From(r.Context())
	if _, isAgent := c.Agent(); isAgent {
		http.Error(w, "agents may only read the activities they started", http.StatusForbidden)
		return
	}
	acts := l.Governor.Activities()
	for i := range acts {
		acts[i].Response = "" // GET /v1/activities/{id} has it; the list stays small
	}
	writeJSON(w, http.StatusOK, map[string]any{"activities": nonNil(acts)})
}

func (l *Launcher) activity(w http.ResponseWriter, r *http.Request) {
	c, _ := peercred.From(r.Context())
	a, ok := l.Governor.Activity(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if name, isAgent := c.Agent(); isAgent && (a.Parent == nil || a.Parent.Agent != name) {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// StartRequest is POST /v1/activities.
type StartRequest struct {
	Prompt  string `json:"prompt"`
	Session string `json:"session"`
	// User and Origin may be set by root only (schedules, events).
	User   string            `json:"user"`
	Origin governance.Origin `json:"origin"`
	// Trace is the delegating activity's (agents only).
	Trace *Trace `json:"trace"`
	// Wait: answer when the activity has ended, with the activity.
	Wait bool `json:"wait"`
	// Playbook names a diagnostic playbook to run (CONTRACT.md §14c);
	// Prompt is then optional and added to it.
	Playbook string `json:"playbook"`
	// Unit is the systemd unit the playbook is about: the steps that
	// need one run with it (§14c).
	Unit string `json:"unit"`
}

type Trace struct {
	RootAgent      string `json:"root_agent"`
	RootActivity   string `json:"root_activity"`
	ParentAgent    string `json:"parent_agent"`
	ParentActivity string `json:"parent_activity"`
}

func (l *Launcher) startActivity(w http.ResponseWriter, r *http.Request) {
	c, _ := peercred.From(r.Context())
	var req StartRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || (strings.TrimSpace(req.Prompt) == "" && req.Playbook == "") {
		http.Error(w, "want {\"prompt\": …} or {\"playbook\": …}", http.StatusBadRequest)
		return
	}
	if req.Unit != "" && (req.Playbook == "" || !playbook.ValidUnit(req.Unit)) {
		http.Error(w, "unit: a systemd unit name, with a playbook", http.StatusBadRequest)
		return
	}
	st := governance.Start{}
	prompt := req.Prompt
	if req.Playbook != "" {
		var status int
		var err error
		if prompt, st.Steps, status, err = l.playbookPrompt(r.Context(), req.Playbook, req.Unit, req.Prompt); err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		st.Playbook = req.Playbook
	}
	user := c.Name
	switch name, isAgent := c.Agent(); {
	case isAgent:
		// Delegation: the caller is the delegating agent, by the kernel;
		// its trace is believed only where it names that agent.
		st.Origin = governance.Origin{Kind: "delegation", By: c.Name}
		if t := req.Trace; t != nil && t.ParentAgent == name {
			st.Parent = &governance.Ref{Agent: t.ParentAgent, Activity: t.ParentActivity}
			st.Root = &governance.Ref{Agent: t.RootAgent, Activity: t.RootActivity}
			if t.RootAgent == "" {
				st.Root = st.Parent
			}
		}
	case c.Root():
		st.Origin = req.Origin
		if st.Origin.Kind == "" {
			st.Origin = governance.Origin{Kind: "api", By: "root"}
		}
		switch st.Origin.Kind {
		case "schedule", "event", "api", "chat":
		default:
			http.Error(w, "origin.kind: want schedule, event, api or chat", http.StatusBadRequest)
			return
		}
		if req.User != "" {
			user = req.User
		} else if st.Origin.Kind == "schedule" || st.Origin.Kind == "event" {
			user = "system:" + st.Origin.Kind
		}
	default:
		st.Origin = governance.Origin{Kind: "api", By: c.Name}
	}

	sid := req.Session
	ctx := context.WithoutCancel(r.Context())
	if sid == "" {
		cs, err := l.Sessions.Create(ctx, &session.CreateRequest{AppName: l.cfg.Name, UserID: user})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sid = cs.Session.ID()
	} else if _, err := l.Sessions.Get(ctx, &session.GetRequest{AppName: l.cfg.Name, UserID: user, SessionID: sid}); err != nil {
		http.Error(w, "session: "+err.Error(), http.StatusNotFound)
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, l.cfg.Limits.MaxInvocation())
	st.Cancel = cancel
	started := make(chan string, 1)
	var startedID atomicString
	st.Started = func(id string) { startedID.Store(id); started <- id }
	l.Governor.Expect(sid, st)
	done := make(chan error, 1)
	l.runs.Add(1)
	go func() {
		defer l.runs.Done()
		defer cancel()
		var runErr error
		var id string
		for ev, err := range l.Runner.Run(runCtx, user, sid, genai.NewContentFromText(prompt, genai.RoleUser), agent.RunConfig{}) {
			if ev != nil && id == "" {
				id = ev.InvocationID
			}
			if err != nil {
				runErr = err
			}
		}
		if runErr != nil {
			l.Governor.Failed(firstNonEmpty(id, startedID.Load()), runErr)
		}
		done <- runErr
	}()
	var id string
	select {
	case id = <-started:
	case err := <-done:
		http.Error(w, fmt.Sprintf("the activity did not start: %v", err), http.StatusInternalServerError)
		return
	}
	if id == "" {
		http.Error(w, "the agent is paused", http.StatusConflict)
		return
	}
	if !req.Wait {
		writeJSON(w, http.StatusAccepted, map[string]string{"id": id, "session": sid})
		return
	}
	select {
	case <-done:
	case <-r.Context().Done():
		return // the activity goes on; the caller can read it later
	}
	a, _ := l.Governor.Activity(id)
	writeJSON(w, http.StatusOK, a)
}

func (l *Launcher) cancel(w http.ResponseWriter, r *http.Request) {
	c, _ := peercred.From(r.Context())
	a, ok := l.Governor.Activity(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !c.Root() && a.User != c.Name && a.Origin.By != c.Name {
		http.Error(w, "only root or the activity's user may cancel it", http.StatusForbidden)
		return
	}
	if !l.Governor.Cancel(a.ID, c.Name) {
		http.Error(w, "the activity is not running", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Launcher) pause(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, _ := peercred.From(r.Context())
		if !c.Root() {
			http.Error(w, "pausing needs administrative access", http.StatusForbidden)
			return
		}
		if on {
			l.Governor.Pause(c.Name)
		} else {
			l.Governor.Resume(c.Name)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

type atomicString struct{ v atomic.Value }

func (a *atomicString) Store(s string) { a.v.Store(s) }
func (a *atomicString) Load() string {
	s, _ := a.v.Load().(string)
	return s
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// peekJSON decodes the body into v and returns the request with the
// body intact for the next handler.
func peekJSON(r *http.Request, v any) (*http.Request, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return nil, errors.New("bad request body")
	}
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(b))
	return r2, nil
}

// playbookPrompt is the model's prompt for a playbook and the steps the
// host runs before the model's first turn: refused when the playbook is
// unknown or broken (400), or when the gateway does not offer this agent
// a tool of a step that is not optional (422).
func (l *Launcher) playbookPrompt(ctx context.Context, name, unit, extra string) (string, []governance.Step, int, error) {
	if !playbook.ValidName(name) {
		return "", nil, http.StatusBadRequest, fmt.Errorf("playbook: %q is not a playbook name", name)
	}
	all, err := playbook.Load(l.playbookRoot)
	if err != nil {
		return "", nil, http.StatusInternalServerError, fmt.Errorf("reading the playbooks: %w", err)
	}
	p := all[name]
	switch {
	case p == nil:
		return "", nil, http.StatusNotFound, fmt.Errorf("no playbook %q on this host", name)
	case p.Error != "":
		return "", nil, http.StatusBadRequest, fmt.Errorf("playbook %s (%s): %s", name, p.File, p.Error)
	}
	offered, err := l.offeredTools(ctx)
	if err != nil {
		return "", nil, http.StatusBadGateway, fmt.Errorf("listing the gateway's tools: %w", err)
	}
	if m := p.Missing(offered); len(m) > 0 {
		return "", nil, http.StatusUnprocessableEntity, fmt.Errorf("playbook %s needs tools the gateway does not offer agent %s: %s",
			name, l.cfg.Name, strings.Join(m, ", "))
	}
	var steps []governance.Step
	for _, s := range p.Run(offered, unit) {
		steps = append(steps, governance.Step{Tool: s.Tool, Why: s.Why, Args: s.Args})
	}
	return p.Prompt(offered, unit, extra), steps, 0, nil
}

// PlaybookOffer is a playbook as GET /v1/playbooks shows it to this
// agent: whether it can run it, and if not, why (CONTRACT.md §5, §14c).
type PlaybookOffer struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Runnable    bool   `json:"runnable"`
	// Missing are the tools of steps that are not optional which the
	// gateway does not offer this agent; Error why the file cannot be
	// used.
	Missing []string `json:"missing,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// playbooks answers which of the host's playbooks this agent can run
// now: those whose every step that is not optional has a tool the
// gateway offers it. The others are listed with the tools they lack.
func (l *Launcher) playbooks(w http.ResponseWriter, r *http.Request) {
	all, err := playbook.Load(l.playbookRoot)
	if err != nil {
		http.Error(w, "reading the playbooks: "+err.Error(), http.StatusInternalServerError)
		return
	}
	offered, err := l.offeredTools(r.Context())
	if err != nil {
		http.Error(w, "listing the gateway's tools: "+err.Error(), http.StatusBadGateway)
		return
	}
	out := map[string]PlaybookOffer{}
	for name, p := range all {
		o := PlaybookOffer{Title: p.Title, Description: p.Description, Error: p.Error}
		if p.Error == "" {
			o.Missing = p.Missing(offered)
			o.Runnable = len(o.Missing) == 0
		}
		out[name] = o
	}
	writeJSON(w, http.StatusOK, map[string]any{"playbooks": out})
}

// offeredTools are the gateway's tools the model is shown now: what the
// gateway's discovery offers this agent, narrowed by its configuration.
func (l *Launcher) offeredTools(ctx context.Context) (map[string]bool, error) {
	ts, err := l.tools.Tools(listContext{ctx})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, t := range ts {
		out[t.Name()] = true
	}
	return out, nil
}

// listContext is the context for listing tools outside a run: the
// toolsets use it only as a context.
type listContext struct{ context.Context }

func (listContext) UserContent() *genai.Content          { return nil }
func (listContext) InvocationID() string                 { return "" }
func (listContext) AgentName() string                    { return "" }
func (listContext) ReadonlyState() session.ReadonlyState { return nil }
func (listContext) UserID() string                       { return "" }
func (listContext) AppName() string                      { return "" }
func (listContext) SessionID() string                    { return "" }
func (listContext) Branch() string                       { return "" }
