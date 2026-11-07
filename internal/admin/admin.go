// Package admin is agent-admin: the administrative actions on agents
// that Cockpit runs with administrative access (CONTRACT.md §11). Every
// path hangs off Root, so tests can run it in a temporary directory.
package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/delegate"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/openshell"
)

type Admin struct {
	Root string // "" on a host; a temporary directory in tests
	// Run runs a command (systemctl, systemd-sysusers, systemd-creds).
	Run func(stdin io.Reader, name string, args ...string) error
	// HTTP reaches an agent's identity provider (check-token); nil: a
	// client with a timeout.
	HTTP *http.Client
	// GatewayConfig, when set, is the gateway configuration idp-client and
	// check-token read instead of the installed one (--gateway-config).
	GatewayConfig string
	// LookupHost resolves a host name (net.LookupHost when nil).
	LookupHost func(host string) ([]string, error)
	// Output runs a command and returns its output (systemd-creds
	// decrypt, the seal's key); nil: run it.
	Output func(stdin io.Reader, name string, args ...string) ([]byte, error)
	Out    io.Writer
	// In is read for a configuration given as "-" (--from -, check -).
	In io.Reader
	// Now is the clock (tests).
	Now func() time.Time
	// Control posts to an agent's control API (pause, resume).
	Control func(sock, path string) error
	// Start starts an activity on an agent's socket (schedules, events).
	Start func(sock string, body map[string]any) error
	// Activities reads an agent's activities (GET /v1/activities), for
	// an event's damping; nil: not checked.
	Activities func(sock string) ([]map[string]any, error)
	// Getenv reads the environment (systemd's MONITOR_* for an event);
	// nil: os.Getenv.
	Getenv func(string) string
	// RunEnv runs a command with extra environment variables (secrets
	// handed to openshell, which reads them from its environment).
	RunEnv func(env []string, name string, args ...string) error
	// Image is the OpenShell sandbox image (CUSTOS_OPENSHELL_IMAGE).
	Image string
	// OpenShellUser runs the local OpenShell gateway, whose rootless
	// Podman holds the sandboxes' containers (CUSTOS_OPENSHELL_USER; empty:
	// root's or Docker's), for the relay's log source.
	OpenShellUser string
	// Secret makes the relay secrets.
	Secret func() string
	// OPAData reads a document of the gateway's OPA (GET /v1/data/<path>);
	// nil: changes are not waited for. PolicyWait bounds the wait.
	OPAData    func(path string) (any, error)
	PolicyWait time.Duration
	// Probe runs a command for its exit status only, without its output
	// (openshell sandbox get while a sandbox is deleted); nil: Run.
	Probe func(name string, args ...string) error
	// Sleep waits between probes (tests: no wait); nil: time.Sleep.
	Sleep func(time.Duration)
	// SandboxGone bounds the wait for a deleted sandbox to be gone.
	SandboxGone time.Duration
	// Journal gives the promotion records' logger for an agent (§10);
	// nil: journald as custos-promotion.
	Journal func(name string) journal.Logger
	// JournalJSON reads journal records (journalctl -o json with these
	// matches, over the readiness window); nil: journalctl.
	JournalJSON func(args ...string) ([]byte, error)
	// HealthCheck runs a rollback health check (sh -c, as root); nil:
	// runHealthCheck.
	HealthCheck func(cmd string) error
	// By is the person, for the records of refusals (category.refused).
	By string
	// StopInstances stops a server's running instances through the
	// gateway's control API and says how many; nil: stopInstances.
	StopInstances func(server string) (int, error)
	// Notify posts a notice to the approvers through the gateway (R3) and
	// gives its id; nil: no notices.
	Notify func(Notice) (string, error)
}

