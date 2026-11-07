package admin

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/journal"
)

type rec struct {
	cmds []string
	// fail makes the commands it reports fail (systemctl is-active, say).
	fail func(cmd string) bool
	// gone answers the probes (openshell sandbox get): nil when the sandbox is there.
	gone func(cmd string) error
	// journal holds agent-admin's records.
	journal journal.Memory
}

func newAdmin(t *testing.T) (*Admin, *rec) {
	r := &rec{}
	a := &Admin{
		Root: t.TempDir(),
		Run: func(stdin io.Reader, name string, args ...string) error {
			c := name + " " + strings.Join(args, " ")
			r.cmds = append(r.cmds, c)
			if r.fail != nil && r.fail(c) {
				return errors.New("failed")
			}
			// The seal's key (#109): "encrypted" as it is, for Output's
			// decrypt below.
			if name == "systemd-creds" && len(args) == 4 && args[0] == "encrypt" && args[1] == "--name="+SealCredential {
				b, _ := io.ReadAll(stdin)
				return os.WriteFile(args[3], b, 0o600)
			}
			return nil
		},
		Output: func(_ io.Reader, name string, args ...string) ([]byte, error) {
			r.cmds = append(r.cmds, name+" "+strings.Join(args, " "))
			if name == "systemd-creds" && len(args) == 4 && args[0] == "decrypt" {
				return os.ReadFile(args[2])
			}
			return nil, errors.New("no output of " + name)
		},
		Out: io.Discard, Now: func() time.Time { return time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC) },
		Control: func(sock, path string) error {
			r.cmds = append(r.cmds, "POST "+sock+path)
			return nil
		},
		Start: func(sock string, body map[string]any) error {
			o := body["origin"].(map[string]any)
			r.cmds = append(r.cmds, "START "+sock+" "+body["prompt"].(string)+" "+o["kind"].(string)+" "+o["schedule"].(string)+" "+o["by"].(string))
			return nil
		},
		RunEnv: func(env []string, name string, args ...string) error {
			r.cmds = append(r.cmds, strings.Join(env, " ")+" "+name+" "+strings.Join(args, " "))
			return nil
		},
		// No DNS in tests: names the test resolves are the test's.
		LookupHost: func(h string) ([]string, error) { return nil, errors.New("no such host") },
		Image:      "localhost/agent:test",
		Secret:     func() string { return "0123456789abcdef0123456789abcdef" },
		// OpenShell has deleted a sandbox by the first look, unless gone says otherwise.
		Probe: func(name string, args ...string) error {
			c := name + " " + strings.Join(args, " ")
			r.cmds = append(r.cmds, c)
			if r.gone != nil {
				return r.gone(c)
			}
			return errors.New("not found")
		},
		Sleep:       func(time.Duration) {},
		SandboxGone: 5 * time.Second,
		// No records: nothing observed yet.
		JournalJSON: func(...string) ([]byte, error) { return nil, nil },
	}
	a.Journal = func(string) journal.Logger { return &r.journal }
	return a, r
}

const yml = `version: 1
name: patch-pilot
model: {provider: openai, name: m, endpoint: "http://ai:8000/v1"}
gateway: {servers: [zypp]}
schedules:
  - {name: compliance, on_calendar: "Mon..Fri 06:00", prompt: "Run the compliance check."}
`

