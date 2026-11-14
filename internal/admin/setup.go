package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gatewayctl"
)

// The bundled diagnostics install (CONTRACT.md §17a, §20b): what
// custos-setup does for the installer.

// The four inference backends, in the documented order (§14, user guide
// chapter 14).
var Backends = []string{"on-host", "local-network", "customer-cloud", "vendor"}

// HealthCheck is the bundled agent: its name and template.
const HealthCheck = "health-check"

// DefaultOnHostEndpoint is a model server on this host's (OpenAI-compatible,
// vLLM's and LiteLLM's usual port).
const DefaultOnHostEndpoint = "http://127.0.0.1:8000/v1"

// SetupCredential is the credential name custos-setup stores the key as.
const SetupCredential = "model-key"

// ExecDir is mcp-gateway's exec server's command directory.
const ExecDir = "/etc/mcp-gateway/exec.d"

// GatewayServersDir is mcp-gateway's directory of server definitions;
// NetServerVendor is Custos's server custos-net (network_view: host),
// which custos-setup copies there.
const (
	GatewayServersDir = "/etc/mcp-gateway/servers.d"
	NetServerVendor   = "/usr/share/custos/gateway/servers.d/custos-net.yaml"
)

// RestartsProfile is mcp-gateway's server restarts (package
// mcp-gateway-restarts-server, mcp-gateway 1.16): its tool
// pending_restarts lists the services an update left waiting for a
// restart. It names a capability, so the gateway accepts it only from
// servers.d, where custos-setup links it when the package is installed.
const RestartsProfile = "/usr/share/mcp-gateway/profiles/restarts.yaml"

// shippedBefore are the SHA-256 digests of the exec command files earlier
// releases shipped (0.3.0; 0.4.0 and 0.5.0): a copy that is one of them
// is no administrator's change, and custos-setup replaces it with the
// current one.
var shippedBefore = map[string][]string{
	"exec.d-diagnostics.yaml": {
		"971bf1d0e0ad1d7203cda0aefbb16fe1e76bf829d4bca39c8669d8367adb14fa", // 0.3.0
		"e14650e1dd4569ffaed6f04a6fef793da92e43efe173e4f0d8e30c792dad2c8e", // 0.4.0, 0.5.0
	},
	"exec.d-disk-usage.yaml": {"7e7f23fe449a005adf26de9b12d02287f37b2f742c294c60a860aa102b6d4f33"},
}

// SetupOptions are custos-setup's arguments.
type SetupOptions struct {
	DiagnosticsOnly bool
	Backend         string
	Provider        string
	Endpoint        string
	Model           string
	// Credential is the key, read from --credential-file; nil: none.
	Credential []byte
	// Restarts offers mcp-gateway's server restarts (--restarts).
	Restarts bool
}

// Step is one thing custos-setup did: done, already (nothing to do),
// skipped (with why).
type Step struct {
	Name   string
	Result string
	Detail string
}

func (s Step) String() string {
	if s.Detail == "" {
		return s.Name + ": " + s.Result
	}
	return s.Name + ": " + s.Result + " (" + s.Detail + ")"
}

// model checks the options and gives the model defaults: the backend
// decides the provider unless --provider says otherwise.
func (o SetupOptions) model() (*ModelDefaults, error) {
	m := &ModelDefaults{Name: o.Model, Endpoint: o.Endpoint, Provider: o.Provider}
	switch o.Backend {
	case "on-host":
		if m.Provider == "" {
			m.Provider = "openai"
		}
		if m.Provider != "openai" {
			return nil, errors.New("--backend on-host: a model server on this host is provider openai")
		}
		if m.Endpoint == "" {
			m.Endpoint = DefaultOnHostEndpoint
		}
	case "local-network":
		if m.Provider == "" {
			m.Provider = "openai"
		}
		if m.Provider != "openai" {
			return nil, errors.New("--backend local-network: a model server on the local network is provider openai")
		}
		if m.Endpoint == "" {
			return nil, errors.New("--backend local-network needs --endpoint, the server's URL")
		}
	case "customer-cloud":
		if m.Provider == "" {
			return nil, errors.New("--backend customer-cloud needs --provider: vertex (Vertex AI in your project) or openai (your own server there, with --endpoint)")
		}
	case "vendor":
		if m.Provider == "" {
			m.Provider = "gemini"
		}
		if o.Credential == nil {
			return nil, errors.New("--backend vendor needs --credential-file: a vendor-hosted service needs a key")
		}
	case "":
		return nil, fmt.Errorf("--backend: one of %s", strings.Join(Backends, ", "))
	default:
		return nil, fmt.Errorf("--backend %q: want one of %s", o.Backend, strings.Join(Backends, ", "))
	}
	if o.Credential != nil {
		m.Credential = SetupCredential
	}
	if err := m.config().Check(); err != nil {
		return nil, err
	}
	return m, nil
}

