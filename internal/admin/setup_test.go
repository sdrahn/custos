package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/journal"
)

func setupHost(t *testing.T) (*Admin, *rec) {
	t.Helper()
	a, r := withTemplates(t)
	mem := &journal.Memory{}
	a.Journal = func(string) journal.Logger { return mem }
	os.MkdirAll(a.p(ExecDir), 0o755)
	os.MkdirAll(a.p(GatewayServersDir), 0o755)
	b, err := os.ReadFile("../../gateway/servers.d/custos-net.yaml")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(a.p(NetServerVendor), b, 0o644)
	a.StopInstances = func(server string) (int, error) {
		r.cmds = append(r.cmds, "stop instances of "+server)
		return 1, nil
	}
	return a, r
}

func results(steps []Step) string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Name+"="+s.Result)
	}
	return strings.Join(out, ", ")
}

// On a fresh host: diagnostics-only, the defaults, health-check, its
// credential and the exec commands; run again, nothing changes.
func TestSetupDiagnosticsOnly(t *testing.T) {
	a, r := setupHost(t)
	o := SetupOptions{DiagnosticsOnly: true, Backend: "on-host", Model: "qwen3-8b", Credential: []byte("sk-local\n")}
	steps, err := a.Setup(o)
	if err != nil {
		t.Fatal(err)
	}
	// The exec commands before the agent: its session with the gateway
	// has the commands there when it starts.
	// The exec server's running instances stop, so that the next ones
	// read the new commands.
	want := "diagnostics-only=done, model defaults=done, exec commands exec.d-diagnostics.yaml=done, " +
		"exec commands exec.d-disk-usage.yaml=done, exec instances=done, server custos-net=done, server restarts=skipped, agent health-check=done, credential=done"
	if got := results(steps); got != want {
		t.Errorf("first run:\n got %s\nwant %s", got, want)
	}
	e, _ := a.GetEnablement()
	if e == nil || e.State != DiagnosticsOnly || !strings.HasPrefix(e.ChangedBy, "custos-setup ") {
		t.Errorf("enablement %+v", e)
	}
	d, _ := a.GetDefaults()
	if d.Model == nil || d.Model.Provider != "openai" || d.Model.Endpoint != DefaultOnHostEndpoint || d.Model.Name != "qwen3-8b" ||
		d.Model.Credential != SetupCredential {
		t.Errorf("defaults %+v", d.Model)
	}
	g, err := a.readGateway(HealthCheck)
	if err != nil || g.Mode != "supervised" || len(g.Permissions) == 0 {
		t.Errorf("health-check %+v %v", g, err)
	}
	if b, _ := os.ReadFile(filepath.Join(a.agentDir(HealthCheck), "agent.yaml")); !strings.Contains(string(b), "name: qwen3-8b") ||
		!strings.Contains(string(b), "credential: model-key") {
		t.Errorf("agent.yaml has not the defaults' model:\n%s", b)
	}
	if !strings.Contains(strings.Join(r.cmds, "\n"), "systemd-creds encrypt --name=agent-health-check.model-key") {
		t.Errorf("credential not stored: %v", r.cmds)
	}
	for _, f := range []string{"exec.d-diagnostics.yaml", "exec.d-disk-usage.yaml"} {
		if _, err := os.Stat(a.p(filepath.Join(ExecDir, f))); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if b, err := os.ReadFile(a.p(filepath.Join(GatewayServersDir, "custos-net.yaml"))); err != nil || !strings.Contains(string(b), "network_view: host") {
		t.Errorf("server custos-net: %v\n%s", err, b)
	}

	// Again: nothing to do but the credential, which a new key replaces.
	steps, err = a.Setup(o)
	if err != nil {
		t.Fatal(err)
	}
	want = "diagnostics-only=already, model defaults=already, exec commands exec.d-diagnostics.yaml=already, " +
		"exec commands exec.d-disk-usage.yaml=already, server custos-net=already, server restarts=skipped, agent health-check=already, credential=done"
	if got := results(steps); got != want {
		t.Errorf("second run:\n got %s\nwant %s", got, want)
	}

	// Commands added for an agent that is there already: it is restarted,
	// for a session with them.
	os.Remove(a.p(filepath.Join(ExecDir, "exec.d-disk-usage.yaml")))
	r.cmds = nil
	if _, err := a.Setup(SetupOptions{DiagnosticsOnly: true, Backend: "on-host", Model: "qwen3-8b"}); err != nil {
		t.Fatal(err)
	}
	if c := strings.Join(r.cmds, "\n"); !strings.Contains(c, "stop instances of exec") || !strings.Contains(c, "systemctl try-restart agent@health-check.service") ||
		strings.Index(c, "stop instances of exec") > strings.Index(c, "try-restart") {
		t.Errorf("exec instances not stopped, then health-check restarted, for the new commands: %v", r.cmds)
	}
	r.cmds = nil
	a.Setup(SetupOptions{DiagnosticsOnly: true, Backend: "on-host", Model: "qwen3-8b"})
	if c := strings.Join(r.cmds, "\n"); strings.Contains(c, "try-restart") || strings.Contains(c, "stop instances") {
		t.Errorf("restarted with nothing new: %v", r.cmds)
	}

	// After the administrator enabled more, setup does not go back.
	if err := a.Enable("alice", []string{"patch-errata"}, false); err != nil {
		t.Fatal(err)
	}
	steps, _ = a.Setup(o)
	if steps[0].Result != "skipped" || !strings.Contains(steps[0].Detail, "not going back") {
		t.Errorf("after enable: %v", steps[0])
	}
	if e, _ := a.GetEnablement(); e.State != SomeCategories {
		t.Errorf("went back: %+v", e)
	}
}

// Without --diagnostics-only nothing is written: the broader install.
func TestSetupBroaderInstall(t *testing.T) {
	a, _ := setupHost(t)
	steps, err := a.Setup(SetupOptions{Backend: "on-host", Model: "m"})
	if err != nil || len(steps) != 1 || steps[0].Result != "skipped" {
		t.Fatalf("%v %v", steps, err)
	}
	if e, _ := a.GetEnablement(); e != nil {
		t.Error("enablement written")
	}
	if _, err := os.Stat(a.agentDir(HealthCheck)); !os.IsNotExist(err) {
		t.Error("agent created")
	}
}

// A host with an agent outside diagnostics is no fresh host: nothing is
// written.
func TestSetupNeedsAFreshHost(t *testing.T) {
	a, _ := setupHost(t)
	if err := a.CreateFromTemplate("pp", "patch-pilot", ""); err != nil {
		t.Fatal(err)
	}
	_, err := a.Setup(SetupOptions{DiagnosticsOnly: true, Backend: "on-host", Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "outside diagnostics") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(a.agentDir(HealthCheck)); !os.IsNotExist(err) {
		t.Error("health-check created all the same")
	}
	if d, _ := a.GetDefaults(); d.Model != nil {
		t.Error("defaults written all the same")
	}
}

// The backends, in the documented order, and what each needs.
func TestSetupBackends(t *testing.T) {
	key := []byte("k")
	for _, c := range []struct {
		o    SetupOptions
		want string // provider/endpoint, or the error
	}{
		{SetupOptions{Backend: "on-host", Model: "m"}, "openai " + DefaultOnHostEndpoint},
		{SetupOptions{Backend: "on-host", Model: "m", Endpoint: "http://127.0.0.1:11434/v1"}, "openai http://127.0.0.1:11434/v1"},
		{SetupOptions{Backend: "on-host", Model: "m", Provider: "gemini"}, "provider openai"},
		{SetupOptions{Backend: "local-network", Model: "m"}, "needs --endpoint"},
		{SetupOptions{Backend: "local-network", Model: "m", Endpoint: "http://ai.lan:8000/v1"}, "openai http://ai.lan:8000/v1"},
		{SetupOptions{Backend: "customer-cloud", Model: "m"}, "needs --provider"},
		{SetupOptions{Backend: "customer-cloud", Model: "gemini-2.5-pro", Provider: "vertex"}, "vertex "},
		{SetupOptions{Backend: "vendor", Model: "gemini-2.5-flash"}, "needs --credential-file"},
		{SetupOptions{Backend: "vendor", Model: "gemini-2.5-flash", Credential: key}, "gemini "},
		{SetupOptions{Backend: "cloud", Model: "m"}, "want one of on-host, local-network, customer-cloud, vendor"},
		{SetupOptions{Model: "m"}, "--backend: one of"},
	} {
		m, err := c.o.model()
		got := ""
		if err != nil {
			got = err.Error()
		} else {
			got = m.Provider + " " + m.Endpoint
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%+v: %s, want %s", c.o, got, c.want)
		}
	}
	if strings.Join(Backends, " ") != "on-host local-network customer-cloud vendor" {
		t.Errorf("the documented order: %v", Backends)
	}
}

// An exec command file the administrator changed stays.
func TestSetupKeepsChangedExecCommands(t *testing.T) {
	a, _ := setupHost(t)
	writeFile(a.p(filepath.Join(ExecDir, "exec.d-diagnostics.yaml")), []byte("version: 1\ncommands: {}\n"), 0o644)
	steps, err := a.Setup(SetupOptions{DiagnosticsOnly: true, Backend: "on-host", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if got := results(steps); !strings.Contains(got, "exec commands exec.d-diagnostics.yaml=skipped") || strings.Contains(got, "credential") {
		t.Errorf("%s", got)
	}
	if b, _ := os.ReadFile(a.p(filepath.Join(ExecDir, "exec.d-diagnostics.yaml"))); string(b) != "version: 1\ncommands: {}\n" {
		t.Error("overwritten")
	}
}

// An earlier release's copy of a command file, unchanged, is replaced; a
// copy the administrator changed stays.
func TestSetupReplacesAnEarlierReleasesCommands(t *testing.T) {
	a, _ := setupHost(t)
	old := []byte("version: 1\ncommands: {}\n")
	defer func(s map[string][]string) { shippedBefore = s }(shippedBefore)
	shippedBefore = map[string][]string{"exec.d-diagnostics.yaml": {sha256Hex(old)}}
	writeFile(a.p(filepath.Join(ExecDir, "exec.d-diagnostics.yaml")), old, 0o644)
	writeFile(a.p(filepath.Join(ExecDir, "exec.d-disk-usage.yaml")), []byte("# mine\n"), 0o644)
	steps, err := a.Setup(SetupOptions{DiagnosticsOnly: true, Backend: "on-host", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	got := results(steps)
	if !strings.Contains(got, "exec commands exec.d-diagnostics.yaml=done") || !strings.Contains(got, "exec commands exec.d-disk-usage.yaml=skipped") {
		t.Errorf("steps: %s", got)
	}
	want, _ := os.ReadFile(a.p(filepath.Join(TemplatesVendor, "exec.d-diagnostics.yaml")))
	if have, _ := os.ReadFile(a.p(filepath.Join(ExecDir, "exec.d-diagnostics.yaml"))); string(have) != string(want) {
		t.Error("the earlier release's copy was not replaced")
	}
	if have, _ := os.ReadFile(a.p(filepath.Join(ExecDir, "exec.d-disk-usage.yaml"))); string(have) != "# mine\n" {
		t.Error("the administrator's copy was replaced")
	}
}

// mcp-gateway's server restarts: custos-setup links its profile into
// servers.d when mcp-gateway-restarts-server is installed, keeps a file
// there that is not that link, and health-check may call its tool.
func TestSetupRestartsServer(t *testing.T) {
	a, _ := setupHost(t)
	o := SetupOptions{DiagnosticsOnly: true, Backend: "on-host", Model: "m"}
	writeFile(a.p(RestartsProfile), []byte("name: restarts\n"), 0o644)
	dst := a.p(filepath.Join(GatewayServersDir, "restarts.yaml"))
	// Installed but not asked for (security review 0.6, P1): not linked,
	// and the step says how.
	steps, err := a.Setup(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := results(steps); !strings.Contains(got, "server restarts=skipped") {
		t.Errorf("without --restarts: %s", got)
	}
	for _, s := range steps {
		if s.Name == "server restarts" && !strings.Contains(s.Detail, "--restarts") {
			t.Errorf("the step does not say how: %v", s)
		}
	}
	if _, err := os.Lstat(dst); err == nil {
		t.Fatal("linked without --restarts")
	}
	o.Restarts = true
	steps, err = a.Setup(o)
	if err != nil {
		t.Fatal(err)
	}
	if got := results(steps); !strings.Contains(got, "server restarts=done") {
		t.Errorf("with --restarts: %s", got)
	}
	if target, err := os.Readlink(dst); err != nil || target != RestartsProfile {
		t.Errorf("link %q %v", target, err)
	}
	g, err := a.readGateway(HealthCheck)
	if err != nil {
		t.Fatal(err)
	}
	var allowed bool
	for _, p := range g.Permissions {
		if p["server"] == "restarts" && p["tool"] == "pending_restarts" && p["read_only"] == true && p["category"] == "diagnostics" {
			allowed = true
		}
	}
	if !allowed {
		t.Errorf("health-check may not call restarts__pending_restarts: %+v", g.Permissions)
	}
	if steps, _ := a.Setup(o); !strings.Contains(results(steps), "server restarts=already") {
		t.Errorf("second run: %s", results(steps))
	}
	// A link that is there stays without the flag; one the administrator
	// removed is not made again without it.
	o.Restarts = false
	if steps, _ := a.Setup(o); !strings.Contains(results(steps), "server restarts=already") {
		t.Errorf("kept link: %s", results(steps))
	}
	os.Remove(dst)
	a.Setup(o)
	if _, err := os.Lstat(dst); err == nil {
		t.Error("linked again after the administrator removed it")
	}
	o.Restarts = true
	// The administrator's own definition stays.
	writeFile(dst, []byte("name: restarts\n# mine\n"), 0o644)
	steps, _ = a.Setup(o)
	if !strings.Contains(results(steps), "server restarts=skipped") {
		t.Errorf("own file: %s", results(steps))
	}
	if b, _ := os.ReadFile(dst); !strings.Contains(string(b), "# mine") {
		t.Errorf("replaced: %s", b)
	}
}