func TestCreateModePauseDelete(t *testing.T) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string { b, _ := os.ReadFile(a.p(p)); return string(b) }
	if s := read("/etc/sysusers.d/agent-patch-pilot.conf"); !strings.Contains(s, "u agent-patch-pilot -") || !strings.Contains(s, "m agent-patch-pilot mcp-users") {
		t.Errorf("sysusers %q", s)
	}
	if s := read("/etc/mcp-gateway/policy/agents/patch-pilot.yaml"); !strings.Contains(s, "patch-pilot:\n    mode: supervised") || !strings.Contains(s, "permissions: []") {
		t.Errorf("gateway data %q", s)
	}
	if s := read("/etc/systemd/system/agent-schedule@patch-pilot_compliance.timer.d/schedule.conf"); !strings.Contains(s, "OnCalendar=Mon..Fri 06:00") {
		t.Errorf("timer %q", s)
	}
	all := strings.Join(r.cmds, "\n")
	for _, want := range []string{"systemd-sysusers", "systemctl enable --now agent@patch-pilot.socket", "systemctl enable --now agent-schedule@patch-pilot_compliance.timer"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if err := a.Create("patch-pilot", f); err == nil {
		t.Error("created twice")
	}
	if err := a.SetMode("patch-pilot", "bounded"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetMode("patch-pilot", "yolo"); err == nil {
		t.Error("bad mode accepted")
	}
	if err := a.SetPermissions("patch-pilot", strings.NewReader("- {server: zypp, tool: 'list_*', read_only: true}\n- {server: zypp, tool: install_patches, autonomous: true}\n")); err != nil {
		t.Fatal(err)
	}
	if err := a.SetPermissions("patch-pilot", strings.NewReader("- {server: zypp, tool: x, allow_everything: true}\n")); err == nil {
		t.Error("unknown permission key accepted")
	}
	if err := a.Pause("patch-pilot", "alice", true); err != nil {
		t.Fatal(err)
	}
	s := read("/etc/mcp-gateway/policy/agents/patch-pilot.yaml")
	if !testHas(s, "mode: bounded", "paused: true", "paused_by: alice 2026-11-03T12:00:00Z", "install_patches", "autonomous: true") {
		t.Errorf("gateway data %q", s)
	}
	if !strings.Contains(strings.Join(r.cmds, "\n"), "POST /run/agents/patch-pilot/api.sock/v1/pause") {
		t.Error("agent not told to pause")
	}
	if names, _ := a.List(); len(names) != 1 || names[0] != "patch-pilot" {
		t.Errorf("list %v", names)
	}
	if err := a.StopAll("alice"); err != nil {
		t.Fatal(err)
	}
	// show: every agent's data and the emergency stop, as the page reads them.
	p, err := a.Show()
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Agents["patch-pilot"]; len(p.Agents) != 1 || d == nil || d.Mode != "bounded" || !d.Paused || len(d.Permissions) != 2 ||
		!p.Control.Stopped || p.Control.By != "alice" {
		t.Errorf("show %+v %+v", p, p.Agents["patch-pilot"])
	}
	if _, err := a.Show("nobody"); err == nil {
		t.Error("show of an agent without data")
	}
	if s := read(GatewayStop); !strings.Contains(s, "_control:\n  stopped: true") {
		t.Errorf("stop file %q", s)
	}
	if err := a.ReleaseAll("bob"); err != nil {
		t.Fatal(err)
	}
	if s := read(GatewayStop); !strings.Contains(s, "stopped: false") || !strings.Contains(s, `by: "bob"`) {
		t.Errorf("after release-all: %q", s)
	}
	// Root's records of both (O11: an emergency stop is an incident).
	if s, rl := r.journal.Events(journal.AgentsStopped), r.journal.Events(journal.AgentsReleased); len(s) != 1 || s[0].Fields["BY"] != "alice" ||
		len(rl) != 1 || rl[0].Fields["BY"] != "bob" {
		t.Errorf("stop records %v, release records %v", s, rl)
	}
	os.MkdirAll(a.p(filepath.Join(SocketDir, "patch-pilot")), 0o755)
	if err := a.Delete("patch-pilot", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.agentDir("patch-pilot")); !os.IsNotExist(err) {
		t.Error("config stays")
	}
	if _, err := os.Stat(a.p("/etc/mcp-gateway/policy/agents/patch-pilot.yaml")); !os.IsNotExist(err) {
		t.Error("gateway data stays")
	}
	if _, err := os.Stat(a.p(filepath.Join(SocketDir, "patch-pilot"))); !os.IsNotExist(err) {
		t.Error("socket directory stays: the Cockpit page would list the agent")
	}
}

// agent-admin returns when the gateway's OPA has the change (here an OPA
// that lags behind), and says so when OPA never gets it.
func TestWaitsForThePolicy(t *testing.T) {
	a, _ := newAdmin(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { now = now.Add(100 * time.Millisecond); return now }
	a.PolicyWait = 5 * time.Second
	reads := 0
	a.OPAData = func(path string) (any, error) {
		reads++
		if path == "mcp/agents/_control/stopped" {
			return reads > 3, nil // loaded on the fourth read
		}
		return nil, nil
	}
	if err := a.StopAll("alice"); err != nil || reads != 4 {
		t.Errorf("stop-all: %v after %d reads", err, reads)
	}
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	a.OPAData = func(string) (any, error) { return nil, nil } // never loads
	if err := a.Create("patch-pilot", f); err == nil || !strings.Contains(err.Error(), "did not load the change") {
		t.Errorf("create with a stale gateway: %v", err)
	}
	a.OPAData = func(string) (any, error) { return nil, errNoOPA }
	if err := a.SetMode("patch-pilot", "advisory"); err != nil {
		t.Errorf("without the gateway's OPA: %v", err)
	}
	a.OPAData = func(string) (any, error) {
		return map[string]any{"mode": "bounded", "paused": false, "permissions": []any{}}, nil
	}
	if err := a.SetMode("patch-pilot", "bounded"); err != nil {
		t.Errorf("loaded at once: %v", err)
	}
}

func TestNames(t *testing.T) {
	a, _ := newAdmin(t)
	for _, bad := range []string{"../etc", "Root", "a", "x/y"} {
		if err := a.SetMode(bad, "advisory"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := a.SetCredential("patch-pilot", "../key", strings.NewReader("s")); err == nil {
		t.Error("bad credential name accepted")
	}
}

// set-credential leaves the encrypted file root's alone, whatever mode
// systemd-creds gave it.
func TestSetCredentialMode(t *testing.T) {
	a, r := newAdmin(t)
	run := a.Run
	a.Run = func(stdin io.Reader, name string, args ...string) error {
		if name == "systemd-creds" {
			os.WriteFile(args[len(args)-1], []byte("encrypted"), 0o644)
		}
		return run(stdin, name, args...)
	}
	if err := a.SetCredential("patch-pilot", "model-key", strings.NewReader("s")); err != nil {
		t.Fatal(err)
	}
	f := a.p("/etc/credstore.encrypted/agent-patch-pilot.model-key")
	if st, err := os.Stat(f); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("%s: %v %v", f, st.Mode(), err)
	}
	if !testHas(strings.Join(r.cmds, "\n"), "systemd-creds encrypt --name=agent-patch-pilot.model-key - "+f) {
		t.Errorf("commands %q", r.cmds)
	}
}

func testHas(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}

func TestTrigger(t *testing.T) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil
	if err := a.Trigger("patch-pilot_compliance"); err != nil {
		t.Fatal(err)
	}
	if want := "START /run/agents/patch-pilot/api.sock Run the compliance check. schedule compliance agent-schedule@patch-pilot_compliance.timer"; len(r.cmds) != 1 || r.cmds[0] != want {
		t.Errorf("got %v\nwant %s", r.cmds, want)
	}
	for _, bad := range []string{"patch-pilot_nope", "patch-pilot", "../x_y"} {
		if err := a.Trigger(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

const oshYml = `version: 1
name: sb
model: {provider: openai, name: m, endpoint: "http://127.0.0.1:8008/v1"}
gateway: {url: "https://gw.example.com:8443/mcp", servers: [zypp]}
sandbox: {kind: openshell}
`

func TestOpenShellAgent(t *testing.T) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(oshYml), 0o644)
	if err := a.Create("sb", f); err != nil {
		t.Fatal(err)
	}
	dir := a.agentDir("sb")
	for name, want := range map[string]string{
		"openshell-policy.yaml":  "run_as_user: sandbox",
		"openshell-profile.yaml": "id: custos-gateway-sb",
		"openshell.env":          "OPENSHELL_GATEWAY=openshell",
		"relay.secret":           "0123456789abcdef",
	} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !strings.Contains(string(b), want) {
			t.Errorf("%s: %v, lacks %q:\n%s", name, err, want, b)
		}
	}
	if st, _ := os.Stat(filepath.Join(dir, "relay.secret")); st.Mode().Perm() != 0o600 {
		t.Errorf("relay.secret mode %v", st.Mode())
	}
	all := strings.Join(r.cmds, "\n")
	if !strings.Contains(all, "openshell -g openshell profile import -f "+filepath.Join(dir, "openshell-profile.yaml")+" --global") {
		t.Errorf("profile not imported:\n%s", all)
	}
	if strings.Contains(all, "agent@sb.socket") || strings.Contains(all, "sandbox create") {
		t.Errorf("no systemd socket and no sandbox before the token:\n%s", all)
	}

	r.cmds = nil
	if err := a.SetCredential("sb", "gateway-token", strings.NewReader("tok-123\n")); err != nil {
		t.Fatal(err)
	}
	all = strings.Join(r.cmds, "\n")
	for _, want := range []string{
		"AGENT_GATEWAY_TOKEN=tok-123 openshell -g openshell provider create --name agent-sb-gateway --type custos-gateway-sb --credential AGENT_GATEWAY_TOKEN",
		"openshell -g openshell sandbox create --name agent-sb --from localhost/agent:test --policy " + filepath.Join(dir, "openshell-policy.yaml"),
		"--env CUSTOS_RELAY_SECRET=0123456789abcdef0123456789abcdef",
		"systemctl enable --now agent-openshell@sb.socket",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if strings.Contains(all, "tok-123 ") && strings.Count(all, "tok-123") != 1 {
		t.Error("token on a command line")
	}

	r.cmds = nil
	a.StopAll("alice")
	all = strings.Join(r.cmds, "\n")
	if !strings.Contains(all, "agent-openshell@*.service") || !strings.Contains(all, "openshell -g openshell sandbox stop agent-sb") {
		t.Errorf("stop-all misses the sandbox:\n%s", all)
	}
	r.cmds = nil
	a.ReleaseAll("alice")
	if all = strings.Join(r.cmds, "\n"); !strings.Contains(all, "agent-openshell@sb.socket") {
		t.Errorf("release-all:\n%s", all)
	}
	r.cmds = nil
	if err := a.Delete("sb", false); err != nil {
		t.Fatal(err)
	}
	all = strings.Join(r.cmds, "\n")
	for _, want := range []string{"disable --now agent-openshell@sb.socket", "sandbox delete agent-sb", "provider delete agent-sb-gateway", "profile delete custos-gateway-sb"} {
		if !strings.Contains(all, want) {
			t.Errorf("delete misses %q:\n%s", want, all)
		}
	}
}

func TestGatewayTokenOnlyForOpenShell(t *testing.T) {
	a, _ := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	a.Create("patch-pilot", f)
	if err := a.SetCredential("patch-pilot", "gateway-token", strings.NewReader("x")); err == nil {
		t.Error("gateway-token accepted for a systemd agent")
	}
}

const oshOAuthYml = `version: 1
name: sb
model: {provider: openai, name: m, endpoint: "http://127.0.0.1:8008/v1"}
gateway:
  url: "https://gw.example.com:8443/mcp"
  servers: [zypp]
  oauth: {token_url: "https://idp.example.com/token", client_id: agent-sb, scopes: [mcp]}
sandbox: {kind: openshell}
`

// With an OAuth client, OpenShell's gateway mints and renews the token:
// agent-admin hands it the client and mints the first token.
func TestOpenShellAgentOAuth(t *testing.T) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(oshOAuthYml), 0o644)
	if err := a.Create("sb", f); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(a.agentDir("sb"), "openshell-profile.yaml"))
	if !strings.Contains(string(b), "strategy: oauth2_client_credentials") || !strings.Contains(string(b), "token_url: https://idp.example.com/token") {
		t.Errorf("profile without the refresh:\n%s", b)
	}
	if err := a.SetCredential("sb", "gateway-token", strings.NewReader("tok")); err == nil {
		t.Error("a static token accepted for an agent with an OAuth client")
	}

	r.cmds = nil
	if err := a.SetCredential("sb", "gateway-client-secret", strings.NewReader("s3cret-value\n")); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.cmds, "\n")
	want := []string{
		"openshell -g openshell provider create --name agent-sb-gateway --type custos-gateway-sb --runtime-credentials",
		"CUSTOS_GATEWAY_CLIENT_SECRET=s3cret-value openshell -g openshell provider refresh configure agent-sb-gateway --credential-key AGENT_GATEWAY_TOKEN --strategy oauth2-client-credentials --material client_id=agent-sb --secret-material-env client_secret=CUSTOS_GATEWAY_CLIENT_SECRET",
		"openshell -g openshell provider refresh rotate agent-sb-gateway --credential-key AGENT_GATEWAY_TOKEN",
		"openshell -g openshell sandbox create --name agent-sb",
		"systemctl enable --now agent-openshell@sb.socket",
	}
	last := -1
	for _, w := range want {
		i := strings.Index(all, w)
		if i < 0 {
			t.Errorf("missing %q in\n%s", w, all)
			continue
		}
		if i < last {
			t.Errorf("%q out of order in\n%s", w, all)
		}
		last = i
	}
	// The secret only in the environment of the one command that needs it.
	if n := strings.Count(all, "s3cret-value"); n != 1 || strings.Contains(all, "client_secret=s3cret") {
		t.Errorf("the client secret is on a command line or repeated (%d):\n%s", n, all)
	}
}