// Setup is the bundled diagnostics install's setup, after the packages:
// diagnostics-only (on a fresh host), the model defaults, the health-check
// agent from its template, its credential, and the exec server's
// diagnostic commands. It is idempotent: run again, it changes only what
// differs and goes back on nothing. Without DiagnosticsOnly it writes
// nothing: the broader install needs no setup.
func (a *Admin) Setup(o SetupOptions) ([]Step, error) {
	if !o.DiagnosticsOnly {
		return []Step{{Name: "setup", Result: "skipped", Detail: "not the diagnostics-only install: nothing to set up"}}, nil
	}
	m, err := o.model()
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(o.Credential)) == 0 && o.Credential != nil {
		return nil, errors.New("--credential-file: the file is empty")
	}
	var steps []Step
	add := func(name, result, detail string) { steps = append(steps, Step{name, result, detail}) }
	const by = "custos-setup"

	// 1. diagnostics-only first: it needs a fresh host, and nothing else
	// is written if it is not one.
	e, err := a.currentEnablement()
	if err != nil {
		return steps, err
	}
	switch {
	case e == nil:
		if err := a.InitDiagnosticsOnly(by); err != nil {
			return steps, fmt.Errorf("diagnostics-only: %w", err)
		}
		add("diagnostics-only", "done", "")
	case e.State == DiagnosticsOnly:
		add("diagnostics-only", "already", "")
	default:
		add("diagnostics-only", "skipped", "this host has more categories enabled ("+strings.Join(e.Enabled, ", ")+"): not going back")
	}

	// 2. The model defaults, for the agent and any created later.
	y := renderModel(m, "")
	if cur, err := a.GetDefaults(); err == nil && cur.Model != nil && string(renderModel(cur.Model, "")) == string(y) {
		add("model defaults", "already", m.Provider+" "+m.Name)
	} else {
		if err := a.SetDefaults(y); err != nil {
			return steps, fmt.Errorf("model defaults: %w", err)
		}
		add("model defaults", "done", m.Provider+" "+m.Name)
	}

	// 3. The exec server's diagnostic commands, which the playbooks use,
	// before the agent: its session with the gateway, and the exec server
	// started for it, have the commands that are there when it starts.
	execSteps := a.setupExec()
	steps = append(steps, execSteps...)
	execAdded := false
	for _, s := range execSteps {
		execAdded = execAdded || s.Result == "done"
	}
	// The exec server reads its commands when an instance starts: one
	// running already (for another client, or from before) would offer
	// the agent none of the new ones.
	if execAdded {
		stop := a.StopInstances
		if stop == nil {
			stop = gatewayctl.StopInstances
		}
		switch n, err := stop("exec"); {
		case errors.Is(err, gatewayctl.ErrNoControl):
			add("exec instances", "skipped", "the gateway does not run: its exec instances start with the commands")
		case err != nil:
			add("exec instances", "skipped", "not stopped ("+err.Error()+"): stop them in Cockpit's Servers tab, or the new commands are offered once they end when idle")
		default:
			add("exec instances", "done", fmt.Sprintf("%d stopped: the next ones read the new commands", n))
		}
	}

	// Custos's server custos-net, for the network commands: its instances
	// see the host's network (network_view: host), which the exec server's
	// do not.
	netStep := a.setupNetServer()
	steps = append(steps, netStep)
	execAdded = execAdded || netStep.Result == "done"
	// mcp-gateway's server restarts, for update-status, when installed.
	restartsStep := a.setupRestartsServer(o.Restarts)
	steps = append(steps, restartsStep)
	execAdded = execAdded || restartsStep.Result == "done"

	// 4. The health-check agent, from its template with the defaults.
	if _, err := os.Stat(filepath.Join(a.agentDir(HealthCheck), "agent.yaml")); err == nil {
		add("agent "+HealthCheck, "already", "its configuration is kept; change it with agent-admin edit")
		if execAdded {
			// A running agent sees new commands only in a new session.
			a.Run(nil, "systemctl", "try-restart", "agent@"+HealthCheck+".service")
		}
	} else {
		if err := a.CreateFromTemplate(HealthCheck, HealthCheck, ""); err != nil {
			return steps, fmt.Errorf("agent %s: %w", HealthCheck, err)
		}
		add("agent "+HealthCheck, "done", "")
	}

	// 5. Its credential: stored again when given, so a new key replaces
	// the old one.
	if o.Credential != nil {
		cfg, err := config.Load(a.agentDir(HealthCheck))
		switch {
		case err != nil:
			return steps, err
		case cfg.Model.Credential != SetupCredential:
			add("credential", "skipped", "the agent's model does not use "+SetupCredential)
		default:
			if err := a.SetCredential(HealthCheck, SetupCredential, bytes.NewReader(o.Credential)); err != nil {
				return steps, fmt.Errorf("credential: %w", err)
			}
			add("credential", "done", SetupCredential)
			a.Run(nil, "systemctl", "try-restart", "agent@"+HealthCheck+".service")
		}
	}
	return steps, nil
}