// New is agent-admin on a host. Its standard output is its own messages
// (Out), which a control plane shows; what the programs it runs print
// (systemctl, openshell, …) goes to standard error (CONTRACT.md §17).
func New() *Admin {
	return &Admin{
		Run: func(stdin io.Reader, name string, args ...string) error {
			c := exec.Command(name, args...)
			c.Stdin, c.Stdout, c.Stderr = stdin, os.Stderr, os.Stderr
			return c.Run()
		},
		Out: os.Stdout, In: os.Stdin, Now: time.Now,
		Control: func(sock, path string) error { return delegate.Post(context.Background(), sock, path) },
		Start:   func(sock string, body map[string]any) error { return delegate.Start(context.Background(), sock, body) },
		Activities: func(sock string) ([]map[string]any, error) {
			return delegate.Activities(context.Background(), sock)
		},
		RunEnv: func(env []string, name string, args ...string) error {
			c := exec.Command(name, args...)
			c.Env = append(os.Environ(), env...)
			c.Stdout, c.Stderr = os.Stderr, os.Stderr
			return c.Run()
		},
		Image:         envOr("CUSTOS_OPENSHELL_IMAGE", "localhost/custos-agent:latest"),
		OpenShellUser: os.Getenv("CUSTOS_OPENSHELL_USER"),
		Secret: func() string {
			b := make([]byte, 32)
			rand.Read(b)
			return hex.EncodeToString(b)
		},
		OPAData:    opaData,
		Notify:     postNotice,
		PolicyWait: 20 * time.Second,
		Probe: func(name string, args ...string) error {
			return exec.Command(name, args...).Run()
		},
		SandboxGone: 2 * time.Minute,
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (a *Admin) p(path string) string { return filepath.Join(a.Root, path) }

func (a *Admin) agentDir(name string) string {
	return a.p(filepath.Join(config.ConfigRoot, name))
}

// Paths of the contract (§3, §6).
const (
	SysusersDir  = "/etc/sysusers.d"
	CredstoreDir = "/etc/credstore.encrypted"
	UnitDir      = "/etc/systemd/system"
	// One directory for every agent's gateway data and the emergency
	// stop: OPA (--watch) watches only directories it loaded a file from
	// when it started, so nothing goes into directories made later. The
	// package ships _control.yaml there.
	GatewayAgents = "/etc/mcp-gateway/policy/agents"
	GatewayStop   = "/etc/mcp-gateway/policy/agents/_control.yaml"
	SocketDir     = "/run/agents"
)

func checkName(name string) error {
	if !config.ValidName(name) {
		return fmt.Errorf("%q is not a valid agent name", name)
	}
	return nil
}

// Check validates a configuration file (or an agent's) without changing anything.
func (a *Admin) Check(target string) error {
	var cfg *config.Config
	var err error
	if config.ValidName(target) {
		cfg, err = config.Load(a.agentDir(target))
	} else {
		var b []byte
		if b, err = a.readFrom(target); err != nil {
			return err
		}
		cfg, err = config.Parse(b)
	}
	if err != nil {
		return err
	}
	if err := a.checkModelCredential(cfg); err != nil {
		return err
	}
	a.noteGatewayHost(cfg)
	return nil
}

// readFrom reads a configuration from a file, or from In for "-" (as a
// control plane gives it, without a file of its own).
func (a *Admin) readFrom(from string) ([]byte, error) {
	if from == "-" {
		if a.In == nil {
			return nil, errors.New("no configuration on stdin")
		}
		return io.ReadAll(io.LimitReader(a.In, 1<<20))
	}
	return os.ReadFile(from)
}

// Create installs a new agent from a configuration file.
func (a *Admin) Create(name, from string) error {
	if err := checkName(name); err != nil {
		return err
	}
	b, err := a.readFrom(from)
	if err != nil {
		return err
	}
	return a.create(name, b)
}

// create installs a new agent from its configuration.
func (a *Admin) create(name string, b []byte) (err error) {
	if err := checkName(name); err != nil {
		return err
	}
	cfg, err := config.Parse(b)
	if err != nil {
		return err
	}
	if cfg.Name != name {
		return fmt.Errorf("the file is for agent %q, not %q", cfg.Name, name)
	}
	dir := a.agentDir(name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("agent %s exists", name)
	}
	if err := a.checkModelCredential(cfg); err != nil {
		return err
	}
	defer func() {
		if err == nil {
			a.noteModelCredential(cfg)
			a.noteGatewayHost(cfg)
		}
	}()
	user := "agent-" + name
	sysusers := fmt.Sprintf("# agent-admin: the user of agent %[1]s (CONTRACT.md §2)\nu %[2]s - \"Custos agent %[1]s\" /var/lib/agents/%[1]s /usr/sbin/nologin\nm %[2]s mcp-users\nm %[2]s agent-clients\n", name, user)
	sf := a.p(filepath.Join(SysusersDir, user+".conf"))
	if err := writeFile(sf, []byte(sysusers), 0o644); err != nil {
		return err
	}
	if err := a.Run(nil, "systemd-sysusers", sf); err != nil {
		return fmt.Errorf("creating user %s: %w", user, err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, "agent.yaml"), b, 0o640); err != nil {
		return err
	}
	if err := a.Run(nil, "chgrp", "-R", user, dir); err != nil {
		return err
	}
	// Supervised by default, and nothing allowed until listed (§6).
	if err := a.writeGateway(name, &GatewayData{Mode: "supervised", Permissions: []map[string]any{}}); err != nil {
		return err
	}
	if err := a.syncEvents(cfg); err != nil {
		return err
	}
	if err := a.syncSchedules(cfg); err != nil {
		return err
	}
	if err := a.Run(nil, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if cfg.Sandbox.Kind == "openshell" {
		return a.createOpenShell(cfg)
	}
	return a.Run(nil, "systemctl", "enable", "--now", "agent@"+name+".socket")
}

var userRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// LookupUser resolves the OpenShell user's uid and home (a variable for
// tests).
var LookupUser = func(name string) (uid, home string, err error) {
	u, err := user.Lookup(name)
	if err != nil {
		return "", "", err
	}
	return u.Uid, u.HomeDir, nil
}

// writeRelayUserDropIn lets the relay's log source use the OpenShell
// user's rootless Podman, which keeps its locks in the user's storage and
// runtime directory; the relay's unit otherwise sees homes and /run/user
// read-only (ProtectHome=read-only). Only those two directories.
func (a *Admin) writeRelayUserDropIn(name string) error {
	uid, home, err := LookupUser(a.OpenShellUser)
	if err != nil {
		return fmt.Errorf("CUSTOS_OPENSHELL_USER: %w", err)
	}
	conf := fmt.Sprintf("# agent-admin: the relay's log source reads %s's rootless Podman (CONTRACT.md §16a).\n[Service]\nReadWritePaths=-%s/.local/share/containers -/run/user/%s\n",
		a.OpenShellUser, home, uid)
	return writeFile(a.p(filepath.Join(UnitDir, "agent-openshell@"+name+".service.d", "openshell-user.conf")), []byte(conf), 0o644)
}

// createOpenShell prepares an OpenShell agent (CONTRACT.md §16a): the
// policy, the gateway provider profile, the relay's secret and port. The
// sandbox itself is created once the gateway token is set
// (set-credential NAME gateway-token), since its provider needs it.
func (a *Admin) createOpenShell(cfg *config.Config) error {
	dir := a.agentDir(cfg.Name)
	pol, err := openshell.Policy(cfg)
	if err != nil {
		return err
	}
	prof, err := openshell.Profile(cfg)
	if err != nil {
		return err
	}
	env := fmt.Sprintf("# agent-admin: agent %s in OpenShell\nOPENSHELL_GATEWAY=%s\nFORWARD_PORT=%d\nAGENT_IMAGE=%s\n",
		cfg.Name, cfg.Sandbox.OpenShellGateway, openshell.Port(cfg.Name), a.Image)
	if a.OpenShellUser != "" {
		if !userRE.MatchString(a.OpenShellUser) {
			return fmt.Errorf("CUSTOS_OPENSHELL_USER: %q is not a user name", a.OpenShellUser)
		}
		env += "OPENSHELL_USER=" + a.OpenShellUser + "\n"
		if err := a.writeRelayUserDropIn(cfg.Name); err != nil {
			return err
		}
	}
	for f, c := range map[string]struct {
		b    []byte
		mode os.FileMode
	}{
		"openshell-policy.yaml":  {pol, 0o644},
		"openshell-profile.yaml": {prof, 0o644},
		"openshell.env":          {[]byte(env), 0o644},
		"relay.secret":           {[]byte(a.Secret() + "\n"), 0o600},
	} {
		if err := writeFile(filepath.Join(dir, f), c.b, c.mode); err != nil {
			return err
		}
	}
	if err := a.Run(nil, "openshell", "-g", cfg.Sandbox.OpenShellGateway, "profile", "import", "-f", filepath.Join(dir, "openshell-profile.yaml"), "--global"); err != nil {
		return fmt.Errorf("importing the provider profile: %w", err)
	}
	if cfg.Gateway.OAuth != nil {
		fmt.Fprintf(a.Out, "agent %[1]s: set its OAuth client's secret next: agent-admin set-credential %[1]s gateway-client-secret < secret\n"+
			"  (the client it needs: agent-admin idp-client %[1]s; its token checked first: agent-admin check-token %[1]s < secret)\n", cfg.Name)
	} else {
		fmt.Fprintf(a.Out, "agent %s: set its gateway token next: agent-admin set-credential %s gateway-token < token\n", cfg.Name, cfg.Name)
	}
	return nil
}

// startOpenShell creates the provider with a static token, the sandbox,
// and starts the relay.
func (a *Admin) startOpenShell(cfg *config.Config, token string) error {
	g := cfg.Sandbox.OpenShellGateway
	a.Run(nil, "openshell", "-g", g, "provider", "delete", openshell.ProviderName(cfg.Name))
	if err := a.RunEnv([]string{openshell.TokenEnv + "=" + token}, "openshell", "-g", g, "provider", "create",
		"--name", openshell.ProviderName(cfg.Name), "--type", openshell.ProfileID(cfg.Name), "--credential", openshell.TokenEnv); err != nil {
		return fmt.Errorf("creating the provider: %w", err)
	}
	return a.startSandbox(cfg)
}

// clientSecretEnv carries the OAuth client secret to openshell, which
// reads it from its environment (--secret-material-env), never from its
// command line.
const clientSecretEnv = "CUSTOS_GATEWAY_CLIENT_SECRET"

// startOpenShellOAuth creates the provider whose token OpenShell's
// gateway mints and renews with the agent's OAuth client (gateway.oauth,
// the profile's refresh), mints the first token, then the sandbox.
func (a *Admin) startOpenShellOAuth(cfg *config.Config, secret string) error {
	g := cfg.Sandbox.OpenShellGateway
	p := openshell.ProviderName(cfg.Name)
	a.Run(nil, "openshell", "-g", g, "provider", "delete", p)
	if err := a.Run(nil, "openshell", "-g", g, "provider", "create", "--name", p, "--type", openshell.ProfileID(cfg.Name), "--runtime-credentials"); err != nil {
		return fmt.Errorf("creating the provider: %w", err)
	}
	if err := a.RunEnv([]string{clientSecretEnv + "=" + secret}, "openshell", "-g", g, "provider", "refresh", "configure", p,
		"--credential-key", openshell.TokenEnv, "--strategy", "oauth2-client-credentials",
		"--material", "client_id="+cfg.Gateway.OAuth.ClientID, "--secret-material-env", "client_secret="+clientSecretEnv); err != nil {
		return fmt.Errorf("configuring the token's renewal: %w", err)
	}
	// The first token now: a wrong secret or client fails here, not in
	// the agent's first call.
	if err := a.Run(nil, "openshell", "-g", g, "provider", "refresh", "rotate", p, "--credential-key", openshell.TokenEnv); err != nil {
		return fmt.Errorf("minting the first token (the issuer refused the client?): %w", err)
	}
	return a.startSandbox(cfg)
}

// startSandbox creates the sandbox with the provider attached and starts
// the relay.
func (a *Admin) startSandbox(cfg *config.Config) error {
	if err := a.createSandbox(cfg, a.Image); err != nil {
		return err
	}
	return a.Run(nil, "systemctl", "enable", "--now", "agent-openshell@"+cfg.Name+".socket")
}

// createSandbox (re)creates the sandbox from the agent's files: the
// policy, the relay secret and the configuration as the sandbox gets it.
// OpenShell starts it.
func (a *Admin) createSandbox(cfg *config.Config, image string) error {
	g := cfg.Sandbox.OpenShellGateway
	dir := a.agentDir(cfg.Name)
	secret, err := os.ReadFile(filepath.Join(dir, "relay.secret"))
	if err != nil {
		return err
	}
	a.Run(nil, "openshell", "-g", g, "sandbox", "delete", openshell.SandboxName(cfg.Name))
	if err := a.waitSandboxGone(cfg); err != nil {
		return err
	}
	args, err := openshell.CreateArgs(cfg, image, filepath.Join(dir, "openshell-policy.yaml"), strings.TrimSpace(string(secret)), openshell.Port(cfg.Name))
	if err != nil {
		return err
	}
	if err := a.Run(nil, "openshell", append([]string{"-g", g}, args...)...); err != nil {
		return fmt.Errorf("creating the sandbox: %w", err)
	}
	return nil
}

// Edit replaces an agent's configuration and applies it: a systemd agent
// restarts if it runs; an OpenShell agent gets the change in its sandbox
// (editOpenShell). The backend and the OpenShell gateway stay as created.
func (a *Admin) Edit(name, from string) (err error) {
	if err := checkName(name); err != nil {
		return err
	}
	b, err := a.readFrom(from)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(b)
	if err != nil {
		return err
	}
	if cfg.Name != name {
		return fmt.Errorf("the file is for agent %q, not %q", cfg.Name, name)
	}
	dir := a.agentDir(name)
	old, err := config.Load(dir)
	if err != nil {
		old = nil // the sandbox's inputs count as changed
	}
	if err := a.checkModelCredential(cfg); err != nil {
		return err
	}
	defer func() {
		if err == nil {
			a.noteModelCredential(cfg)
			a.noteGatewayHost(cfg)
		}
	}()
	if old != nil && old.Sandbox.Kind != cfg.Sandbox.Kind {
		return fmt.Errorf("sandbox.kind: agent %s runs with %s, not %s; delete and create it to change the backend", name, old.Sandbox.Kind, cfg.Sandbox.Kind)
	}
	if old != nil && cfg.Sandbox.Kind == "openshell" && old.Sandbox.OpenShellGateway != cfg.Sandbox.OpenShellGateway {
		return fmt.Errorf("sandbox.openshell_gateway: agent %s's sandbox is on OpenShell gateway %q; delete and create it to move it", name, old.Sandbox.OpenShellGateway)
	}
	if err := writeFile(filepath.Join(dir, "agent.yaml"), b, 0o640); err != nil {
		return err
	}
	a.Run(nil, "chgrp", "agent-"+name, filepath.Join(dir, "agent.yaml"))
	if err := a.syncEvents(cfg); err != nil {
		return err
	}
	if err := a.syncSchedules(cfg); err != nil {
		return err
	}
	a.Run(nil, "systemctl", "daemon-reload")
	if cfg.Sandbox.Kind == "openshell" {
		return a.editOpenShell(old, name)
	}
	return a.Run(nil, "systemctl", "try-restart", "agent@"+name+".service")
}

// editOpenShell applies an edited configuration to an OpenShell agent
// (CONTRACT.md §16a). The sandbox holds the configuration it was created
// with (CUSTOS_CONFIG), the policy and the provider's endpoint, so a
// change to any of them rewrites the policy and the profile and recreates
// the sandbox; the relay and the sandbox then run as they did before. A
// change of the gateway credential's kind (gateway.oauth, its client_id)
// needs the credential again, which agent-admin does not keep: the agent
// stays down until set-credential. Without any change the relay restarts
// if it runs. openshell.env takes nothing from agent.yaml and stays.
func (a *Admin) editOpenShell(old *config.Config, name string) error {
	dir := a.agentDir(name)
	cfg, err := config.Load(dir) // as the sandbox gets it: instruction_file read
	if err != nil {
		return err
	}
	g := cfg.Sandbox.OpenShellGateway
	pol, err := openshell.Policy(cfg)
	if err != nil {
		return err
	}
	prof, err := openshell.Profile(cfg)
	if err != nil {
		return err
	}
	polFile, profFile := filepath.Join(dir, "openshell-policy.yaml"), filepath.Join(dir, "openshell-profile.yaml")
	oldPol, _ := os.ReadFile(polFile)
	oldProf, _ := os.ReadFile(profFile)
	polChanged, profChanged := !bytes.Equal(pol, oldPol), !bytes.Equal(prof, oldProf)
	cfgChanged := true
	if old != nil {
		oc, e1 := openshell.SandboxConfig(old)
		ns, e2 := openshell.SandboxConfig(cfg)
		cfgChanged = e1 != nil || e2 != nil || !bytes.Equal(oc, ns)
	}
	if polChanged {
		if err := writeFile(polFile, pol, 0o644); err != nil {
			return err
		}
	}
	if profChanged {
		if err := writeFile(profFile, prof, 0o644); err != nil {
			return err
		}
		if err := a.Run(nil, "openshell", "-g", g, "profile", "import", "-f", profFile, "--global"); err != nil {
			return fmt.Errorf("importing the provider profile: %w", err)
		}
	}
	socket, service := "agent-openshell@"+name+".socket", "agent-openshell@"+name+".service"
	// set-credential enables the socket once it has created the sandbox:
	// before that, it creates the sandbox from the files written here.
	if a.Run(nil, "systemctl", "-q", "is-enabled", socket) != nil {
		fmt.Fprintf(a.Out, "agent %s: no sandbox yet; set-credential creates it with this configuration\n", name)
		return nil
	}
	if old != nil && credentialChanged(old, cfg) {
		a.Run(nil, "systemctl", "disable", "--now", socket, service)
		a.Run(nil, "openshell", "-g", g, "sandbox", "delete", openshell.SandboxName(name))
		a.Run(nil, "openshell", "-g", g, "provider", "delete", openshell.ProviderName(name))
		cred := "gateway-token < token"
		if cfg.Gateway.OAuth != nil {
			cred = "gateway-client-secret < secret"
		}
		fmt.Fprintf(a.Out, "agent %s: its gateway credential changed (gateway.oauth); it is stopped until: agent-admin set-credential %s %s\n", name, name, cred)
		return nil
	}
	if !polChanged && !profChanged && !cfgChanged {
		return a.Run(nil, "systemctl", "try-restart", service)
	}
	// The relay must not start the old sandbox, or forward to none, while
	// the new one is made; afterwards it runs again as it ran before
	// (not after stop-all, say).
	socketUp := a.Run(nil, "systemctl", "-q", "is-active", socket) == nil
	serviceUp := a.Run(nil, "systemctl", "-q", "is-active", service) == nil
	a.Run(nil, "systemctl", "stop", socket, service)
	if err := a.createSandbox(cfg, a.sandboxImage(name)); err != nil {
		return err
	}
	var start []string
	if socketUp {
		start = append(start, socket)
	}
	if serviceUp {
		start = append(start, service)
	} else {
		// OpenShell started it; the relay starts it when it starts.
		a.Run(nil, "openshell", "-g", g, "sandbox", "stop", openshell.SandboxName(name))
	}
	if len(start) == 0 {
		return nil
	}
	return a.Run(nil, "systemctl", append([]string{"start"}, start...)...)
}

// waitSandboxGone waits until OpenShell no longer knows the sandbox:
// deleting a running sandbox is only accepted at first ("cleanup is
// pending"), and a create of the same name until then fails ("already
// exists").
func (a *Admin) waitSandboxGone(cfg *config.Config) error {
	probe, sleep := a.Probe, a.Sleep
	if probe == nil {
		probe = func(name string, args ...string) error { return a.Run(nil, name, args...) }
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	const step = time.Second
	for waited := time.Duration(0); ; waited += step {
		if probe("openshell", "-g", cfg.Sandbox.OpenShellGateway, "sandbox", "get", openshell.SandboxName(cfg.Name)) != nil {
			return nil
		}
		if waited >= a.SandboxGone {
			return fmt.Errorf("sandbox %s is still there %s after its deletion", openshell.SandboxName(cfg.Name), a.SandboxGone)
		}
		sleep(step)
	}
}

// credentialChanged: the provider's credential is made differently (a
// static token or an OAuth client, and which client).
func credentialChanged(old, cfg *config.Config) bool {
	o, n := old.Gateway.OAuth, cfg.Gateway.OAuth
	return (o == nil) != (n == nil) || (o != nil && o.ClientID != n.ClientID)
}

// sandboxImage is the image the agent's sandbox was created from
// (openshell.env's AGENT_IMAGE), so that an edit keeps it.
func (a *Admin) sandboxImage(name string) string {
	b, _ := os.ReadFile(filepath.Join(a.agentDir(name), "openshell.env"))
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "AGENT_IMAGE="); ok && v != "" {
			return v
		}
	}
	return a.Image
}

// Delete removes an agent. Its user stays: uids are not reused while
// files may still be theirs.
func (a *Admin) Delete(name string, keepState bool) error {
	if err := checkName(name); err != nil {
		return err
	}
	cfg, _ := config.Load(a.agentDir(name))
	units := []string{"agent@" + name + ".socket", "agent@" + name + ".service"}
	if cfg != nil && cfg.Sandbox.Kind == "openshell" {
		units = []string{"agent-openshell@" + name + ".socket", "agent-openshell@" + name + ".service"}
		g := cfg.Sandbox.OpenShellGateway
		defer func() {
			a.Run(nil, "openshell", "-g", g, "sandbox", "delete", openshell.SandboxName(name))
			a.Run(nil, "openshell", "-g", g, "provider", "delete", openshell.ProviderName(name))
			a.Run(nil, "openshell", "-g", g, "profile", "delete", openshell.ProfileID(name))
		}()
	}
	if cfg != nil {
		for _, s := range cfg.Schedules {
			units = append(units, scheduleUnit(name, s.Name)+".timer")
		}
	}
	a.Run(nil, "systemctl", append([]string{"disable", "--now"}, units...)...)
	rm := []string{a.agentDir(name), a.gatewayFile(name), a.p(filepath.Join(UnitDir, "agent@"+name+".service.d")),
		a.p(filepath.Join(UnitDir, "agent-openshell@"+name+".service.d")),
		// The socket's directory: systemd removes the socket at stop
		// (RemoveOnStop=) but leaves the directory, by which the
		// Cockpit page finds agents (§3).
		a.p(filepath.Join(SocketDir, name))}
	matches, _ := filepath.Glob(a.p(filepath.Join(CredstoreDir, "agent-"+name+".*")))
	rm = append(rm, matches...)
	timers, _ := filepath.Glob(a.p(filepath.Join(UnitDir, "agent-schedule@"+name+"_*")))
	rm = append(rm, timers...)
	a.removeEvents(name)
	if !keepState {
		rm = append(rm, a.p("/var/lib/agents/"+name))
	}
	for _, p := range rm {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return a.Run(nil, "systemctl", "daemon-reload")
}

// SetCredential encrypts a secret from stdin for the agent (§5).
func (a *Admin) SetCredential(name, cred string, secret io.Reader) error {
	if err := checkName(name); err != nil {
		return err
	}
	if !config.ValidCredential(cred) {
		return fmt.Errorf("%q is not a valid credential name", cred)
	}
	if cred == "gateway-token" || cred == "gateway-client-secret" {
		cfg, err := config.Load(a.agentDir(name))
		if err != nil {
			return err
		}
		if cfg.Sandbox.Kind != "openshell" {
			return fmt.Errorf("agent %s reaches the gateway by its socket; %s is for OpenShell agents", name, cred)
		}
		switch {
		case cred == "gateway-token" && cfg.Gateway.OAuth != nil:
			return fmt.Errorf("agent %s has an OAuth client (gateway.oauth): set gateway-client-secret, OpenShell mints its tokens", name)
		case cred == "gateway-client-secret" && cfg.Gateway.OAuth == nil:
			return fmt.Errorf("agent %s has no OAuth client (gateway.oauth): set gateway-token", name)
		}
		b, err := io.ReadAll(io.LimitReader(secret, 64<<10))
		if err != nil {
			return err
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return fmt.Errorf("%s: empty", cred)
		}
		if cred == "gateway-client-secret" {
			return a.startOpenShellOAuth(cfg, v)
		}
		return a.startOpenShell(cfg, v)
	}
	id := "agent-" + name + "." + cred
	if err := os.MkdirAll(a.p(CredstoreDir), 0o700); err != nil {
		return err
	}
	out := a.p(filepath.Join(CredstoreDir, id))
	if err := a.Run(secret, "systemd-creds", "encrypt", "--name="+id, "-", out); err != nil {
		return err
	}
	// systemd-creds creates it with the umask (0644): encrypted, but root's
	// alone all the same.
	if err := os.Chmod(out, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// credentialStored reports whether the agent's credential cred is in
// the credential store; known is false when that cannot be told (not
// root: the store is root's alone).
func (a *Admin) credentialStored(name, cred string) (stored, known bool) {
	_, err := os.Stat(a.p(filepath.Join(CredstoreDir, "agent-"+name+"."+cred)))
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, os.ErrNotExist):
		return false, true
	}
	return false, false
}

// checkModelCredential refuses a model.credential that looks like the
// secret itself (config.LooksLikeSecret) unless a credential of that name
// is stored: a key written into agent.yaml is no credential, and the
// agent would not start. When the store cannot be read, it lets it pass.
func (a *Admin) checkModelCredential(cfg *config.Config) error {
	c := cfg.Model.Credential
	if c == "" || !config.LooksLikeSecret(c) {
		return nil
	}
	if stored, known := a.credentialStored(cfg.Name, c); stored || !known {
		return nil
	}
	return errors.New(config.SecretAsName)
}

// noteModelCredential says what an agent still needs when its model's
// credential is not stored yet: it does not start without it.
func (a *Admin) noteModelCredential(cfg *config.Config) {
	c := cfg.Model.Credential
	if c == "" {
		return
	}
	if stored, known := a.credentialStored(cfg.Name, c); known && !stored {
		fmt.Fprintf(a.Out, "agent %[1]s: its model credential %[2]s is not stored yet, and the agent does not start without it: "+
			"agent-admin set-credential %[1]s %[2]s (the secret on stdin)\n", cfg.Name, c)
	}
}

// GatewayData is data.mcp.agents.<name> (§6).
type GatewayData struct {
	Mode        string           `yaml:"mode" json:"mode"`
	Paused      bool             `yaml:"paused,omitempty" json:"paused"`
	PausedBy    string           `yaml:"paused_by,omitempty" json:"paused_by,omitempty"`
	Permissions []map[string]any `yaml:"permissions" json:"permissions"`
	// Categories are the task categories promoted or demoted (§6a).
	Categories map[string]*CategoryState `yaml:"categories,omitempty" json:"categories,omitempty"`
	// Owners may approve the agent's calls (§7, O1): rules of the
	// gateway's approver forms (owners.rego).
	Owners []string `yaml:"owners,omitempty" json:"owners,omitempty"`
}

// gatewayFile is the agent's file in GatewayAgents: <name>.yaml, whose
// top-level key is the name (data.mcp.agents.<name>).
func (a *Admin) gatewayFile(name string) string {
	return a.p(filepath.Join(GatewayAgents, name+".yaml"))
}

func (a *Admin) readGateway(name string) (*GatewayData, error) {
	b, err := os.ReadFile(a.gatewayFile(name))
	if err != nil {
		return nil, err
	}
	var doc map[string]*GatewayData
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", a.gatewayFile(name), err)
	}
	if doc[name] == nil {
		return nil, fmt.Errorf("%s: no data for agent %s", a.gatewayFile(name), name)
	}
	return doc[name], nil
}

func (a *Admin) writeGateway(name string, d *GatewayData) error {
	if d.Permissions == nil {
		d.Permissions = []map[string]any{}
	}
	b, err := yaml.Marshal(map[string]*GatewayData{name: d})
	if err != nil {
		return err
	}
	head := "# Written by agent-admin: the autonomy mode and permissions of agent " + name + "\n# (Custos CONTRACT.md §6). mcp-gateway's OPA reloads it by itself.\n"
	from := []map[string]any{}
	if prev, err := a.readGateway(name); err == nil && prev.Permissions != nil {
		from = prev.Permissions
	}
	if err := writeFile(a.gatewayFile(name), append([]byte(head), b...), 0o644); err != nil {
		return err
	}
	a.recordPermissions(name, from, d.Permissions)
	return a.waitPolicy("mcp/agents/"+name, func(v any) bool {
		m, _ := v.(map[string]any)
		perms, _ := m["permissions"].([]any)
		paused, _ := m["paused"].(bool)
		owners, _ := m["owners"].([]any)
		if m == nil || m["mode"] != d.Mode || paused != d.Paused || len(perms) != len(d.Permissions) || len(owners) != len(d.Owners) {
			return false
		}
		for i, o := range d.Owners {
			if owners[i] != o {
				return false
			}
		}
		cats, _ := m["categories"].(map[string]any)
		for n, c := range d.Categories {
			got, _ := cats[n].(map[string]any)
			// The promotion's id too: a promotion again keeps the mode
			// (bounded) and changes the envelope, which the gateway must
			// have before agent-admin returns.
			if got == nil || got["mode"] != c.Mode || (c.Promotion != "" && got["promotion"] != c.Promotion) {
				return false
			}
		}
		return true
	})
}

// recordPermissions writes root's record of a change of the agent's
// permissions, with them before and after (§10): readiness judges each
// call by the permissions in force at its time (O11).
func (a *Admin) recordPermissions(name string, from, to []map[string]any) {
	f, _ := json.Marshal(from)
	t, _ := json.Marshal(to)
	if string(f) == string(t) {
		return
	}
	a.record(journal.PermissionsChanged, name, fmt.Sprintf("%s changed the permissions of agent %s (%d → %d)", a.who(), name, len(from), len(to)),
		journal.Fields{"PERMISSIONS_FROM": string(f), "PERMISSIONS_TO": string(t), "BY": a.who()})
}

// who is the person for a record: By, else root.
func (a *Admin) who() string {
	if a.By != "" {
		return a.By
	}
	return "root"
}

// SetMode sets the autonomy mode, enforced by the gateway.
func (a *Admin) SetMode(name, mode string) error {
	if err := checkName(name); err != nil {
		return err
	}
	switch mode {
	case "advisory", "supervised", "bounded":
	default:
		return fmt.Errorf("mode %q: want advisory, supervised or bounded", mode)
	}
	d, err := a.readGateway(name)
	if err != nil {
		return err
	}
	d.Mode = mode
	return a.writeGateway(name, d)
}

// ownerRE is an owner rule: the gateway's approver forms but "self"
// (owners.rego).
var ownerRE = regexp.MustCompile(`^(user|group|role|remote-user|remote-group):[A-Za-z0-9._@-][A-Za-z0-9._@:-]{0,127}$`)

// SetOwners sets who may approve the agent's calls besides the servers'
// approvers (§7, O1); none clears them. An agent's own user or group is
// refused: an agent never approves.
func (a *Admin) SetOwners(name string, owners []string) error {
	if err := checkName(name); err != nil {
		return err
	}
	seen := map[string]bool{}
	var out []string
	for _, o := range owners {
		switch {
		case o == "self":
			return errors.New(`owner "self" would be the agent itself: name a person, a group or a role`)
		case !ownerRE.MatchString(o):
			return fmt.Errorf("owner %q: want user:NAME, group:NAME, role:NAME, remote-user:SUBJECT or remote-group:NAME", o)
		case strings.HasPrefix(o, "user:agent-") || strings.HasPrefix(o, "group:agent-"):
			return fmt.Errorf("owner %q is an agent's account: an agent never approves", o)
		}
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	d, err := a.readGateway(name)
	if err != nil {
		return err
	}
	d.Owners = out
	if err := a.writeGateway(name, d); err != nil {
		return err
	}
	if len(out) == 0 {
		fmt.Fprintf(a.Out, "agent %s: no owners; the servers' approvers decide its calls\n", name)
	} else {
		fmt.Fprintf(a.Out, "agent %s: %s may approve its calls\n", name, strings.Join(out, ", "))
	}
	return nil
}

// SetPermissions replaces the agent's gateway permissions from a YAML list.
func (a *Admin) SetPermissions(name string, from io.Reader) error {
	if err := checkName(name); err != nil {
		return err
	}
	b, err := io.ReadAll(from)
	if err != nil {
		return err
	}
	perms, err := a.parsePermissions(b)
	if err != nil {
		return err
	}
	if err := a.checkEnabled(name, "permissions", perms); err != nil {
		return err
	}
	d, err := a.readGateway(name)
	if err != nil {
		return err
	}
	d.Permissions = perms
	return a.writeGateway(name, d)
}

// parsePermissions reads and checks a YAML list of gateway permissions.
func (a *Admin) parsePermissions(b []byte) ([]map[string]any, error) {
	var perms []map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&perms); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("permissions: %w", err)
	}
	for i, p := range perms {
		if _, ok := p["server"].(string); !ok {
			return nil, fmt.Errorf("permissions[%d]: server missing", i)
		}
		for k := range p {
			switch k {
			case "server", "tool", "resource", "prompt", "args", "read_only", "autonomous", "approval_channel", "approval_scopes", "effect", "obligations":
			case "category":
				c, ok := p["category"].(string)
				if !ok {
					return nil, fmt.Errorf("permissions[%d]: category is a name", i)
				}
				if err := a.checkCategory(c); err != nil {
					return nil, fmt.Errorf("permissions[%d]: %w", i, err)
				}
				if p["autonomous"] != nil {
					return nil, fmt.Errorf("permissions[%d]: autonomous is for permissions without a category; a category is promoted with agent-admin promote", i)
				}
				if c == Diagnostics && p["read_only"] != true {
					return nil, fmt.Errorf("permissions[%d]: category diagnostics only reads: add read_only: true, or use another category", i)
				}
			default:
				return nil, fmt.Errorf("permissions[%d]: unknown key %q", i, k)
			}
		}
	}
	if perms == nil {
		perms = []map[string]any{}
	}
	return perms, nil
}

// Pause holds an agent: the gateway denies its calls (enforced) and the
// agent holds its activities (graceful).
func (a *Admin) Pause(name, by string, on bool) error {
	if err := checkName(name); err != nil {
		return err
	}
	path := "/v1/resume"
	if on {
		path = "/v1/pause"
	}
	sock := filepath.Join(SocketDir, name, "api.sock")
	d, err := a.readGateway(name)
	if errors.Is(err, os.ErrNotExist) {
		// An agent the gateway has no data for (another host's, §3): the
		// gateway denies it everything already; the host's part (§8) is
		// all there is.
		if err := a.Control(sock, path); err != nil {
			return fmt.Errorf("agent %s: the gateway has no data for it, and its host was not reached: %w", name, err)
		}
		fmt.Fprintf(a.Out, "agent %s: the gateway has no data for it (it denies it everything); its host was told\n", name)
		return nil
	}
	if err != nil {
		return err
	}
	d.Paused, d.PausedBy = on, ""
	if on {
		d.PausedBy = by + " " + a.Now().UTC().Format(time.RFC3339)
	}
	if err := a.writeGateway(name, d); err != nil {
		return err
	}
	if err := a.Control(sock, path); err != nil {
		fmt.Fprintf(a.Out, "agent %s: the gateway denies its calls; the agent itself was not reached (%v)\n", name, err)
	}
	return nil
}

// StopAll is the emergency stop: the gateway denies every agent
// everything, and every agent unit and schedule stops.
func (a *Admin) StopAll(by string) error {
	if err := a.writeStop(true, by); err != nil {
		return err
	}
	// Root's record: readiness counts an approved call whose activity it
	// stopped as an incident (O11).
	a.record(journal.AgentsStopped, "", by+" stopped every agent (emergency stop)", journal.Fields{"BY": by})
	err := a.Run(nil, "systemctl", "stop", "agent-schedule@*.timer", "agent@*.socket", "agent@*.service",
		"agent-openshell@*.socket", "agent-openshell@*.service")
	// Sandboxes outlive their relay unit: stop them by name as well.
	names, _ := a.List()
	for _, n := range names {
		if cfg, e := config.Load(a.agentDir(n)); e == nil && cfg.Sandbox.Kind == "openshell" {
			a.Run(nil, "openshell", "-g", cfg.Sandbox.OpenShellGateway, "sandbox", "stop", openshell.SandboxName(n))
		}
	}
	return err
}

// writeStop writes the emergency stop's flag (data.mcp.agents._control).
func (a *Admin) writeStop(stopped bool, by string) error {
	b := fmt.Sprintf("# The emergency stop of every agent (agent-admin stop-all, release-all;\n# custos CONTRACT.md §8).\n_control:\n  stopped: %t\n  by: %q\n  at: %q\n",
		stopped, by, a.Now().UTC().Format(time.RFC3339))
	if err := writeFile(a.p(GatewayStop), []byte(b), 0o644); err != nil {
		return err
	}
	return a.waitPolicy("mcp/agents/_control/stopped", func(v any) bool { return v == stopped })
}

// waitPolicy waits until the gateway's OPA has loaded what was just
// written, so that a mode, a pause or the emergency stop is in force when
// agent-admin returns. Without the gateway's OPA it only says so.
func (a *Admin) waitPolicy(path string, loaded func(any) bool) error {
	if a.OPAData == nil {
		return nil
	}
	deadline := a.Now().Add(a.PolicyWait)
	var last error
	for {
		v, err := a.OPAData(path)
		if err == nil && loaded(v) {
			return nil
		}
		if err != nil {
			last = err
			if errors.Is(err, errNoOPA) {
				fmt.Fprintf(a.Out, "agent-admin: mcp-gateway's OPA is not running; the change is in force when it loads %s\n", GatewayAgents)
				return nil
			}
		}
		if !a.Now().Before(deadline) {
			if last != nil {
				return fmt.Errorf("the gateway's policy did not load the change within %s: %w", a.PolicyWait, last)
			}
			return fmt.Errorf("the gateway's policy did not load the change within %s (%s)", a.PolicyWait, path)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// OPASocket is mcp-gateway's OPA (mcp-opa.service).
const OPASocket = "/run/mcp-gateway/opa.sock"

var errNoOPA = errors.New("mcp-gateway's OPA is not running")

func opaData(path string) (any, error) {
	if _, err := os.Stat(OPASocket); err != nil {
		return nil, errNoOPA
	}
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", OPASocket)
	}}}
	res, err := c.Get("http://opa/v1/data/" + path)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Result any `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

// ReleaseAll undoes StopAll: sockets and timers that are enabled start
// again. One an administrator disabled (an agent kept stopped, a paused
// schedule) stays stopped.
func (a *Admin) ReleaseAll(by string) error {
	if err := a.writeStop(false, by); err != nil {
		return err
	}
	a.record(journal.AgentsReleased, "", by+" released the emergency stop", journal.Fields{"BY": by})
	names, err := a.List()
	if err != nil {
		return err
	}
	var units []string
	for _, n := range names {
		cfg, err := config.Load(a.agentDir(n))
		if err == nil && cfg.Sandbox.Kind == "openshell" {
			units = append(units, "agent-openshell@"+n+".socket")
		} else {
			units = append(units, "agent@"+n+".socket")
		}
		if err == nil {
			for _, s := range cfg.Schedules {
				units = append(units, scheduleUnit(n, s.Name)+".timer")
			}
		}
	}
	var start []string
	for _, u := range units {
		if a.enabled(u) {
			start = append(start, u)
		}
	}
	if len(start) == 0 {
		return nil
	}
	return a.Run(nil, "systemctl", append([]string{"start"}, start...)...)
}

// enabled reports whether a unit is enabled (systemctl is-enabled).
func (a *Admin) enabled(unit string) bool {
	probe := a.Probe
	if probe == nil {
		probe = func(name string, args ...string) error { return a.Run(nil, name, args...) }
	}
	return probe("systemctl", "is-enabled", "--quiet", unit) == nil
}

// Trigger starts a schedule's activity on the agent's socket, as root
// with origin schedule (CONTRACT.md §4). agent-schedule@<name>_<schedule>.service
// (instance NAME_SCHEDULE) runs it; it is here and not in agent-launcher because systemd would
// run the launcher's program in agent_t, which may not reach agents'
// sockets (§13 of the contract, SELinux).
func (a *Admin) Trigger(instance string) error {
	name, sched, ok := strings.Cut(instance, "_")
	if !ok || checkName(name) != nil {
		return fmt.Errorf("%q: want NAME_SCHEDULE", instance)
	}
	cfg, err := config.Load(a.agentDir(name))
	if err != nil {
		return err
	}
	for _, s := range cfg.Schedules {
		if s.Name == sched {
			body := map[string]any{
				"prompt": s.Prompt,
				"origin": map[string]any{"kind": "schedule", "by": "agent-schedule@" + instance + ".timer", "schedule": sched},
			}
			if s.Playbook != "" {
				body["playbook"] = s.Playbook
			}
			return a.Start(filepath.Join(SocketDir, name, "api.sock"), body)
		}
	}
	return fmt.Errorf("agent %s has no schedule %q", name, sched)
}

// Posture is what the gateway enforces for the agents (§6, §8), as
// agent-admin show prints it: each agent's data and the emergency stop.
type Posture struct {
	Agents  map[string]*GatewayData `json:"agents"`
	Control Control                 `json:"control"`
	// Enablement is the enablement state (§6b); absent: every category.
	Enablement *Enablement `json:"enablement,omitempty"`
}

// Control is the emergency stop's state (_control.yaml, §8).
type Control struct {
	Stopped bool   `yaml:"stopped" json:"stopped"`
	By      string `yaml:"by,omitempty" json:"by,omitempty"`
	At      string `yaml:"at,omitempty" json:"at,omitempty"`
}

// Show reads the gateway data of names, or of every agent that has some,
// and the emergency stop. The files are readable by everyone, so a
// control plane can show the posture without root.
func (a *Admin) Show(names ...string) (*Posture, error) {
	if len(names) == 0 {
		m, err := filepath.Glob(a.p(filepath.Join(GatewayAgents, "*.yaml")))
		if err != nil {
			return nil, err
		}
		for _, f := range m {
			if n := strings.TrimSuffix(filepath.Base(f), ".yaml"); config.ValidName(n) {
				names = append(names, n)
			}
		}
		sort.Strings(names)
	}
	p := &Posture{Agents: map[string]*GatewayData{}}
	for _, n := range names {
		if err := checkName(n); err != nil {
			return nil, err
		}
		d, err := a.readGateway(n)
		if err != nil {
			return nil, err
		}
		if d.Permissions == nil {
			d.Permissions = []map[string]any{}
		}
		p.Agents[n] = d
	}
	b, err := os.ReadFile(a.p(GatewayStop))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var doc struct {
		Control Control `yaml:"_control"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", a.p(GatewayStop), err)
	}
	p.Control = doc.Control
	if p.Enablement, err = a.GetEnablement(); err != nil {
		return nil, err
	}
	if p.Enablement != nil {
		p.Enablement.Tampered = a.enablementTampered(p.Enablement)
	}
	if err := a.checkSeal(p); err != nil {
		return nil, err
	}
	return p, nil
}