func TestClientSecretNeedsOAuth(t *testing.T) {
	a, _ := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(oshYml), 0o644)
	a.Create("sb", f)
	if err := a.SetCredential("sb", "gateway-client-secret", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "gateway.oauth") {
		t.Errorf("client secret without an OAuth client: %v", err)
	}
}

// The user whose rootless Podman runs the sandboxes goes to the relay
// (openshell.env), for its log source.
func TestOpenShellUser(t *testing.T) {
	a, _ := newAdmin(t)
	a.OpenShellUser = "osh"
	old := LookupUser
	LookupUser = func(string) (string, string, error) { return "1001", "/home/osh", nil }
	t.Cleanup(func() { LookupUser = old })
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(oshYml), 0o644)
	if err := a.Create("sb", f); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(a.agentDir("sb"), "openshell.env"))
	if !strings.Contains(string(b), "\nOPENSHELL_USER=osh\n") {
		t.Errorf("openshell.env:\n%s", b)
	}
	d, err := os.ReadFile(a.p(filepath.Join(UnitDir, "agent-openshell@sb.service.d", "openshell-user.conf")))
	if err != nil || !strings.Contains(string(d), "ReadWritePaths=-/home/osh/.local/share/containers -/run/user/1001\n") {
		t.Errorf("relay drop-in: %v\n%s", err, d)
	}
	a2, _ := newAdmin(t)
	a2.OpenShellUser = "osh\nEVIL=1"
	if err := a2.Create("sb", f); err == nil {
		t.Error("a user name with a newline was written to openshell.env")
	}
}