// setupExec puts the shipped diagnostic commands into the exec server's
// directory, where the exec server is installed; one there already that
// differs is the administrator's and stays.
func (a *Admin) setupExec() []Step {
	if _, err := os.Stat(a.p(ExecDir)); err != nil {
		return []Step{{Name: "exec commands", Result: "skipped", Detail: ExecDir + " does not exist: install mcp-gateway-exec-server for the playbooks' commands"}}
	}
	var out []Step
	for _, f := range []string{"exec.d-diagnostics.yaml", "exec.d-disk-usage.yaml"} {
		src := a.p(filepath.Join(TemplatesVendor, f))
		dst := a.p(filepath.Join(ExecDir, f)) // its shipped name, as the user guide copies it
		name := "exec commands " + filepath.Base(dst)
		want, err := os.ReadFile(src)
		if err != nil {
			out = append(out, Step{name, "skipped", err.Error()})
			continue
		}
		have, err := os.ReadFile(dst)
		switch {
		case err == nil && bytes.Equal(have, want):
			out = append(out, Step{name, "already", ""})
		case err == nil && slices.Contains(shippedBefore[f], sha256Hex(have)):
			// An earlier release's copy, unchanged: the current one replaces it.
			if err := writeFile(dst, want, 0o644); err != nil {
				out = append(out, Step{name, "failed", err.Error()})
				continue
			}
			out = append(out, Step{name, "done", dst + " (an earlier release's, replaced)"})
		case err == nil:
			out = append(out, Step{name, "skipped", dst + " differs from the shipped one: kept"})
		default:
			if err := copyFile(src, dst); err != nil {
				out = append(out, Step{name, "failed", err.Error()})
				continue
			}
			out = append(out, Step{name, "done", dst})
		}
	}
	return out
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// setupNetServer copies Custos's server definition custos-net into the
// gateway's servers.d, where the gateway picks it up; one there already
// that differs is the administrator's and stays.
func (a *Admin) setupNetServer() Step {
	const name = "server custos-net"
	if _, err := os.Stat(a.p(GatewayServersDir)); err != nil {
		return Step{name, "skipped", GatewayServersDir + " does not exist"}
	}
	src := a.p(NetServerVendor)
	dst := a.p(filepath.Join(GatewayServersDir, "custos-net.yaml"))
	want, err := os.ReadFile(src)
	if err != nil {
		return Step{name, "skipped", err.Error()}
	}
	have, err := os.ReadFile(dst)
	switch {
	case err == nil && bytes.Equal(have, want):
		return Step{name, "already", ""}
	case err == nil:
		return Step{name, "skipped", dst + " differs from the shipped one: kept"}
	}
	if err := copyFile(src, dst); err != nil {
		return Step{name, "failed", err.Error()}
	}
	return Step{name, "done", dst}
}

// setupRestartsServer links mcp-gateway's profile restarts into the
// gateway's servers.d, as mcp-gateway's user guide does, when the package
// mcp-gateway-restarts-server is installed and the administrator asked
// for it (--restarts): the server holds CAP_SYS_PTRACE, so offering it is
// a choice of its own, not one the package or an earlier run made
// (security review 0.6, P1). A link that is there is kept; a file there
// that is not that link is the administrator's and stays.
func (a *Admin) setupRestartsServer(want bool) Step {
	const name = "server restarts"
	if _, err := os.Stat(a.p(RestartsProfile)); err != nil {
		return Step{name, "skipped", "mcp-gateway-restarts-server is not installed: update-status then does without the services to restart"}
	}
	if _, err := os.Stat(a.p(GatewayServersDir)); err != nil {
		return Step{name, "skipped", GatewayServersDir + " does not exist"}
	}
	dst := a.p(filepath.Join(GatewayServersDir, "restarts.yaml"))
	if target, err := os.Readlink(dst); err == nil && target == RestartsProfile {
		return Step{name, "already", ""}
	}
	if _, err := os.Lstat(dst); err == nil {
		return Step{name, "skipped", dst + " is not the link to " + RestartsProfile + ": kept"}
	}
	if !want {
		return Step{name, "skipped", "not offered: it holds CAP_SYS_PTRACE; add --restarts to offer it for update-status's services to restart"}
	}
	if err := os.Symlink(RestartsProfile, dst); err != nil {
		return Step{name, "failed", err.Error()}
	}
	return Step{name, "done", dst + " -> " + RestartsProfile}
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