// List returns the agents' names (§11: an agent exists when its agent.yaml does).
func (a *Admin) List() ([]string, error) {
	m, err := filepath.Glob(a.p(filepath.Join(config.ConfigRoot, "*", "agent.yaml")))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range m {
		if n := filepath.Base(filepath.Dir(f)); config.ValidName(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func scheduleUnit(agent, sched string) string { return "agent-schedule@" + agent + "_" + sched }

// syncSchedules writes a timer drop-in per schedule and removes the
// ones of schedules that are gone.
func (a *Admin) syncSchedules(cfg *config.Config) error {
	want := map[string]bool{} // unit → new (no drop-in before)
	for _, s := range cfg.Schedules {
		unit := scheduleUnit(cfg.Name, s.Name)
		dropIn := a.p(filepath.Join(UnitDir, unit+".timer.d", "schedule.conf"))
		_, err := os.Stat(dropIn)
		want[unit] = errors.Is(err, os.ErrNotExist)
		conf := fmt.Sprintf("# agent-admin: schedule %s of agent %s\n[Timer]\nOnCalendar=\nOnCalendar=%s\n", s.Name, cfg.Name, s.OnCalendar)
		if err := writeFile(dropIn, []byte(conf), 0o644); err != nil {
			return err
		}
	}
	old, _ := filepath.Glob(a.p(filepath.Join(UnitDir, "agent-schedule@"+cfg.Name+"_*.timer.d")))
	for _, d := range old {
		unit := strings.TrimSuffix(filepath.Base(d), ".timer.d")
		if _, keep := want[unit]; !keep {
			a.Run(nil, "systemctl", "disable", "--now", unit+".timer")
			if err := os.RemoveAll(d); err != nil {
				return err
			}
		}
	}
	a.Run(nil, "systemctl", "daemon-reload")
	// A new schedule's timer is enabled and started. One that was there
	// keeps what the administrator made of it: a running timer restarts
	// for its new calendar, a stopped or disabled one stays so.
	for unit, isNew := range want {
		verb := []string{"try-restart", unit + ".timer"}
		if isNew {
			verb = []string{"enable", "--now", unit + ".timer"}
		}
		if err := a.Run(nil, "systemctl", verb...); err != nil {
			return err
		}
	}
	return nil
}

// writeFile writes atomically.
func writeFile(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