// inOrder reports the first of want that is missing from cmds or out of order.
func inOrder(cmds []string, want ...string) string {
	all := strings.Join(cmds, "\n")
	last := -1
	for _, w := range want {
		i := strings.Index(all[last+1:], w)
		if i < 0 {
			return w
		}
		last += 1 + i
	}
	return ""
}

func countPrefix(cmds []string, p string) int {
	n := 0
	for _, c := range cmds {
		if strings.HasPrefix(c, p) {
			n++
		}
	}
	return n
}

// openShellRunning is agent sb in OpenShell with its sandbox created.
func openShellRunning(t *testing.T, yml string) (*Admin, *rec) {
	t.Helper()
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	if err := a.Create("sb", f); err != nil {
		t.Fatal(err)
	}
	cred, secret := "gateway-token", "tok"
	if strings.Contains(yml, "oauth:") {
		cred = "gateway-client-secret"
	}
	if err := a.SetCredential("sb", cred, strings.NewReader(secret)); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil
	return a, r
}

func edit(t *testing.T, a *Admin, yml string) error {
	t.Helper()
	f := filepath.Join(t.TempDir(), "b.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	return a.Edit("sb", f)
}

// An edit of a systemd agent restarts agent@NAME.service, nothing of OpenShell's.
func TestEditSystemd(t *testing.T) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil
	os.WriteFile(f, []byte(strings.Replace(yml, "name: m", "name: m2", 1)), 0o644)
	if err := a.Edit("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.cmds, "\n")
	if !strings.HasSuffix(all, "systemctl try-restart agent@patch-pilot.service") || strings.Contains(all, "openshell") {
		t.Errorf("edit of a systemd agent:\n%s", all)
	}
	// The backend is the one it was created with.
	os.WriteFile(f, []byte(strings.Replace(oshYml, "name: sb", "name: patch-pilot", 1)), 0o644)
	if err := a.Edit("patch-pilot", f); err == nil || !strings.Contains(err.Error(), "sandbox.kind") {
		t.Errorf("a change of the backend: %v", err)
	}
}

// An edit that changes what the sandbox gets (its configuration, the
// policy) recreates the sandbox from the new files, with the image it had,
// and the relay runs again as it ran.
func TestEditOpenShellRecreatesSandbox(t *testing.T) {
	a, r := openShellRunning(t, oshYml)
	dir := a.agentDir("sb")
	a.Image = "localhost/agent:other" // CUSTOS_OPENSHELL_IMAGE now; the sandbox keeps its own
	edited := strings.Replace(oshYml, "http://127.0.0.1:8008/v1", "http://127.0.0.1:9009/v1", 1) + "limits: {max_llm_calls: 7}\n"
	if err := edit(t, a, edited); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "openshell-policy.yaml")); !strings.Contains(string(b), "port: 9009") {
		t.Errorf("policy not rewritten:\n%s", b)
	}
	if w := inOrder(r.cmds,
		"systemctl stop agent-openshell@sb.socket agent-openshell@sb.service",
		"openshell -g openshell sandbox delete agent-sb",
		"openshell -g openshell sandbox create --name agent-sb --from localhost/agent:test --policy "+filepath.Join(dir, "openshell-policy.yaml"),
		"systemctl start agent-openshell@sb.socket agent-openshell@sb.service",
	); w != "" {
		t.Errorf("missing or out of order %q:\n%s", w, strings.Join(r.cmds, "\n"))
	}
	// The new configuration is the sandbox's.
	var create string
	for _, c := range r.cmds {
		if strings.Contains(c, "sandbox create") {
			create = c
		}
	}
	i := strings.Index(create, "CUSTOS_CONFIG=")
	if i < 0 {
		t.Fatalf("no configuration: %s", create)
	}
	enc, _, _ := strings.Cut(create[i+len("CUSTOS_CONFIG="):], " ")
	sc, _ := base64.StdEncoding.DecodeString(enc)
	if !strings.Contains(string(sc), "max_llm_calls: 7") || !strings.Contains(string(sc), "host.openshell.internal:9009") {
		t.Errorf("the sandbox's configuration:\n%s", sc)
	}
	all := strings.Join(r.cmds, "\n")
	// The profile did not change; the provider and its token stay.
	if strings.Contains(all, "profile import") || strings.Contains(all, "openshell -g openshell provider") || strings.Contains(all, "agent@sb") {
		t.Errorf("edit touched the profile, the provider or systemd's units:\n%s", all)
	}
}

// After stop-all (relay stopped), the recreated sandbox is stopped and
// the relay stays down.
func TestEditOpenShellStopped(t *testing.T) {
	a, r := openShellRunning(t, oshYml)
	r.fail = func(c string) bool { return strings.HasPrefix(c, "systemctl -q is-active") }
	if err := edit(t, a, oshYml+"limits: {max_llm_calls: 7}\n"); err != nil {
		t.Fatal(err)
	}
	if w := inOrder(r.cmds, "sandbox create --name agent-sb", "openshell -g openshell sandbox stop agent-sb"); w != "" {
		t.Errorf("missing %q:\n%s", w, strings.Join(r.cmds, "\n"))
	}
	if n := countPrefix(r.cmds, "systemctl start"); n != 0 {
		t.Errorf("the relay was started:\n%s", strings.Join(r.cmds, "\n"))
	}
}

// A change of the gateway's URL changes the provider's endpoint: the
// profile is imported again before the sandbox is recreated.
func TestEditOpenShellProfile(t *testing.T) {
	a, r := openShellRunning(t, oshYml)
	dir := a.agentDir("sb")
	if err := edit(t, a, strings.Replace(oshYml, "gw.example.com:8443", "gw2.example.com:9443", 1)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "openshell-profile.yaml")); !strings.Contains(string(b), "host: gw2.example.com") {
		t.Errorf("profile not rewritten:\n%s", b)
	}
	if w := inOrder(r.cmds,
		"openshell -g openshell profile import -f "+filepath.Join(dir, "openshell-profile.yaml")+" --global",
		"openshell -g openshell sandbox create --name agent-sb",
	); w != "" {
		t.Errorf("missing or out of order %q:\n%s", w, strings.Join(r.cmds, "\n"))
	}
}

// Without a change to the sandbox's inputs (the same file again), only
// the relay restarts, if it runs.
func TestEditOpenShellUnchanged(t *testing.T) {
	a, r := openShellRunning(t, oshYml)
	if err := edit(t, a, oshYml); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.cmds, "\n")
	if !strings.HasSuffix(all, "systemctl try-restart agent-openshell@sb.service") || strings.Contains(all, "sandbox") || strings.Contains(all, "agent@sb") {
		t.Errorf("unchanged edit:\n%s", all)
	}
}

// Before set-credential there is no sandbox: the files are rewritten,
// set-credential creates the sandbox from them.
func TestEditOpenShellBeforeCredential(t *testing.T) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(oshYml), 0o644)
	if err := a.Create("sb", f); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil
	r.fail = func(c string) bool { return strings.HasPrefix(c, "systemctl -q is-enabled agent-openshell@sb.socket") }
	if err := edit(t, a, strings.Replace(oshYml, "8008", "9009", 1)); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.cmds, "\n")
	if strings.Contains(all, "sandbox") || strings.Contains(all, "systemctl start") || strings.Contains(all, "try-restart") {
		t.Errorf("edit before the sandbox exists:\n%s", all)
	}
	if b, _ := os.ReadFile(filepath.Join(a.agentDir("sb"), "openshell-policy.yaml")); !strings.Contains(string(b), "port: 9009") {
		t.Errorf("policy not rewritten:\n%s", b)
	}
}

// A static token replaced by an OAuth client: the provider must be made
// again with the secret, which agent-admin does not keep. The agent stops
// until set-credential.
func TestEditOpenShellCredentialKind(t *testing.T) {
	a, r := openShellRunning(t, oshYml)
	if err := edit(t, a, oshOAuthYml); err != nil {
		t.Fatal(err)
	}
	if w := inOrder(r.cmds,
		"openshell -g openshell profile import",
		"systemctl disable --now agent-openshell@sb.socket agent-openshell@sb.service",
		"openshell -g openshell sandbox delete agent-sb",
		"openshell -g openshell provider delete agent-sb-gateway",
	); w != "" {
		t.Errorf("missing or out of order %q:\n%s", w, strings.Join(r.cmds, "\n"))
	}
	if all := strings.Join(r.cmds, "\n"); strings.Contains(all, "sandbox create") {
		t.Errorf("a sandbox without a provider:\n%s", all)
	}
	// An OAuth agent whose scopes change keeps its client: recreated.
	a, r = openShellRunning(t, oshOAuthYml)
	if err := edit(t, a, strings.Replace(oshOAuthYml, "scopes: [mcp]", "scopes: [mcp, more]", 1)); err != nil {
		t.Fatal(err)
	}
	if w := inOrder(r.cmds, "profile import", "sandbox create --name agent-sb", "systemctl start"); w != "" {
		t.Errorf("missing %q:\n%s", w, strings.Join(r.cmds, "\n"))
	}
	if all := strings.Join(r.cmds, "\n"); strings.Contains(all, "provider delete") {
		t.Errorf("the provider was deleted:\n%s", all)
	}
}

// The OpenShell gateway is where the sandbox and provider are: an edit
// does not move them.
func TestEditOpenShellGatewayFixed(t *testing.T) {
	a, _ := openShellRunning(t, oshYml)
	err := edit(t, a, strings.Replace(oshYml, "{kind: openshell}", "{kind: openshell, openshell_gateway: other}", 1))
	if err == nil || !strings.Contains(err.Error(), "openshell_gateway") {
		t.Errorf("moving to another OpenShell gateway: %v", err)
	}
}

// A control plane gives the configuration on stdin (--from -, check -),
// without a file of its own.
func TestConfigurationFromStdin(t *testing.T) {
	a, _ := newAdmin(t)
	a.In = strings.NewReader(yml)
	if err := a.Check("-"); err != nil {
		t.Fatalf("check -: %v", err)
	}
	a.In = strings.NewReader("version: 1\nname: x\n")
	if err := a.Check("-"); err == nil {
		t.Error("check - accepted an incomplete configuration")
	}
	a.In = strings.NewReader(yml)
	if err := a.Create("patch-pilot", "-"); err != nil {
		t.Fatalf("create --from -: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.agentDir("patch-pilot"), "agent.yaml")); err != nil {
		t.Errorf("agent.yaml not installed: %v", err)
	}
	a.In = strings.NewReader(strings.Replace(yml, "Keeps", "Still keeps", 1))
	if err := a.Edit("patch-pilot", "-"); err != nil {
		t.Fatalf("edit --from -: %v", err)
	}
	a.In = nil
	if err := a.Check("-"); err == nil {
		t.Error("check - without stdin")
	}
}

// Deleting a running sandbox is only accepted at first: the new one is
// created once OpenShell no longer knows the old one (the spike on a real
// host: "sandbox 'agent-sb' already exists").
func TestEditOpenShellWaitsForDeletion(t *testing.T) {
	a, r := openShellRunning(t, oshYml)
	left := 3
	r.gone = func(c string) error {
		if strings.Contains(c, "sandbox get agent-sb") && left > 0 {
			left--
			return nil
		}
		return errors.New("not found")
	}
	if err := edit(t, a, oshYml+"limits: {max_llm_calls: 7}\n"); err != nil {
		t.Fatal(err)
	}
	if n := countPrefix(r.cmds, "openshell -g openshell sandbox get agent-sb"); n != 4 {
		t.Errorf("%d probes, want 4:\n%s", n, strings.Join(r.cmds, "\n"))
	}
	if w := inOrder(r.cmds, "sandbox delete agent-sb", "sandbox get agent-sb", "sandbox get agent-sb", "sandbox get agent-sb", "sandbox get agent-sb", "sandbox create --name agent-sb"); w != "" {
		t.Errorf("missing or out of order %q:\n%s", w, strings.Join(r.cmds, "\n"))
	}
	// A sandbox that does not go away: an error, no create.
	a, r = openShellRunning(t, oshYml)
	r.gone = func(string) error { return nil }
	if err := edit(t, a, oshYml+"limits: {max_llm_calls: 8}\n"); err == nil || !strings.Contains(err.Error(), "still there") {
		t.Errorf("a sandbox that stays: %v", err)
	}
	if all := strings.Join(r.cmds, "\n"); strings.Contains(all, "sandbox create") {
		t.Errorf("created over the old sandbox:\n%s", all)
	}
}

// agent-admin's stdout is its own words (Cockpit shows them after an
// edit); the programs it runs print to its stderr.
func TestProgramsPrintToStderr(t *testing.T) {
	dir := t.TempDir()
	out, _ := os.Create(filepath.Join(dir, "out"))
	errf, _ := os.Create(filepath.Join(dir, "err"))
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errf
	a := New()
	runErr := a.Run(nil, "sh", "-c", "echo run-out; echo run-err >&2")
	envErr := a.RunEnv([]string{"X=env-out"}, "sh", "-c", "echo $X")
	os.Stdout, os.Stderr = oldOut, oldErr
	out.Close()
	errf.Close()
	if runErr != nil || envErr != nil {
		t.Fatal(runErr, envErr)
	}
	o, _ := os.ReadFile(filepath.Join(dir, "out"))
	e, _ := os.ReadFile(filepath.Join(dir, "err"))
	if len(o) != 0 {
		t.Errorf("stdout has the programs' output: %q", o)
	}
	for _, w := range []string{"run-out", "run-err", "env-out"} {
		if !strings.Contains(string(e), w) {
			t.Errorf("stderr lacks %q: %q", w, e)
		}
	}
}

// edit enables only a schedule that is new; one that was there keeps what
// the administrator made of it (a running timer restarts for its new
// calendar, a disabled one stays disabled).
func TestEditKeepsDisabledSchedules(t *testing.T) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil
	edited := strings.Replace(yml, `"Mon..Fri 06:00"`, `"Mon..Fri 07:00"`, 1) +
		`  - {name: weekly, on_calendar: "Sun 03:00", prompt: "Report."}` + "\n"
	os.WriteFile(f, []byte(edited), 0o644)
	if err := a.Edit("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.cmds, "\n")
	if !strings.Contains(all, "systemctl try-restart agent-schedule@patch-pilot_compliance.timer") ||
		strings.Contains(all, "enable --now agent-schedule@patch-pilot_compliance.timer") {
		t.Errorf("the existing schedule:\n%s", all)
	}
	if !strings.Contains(all, "systemctl enable --now agent-schedule@patch-pilot_weekly.timer") {
		t.Errorf("the new schedule:\n%s", all)
	}
	b, _ := os.ReadFile(a.p("/etc/systemd/system/agent-schedule@patch-pilot_compliance.timer.d/schedule.conf"))
	if !strings.Contains(string(b), "OnCalendar=Mon..Fri 07:00") {
		t.Errorf("calendar %q", b)
	}
}

// release-all starts what is enabled; an agent or a schedule the
// administrator disabled stays stopped.
func TestReleaseAllKeepsDisabledUnits(t *testing.T) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f, []byte(strings.Replace(yml, "patch-pilot", "kept-off", 1)), 0o644)
	if err := a.Create("kept-off", f); err != nil {
		t.Fatal(err)
	}
	if err := a.StopAll("alice"); err != nil {
		t.Fatal(err)
	}
	// Enabled: patch-pilot's socket. Disabled: its schedule's timer, and
	// kept-off's socket and timer.
	r.gone = func(c string) error {
		if c == "systemctl is-enabled --quiet agent@patch-pilot.socket" {
			return nil
		}
		return errors.New("disabled")
	}
	r.cmds = nil
	if err := a.ReleaseAll("bob"); err != nil {
		t.Fatal(err)
	}
	var start string
	for _, c := range r.cmds {
		if strings.HasPrefix(c, "systemctl start ") {
			start = c
		}
	}
	if start != "systemctl start agent@patch-pilot.socket" {
		t.Errorf("start %q, want only the enabled socket (commands %q)", start, r.cmds)
	}
	// Nothing enabled: no start at all.
	r.gone = func(string) error { return errors.New("disabled") }
	r.cmds = nil
	a.ReleaseAll("bob")
	if all := strings.Join(r.cmds, "\n"); strings.Contains(all, "systemctl start") {
		t.Errorf("started with nothing enabled:\n%s", all)
	}
}

// A schedule that names a playbook starts it, with its prompt (if any)
// added (§14c).
func TestTriggerPlaybook(t *testing.T) {
	a, _ := newAdmin(t)
	var got map[string]any
	a.Start = func(sock string, body map[string]any) error { got = body; return nil }
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(strings.Replace(yml, `prompt: "Run the compliance check."}`, `playbook: failed-units}`, 1)), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	if err := a.Trigger("patch-pilot_compliance"); err != nil {
		t.Fatal(err)
	}
	if got["playbook"] != "failed-units" || got["prompt"] != "" {
		t.Errorf("body %v", got)
	}
}

// Pausing an agent the gateway has no data for (another host's, #112):
// the host is told; without its host either, it is an error.
func TestPauseWithoutGatewayData(t *testing.T) {
	a, r := newAdmin(t)
	if err := a.Pause("mock", "alice", true); err != nil {
		t.Fatal(err)
	}
	if len(r.cmds) != 1 || r.cmds[0] != "POST /run/agents/mock/api.sock/v1/pause" {
		t.Errorf("cmds %v", r.cmds)
	}
	if _, err := os.Stat(a.gatewayFile("mock")); !errors.Is(err, os.ErrNotExist) {
		t.Error("gateway data was written for it")
	}
	a.Control = func(string, string) error { return errors.New("connection refused") }
	if err := a.Pause("mock", "alice", false); err == nil || !strings.Contains(err.Error(), "no data for it, and its host was not reached") {
		t.Errorf("unreachable: %v", err)
	}
}
