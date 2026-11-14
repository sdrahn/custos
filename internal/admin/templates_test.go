package admin

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/category"
	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/playbook"
)

// knownTools are the tools of the gateway servers the templates use, as
// the servers define them (systemd-mcp, mcp-server-zypp 0.1.2,
// mcp-server-snapper, firewalld-mcp 0.1.0, mcp-server-tukit 0.4.0,
// mcp-gateway's mcp-server-restarts) and,
// for exec, as the shipped exec.d-*.yaml files do. read: the tools that change
// nothing. A template naming a tool not here names one that does not
// exist, and the gateway would never match it.
var knownTools = map[string]struct{ read, change []string }{
	"systemd": {
		read:   []string{"list_loaded_units", "list_unit_files", "list_log", "check_restart_reload", "get_man_page", "get_file"},
		change: []string{"change_unit_state"},
	},
	"zypp": {
		read:   []string{"search_packages", "find_providers", "find_dependents", "check_updates", "plan_install", "plan_remove"},
		change: []string{"confirm_install", "confirm_remove"},
	},
	"snapper": {
		read:   []string{"list_configs", "get_config", "list_snapshots"},
		change: []string{"create_snapshot", "delete_snapshots", "set_config", "rollback"},
	},
	"firewalld": {
		read: []string{"get_default_zone", "get_active_zones", "get_services_for_zone", "get_service_info", "is_default_zone"},
	},
	"tukit": {
		// refresh_repositories fetches the repositories' metadata only.
		read: []string{"get_status", "list_snapshots", "list_updates", "refresh_repositories"},
		change: []string{"pkg_install", "pkg_remove", "update_system", "rollback",
			"delete_snapshot", "cleanup_snapshots", "apply", "reboot", "run_in_snapshot"},
	},
	// mcp-docs (0.1) as the gateway's server suse-docs (#130): it only reads.
	"suse-docs": {
		read: []string{"list_docs", "outline", "search", "find_sections", "read_section", "read_lines"},
	},
	// mcp-gateway's server restarts (1.16, mcp-gateway-restarts-server).
	"restarts": {
		read: []string{"pending_restarts"},
	},
	"exec":       {}, // from the shipped exec.d files (init)
	"custos-net": {}, // from gateway/custos-net.d (init)
}

// The exec server's tools are the commands of the shipped exec.d files,
// custos-net's those of gateway/custos-net.d; the read-only ones among
// them change nothing.
func init() {
	for server, glob := range map[string]string{"exec": "../../templates/exec.d-*.yaml", "custos-net": "../../gateway/custos-net.d/*.yaml"} {
		knownTools[server] = commandTools(glob)
	}
}

func commandTools(glob string) struct{ read, change []string } {
	files, _ := filepath.Glob(glob)
	var k struct{ read, change []string }
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			panic(err)
		}
		var doc struct {
			Commands map[string]struct {
				ReadOnly bool `yaml:"read_only"`
			} `yaml:"commands"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			panic(f + ": " + err.Error())
		}
		for n, c := range doc.Commands {
			if c.ReadOnly {
				k.read = append(k.read, n)
			} else {
				k.change = append(k.change, n)
			}
		}
	}
	return k
}

// Changes that alter what runs: never in a category, so they need an
// approval whatever is promoted. run_in_snapshot is never allowed.
var (
	alwaysApproved = []string{"tukit apply", "tukit reboot", "tukit rollback", "snapper rollback", "tukit delete_snapshot", "snapper delete_snapshots"}
	neverAllowed   = []string{"tukit run_in_snapshot"}
)

// withTemplates is an agent-admin with the shipped categories and
// templates installed.
func withTemplates(t *testing.T) (*Admin, *rec) {
	t.Helper()
	a, r := newAdmin(t)
	m, _ := filepath.Glob("../../packaging/categories/*.yaml")
	for _, f := range m {
		b, _ := os.ReadFile(f)
		host.WriteFile(a.Path(filepath.Join(category.Vendor, filepath.Base(f))), b, 0o644)
	}
	copyTree(t, "../../templates", a.Path(TemplatesVendor))
	return a, r
}

// copyTemplate copies a shipped template as another one.
func copyTemplate(t *testing.T, from, name, to string) {
	t.Helper()
	copyTree(t, filepath.Join("../../templates", from), to)
	b, _ := os.ReadFile(filepath.Join(to, "agent.yaml"))
	host.WriteFile(filepath.Join(to, "agent.yaml"), WithName(b, name), 0o644)
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return host.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readPerms(t *testing.T, f string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	var perms []map[string]any
	if err := yaml.Unmarshal(b, &perms); err != nil {
		t.Fatalf("%s: %v", f, err)
	}
	return perms
}

// checkTools checks that a permissions file names only existing tools,
// marks read-only exactly the tools that change nothing, and keeps the
// tools that change what runs out of every category.
func checkTools(t *testing.T, f string, template bool) {
	t.Helper()
	for i, p := range readPerms(t, f) {
		srv, _ := p["server"].(string)
		tool, _ := p["tool"].(string)
		k, ok := knownTools[srv]
		if !ok {
			t.Errorf("%s[%d]: server %q is not one the templates know", f, i, srv)
			continue
		}
		ro := p["read_only"] == true
		switch {
		case slices.Contains(k.read, tool):
			if !ro {
				t.Errorf("%s[%d]: %s %s changes nothing: mark it read_only", f, i, srv, tool)
			}
		case slices.Contains(k.change, tool):
			if ro {
				t.Errorf("%s[%d]: %s %s changes the system: it is not read-only", f, i, srv, tool)
			}
		default:
			t.Errorf("%s[%d]: %s has no tool %q", f, i, srv, tool)
		}
		if slices.Contains(alwaysApproved, srv+" "+tool) && p["category"] != nil {
			t.Errorf("%s[%d]: %s %s must need an approval every time: no category", f, i, srv, tool)
		}
		if slices.Contains(neverAllowed, srv+" "+tool) {
			t.Errorf("%s[%d]: %s %s is never allowed", f, i, srv, tool)
		}
		if template && p["autonomous"] != nil {
			t.Errorf("%s[%d]: a template promotes nothing (autonomous)", f, i)
		}
	}
}

func TestShippedTemplates(t *testing.T) {
	a, _ := withTemplates(t)
	l, err := a.ListTemplates()
	if err != nil {
		t.Fatal(err)
	}
	dirs, _ := filepath.Glob("../../templates/*/template.yaml")
	if len(l.Templates) != len(dirs) || len(dirs) < 7 {
		t.Fatalf("%d templates listed, %d shipped", len(l.Templates), len(dirs))
	}
	for name, tm := range l.Templates {
		if tm.Error != "" {
			t.Errorf("template %s: %s", name, tm.Error)
			continue
		}
		src := filepath.Join("../../templates", name)
		checkTools(t, filepath.Join(src, "permissions.yaml"), true)
		cfg, err := config.Load(src)
		if err != nil {
			t.Fatalf("template %s: %v", name, err)
		}
		perms := readPerms(t, filepath.Join(src, "permissions.yaml"))
		// What the model is shown is what it is allowed, and every
		// permission's server is one the agent uses.
		for _, x := range cfg.Gateway.Tools {
			srv, tool, _ := strings.Cut(x, "__")
			if !slices.ContainsFunc(perms, func(p map[string]any) bool { return p["server"] == srv && p["tool"] == tool }) {
				t.Errorf("template %s: gateway.tools %s has no permission", name, x)
			}
		}
		for _, p := range perms {
			srv, tool := p["server"].(string), p["tool"].(string)
			if !slices.Contains(cfg.Gateway.Servers, srv) {
				t.Errorf("template %s: permission for %s, which gateway.servers does not name", name, srv)
			}
			if len(cfg.Gateway.Tools) > 0 && !slices.Contains(cfg.Gateway.Tools, srv+"__"+tool) {
				t.Errorf("template %s: %s %s is allowed but not in gateway.tools", name, srv, tool)
			}
			if tm.Mode == "advisory" && p["read_only"] != true {
				t.Errorf("template %s: advisory, but %s %s changes the system (advisory denies it)", name, srv, tool)
			}
		}
		// The transactional variants use tukit for changes, the classic
		// ones never.
		for _, p := range perms {
			if p["read_only"] == true {
				continue
			}
			switch {
			case tm.System == "classic" && p["server"] == "tukit":
				t.Errorf("template %s: classic, but changes through tukit", name)
			case tm.System == "transactional" && (p["server"] == "zypp" || p["server"] == "snapper"):
				t.Errorf("template %s: transactional, but changes through %s", name, p["server"])
			}
		}
		if strings.TrimSpace(cfg.Instruction) == "" || tm.Description == "" || len(tm.Needs) == 0 {
			t.Errorf("template %s: instruction, description and needs are required", name)
		}
	}
	if pp := l.Templates["patch-pilot"]; !slices.Equal(pp.Categories, []string{"config-lifecycle", "patch-errata"}) || !slices.Equal(pp.Envelopes, []string{"patch-errata"}) {
		t.Errorf("patch-pilot: categories %v, envelopes %v", pp.Categories, pp.Envelopes)
	}
	if l.Transactional {
		t.Error("no /proc/self/mounts in the test root: not transactional")
	}
}

// The example installed beside the templates names real tools, too.
func TestExampleTools(t *testing.T) {
	checkTools(t, "../../examples/patch-pilot/permissions.yaml", false)
	if _, err := config.Load("../../examples/patch-pilot"); err != nil {
		t.Fatal(err)
	}
}

func TestCreateFromTemplate(t *testing.T) {
	a, r := withTemplates(t)
	if err := a.CreateFromTemplate("web-doctor", "service-doctor", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.Path("/etc/custos/agents/web-doctor/agent.yaml"))
	if !strings.Contains(string(b), "\nname: web-doctor\n") || !strings.Contains(string(b), "# Agent template service-doctor") {
		t.Errorf("agent.yaml:\n%s", b)
	}
	d, err := a.readGateway("web-doctor")
	if err != nil {
		t.Fatal(err)
	}
	if d.Mode != "supervised" || len(d.Permissions) != len(readPerms(t, "../../templates/service-doctor/permissions.yaml")) {
		t.Errorf("gateway data %+v", d)
	}
	if len(d.Categories) != 0 {
		t.Errorf("a template promotes nothing: %+v", d.Categories)
	}
	if !slices.Contains(r.cmds, "systemctl enable --now agent@web-doctor.socket") {
		t.Errorf("commands %q", r.cmds)
	}

	// The configuration as a control plane edited it, with the template's
	// permissions and mode.
	src, _ := os.ReadFile("../../templates/triage/agent.yaml")
	edited := strings.Replace(string(WithName(src, "look")), "qwen3-32b", "granite-4", 1)
	a.In = strings.NewReader(edited)
	if err := a.CreateFromTemplate("look", "triage", "-"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(a.Path("/etc/custos/agents/look/agent.yaml")); !strings.Contains(string(b), "granite-4") {
		t.Errorf("agent.yaml:\n%s", b)
	}
	if d, _ := a.readGateway("look"); d.Mode != "advisory" {
		t.Errorf("mode %q", d.Mode)
	}

	// A configuration for another name is refused, and no agent made.
	a.In = strings.NewReader(string(src))
	if err := a.CreateFromTemplate("other", "triage", "-"); err == nil || !strings.Contains(err.Error(), `for agent "triage", not "other"`) {
		t.Errorf("err %v", err)
	}
	if err := a.CreateFromTemplate("x1", "no-such", ""); err == nil || !strings.Contains(err.Error(), `no template "no-such"`) {
		t.Errorf("err %v", err)
	}
	if _, err := os.Stat(a.Path("/etc/custos/agents/x1")); err == nil {
		t.Error("agent x1 made")
	}
}

func TestSiteTemplates(t *testing.T) {
	a, _ := withTemplates(t)
	// An administrator's template of a shipped name replaces it; a
	// directory without template.yaml hides one.
	copyTree(t, "../../templates/triage", a.Path(filepath.Join(TemplatesSite, "triage")))
	host.WriteFile(a.Path(filepath.Join(TemplatesSite, "triage", "template.yaml")), []byte("title: Our triage\nsystem: any\nmode: advisory\n"), 0o644)
	os.MkdirAll(a.Path(filepath.Join(TemplatesSite, "exposure-watch")), 0o755)
	// One naming a category the catalog lacks is listed with its error.
	copyTemplate(t, "disk-watch", "cleaner", a.Path(filepath.Join(TemplatesSite, "cleaner")))
	host.WriteFile(a.Path(filepath.Join(TemplatesSite, "cleaner", "permissions.yaml")), []byte("- {server: snapper, tool: delete_snapshots, category: tidy}\n"), 0o644)
	l, err := a.ListTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if l.Templates["triage"].Title != "Our triage" {
		t.Errorf("triage %+v", l.Templates["triage"])
	}
	if _, ok := l.Templates["exposure-watch"]; ok {
		t.Error("exposure-watch not hidden")
	}
	if e := l.Templates["cleaner"].Error; !strings.Contains(e, `no task category "tidy"`) {
		t.Errorf("cleaner: %q", e)
	}
	if err := a.CreateFromTemplate("watch", "exposure-watch", ""); err == nil {
		t.Error("created from a hidden template")
	}
}

func TestTransactional(t *testing.T) {
	a, _ := newAdmin(t)
	mounts := "/dev/vda3 / btrfs ro,relatime,ssd,subvol=/@/.snapshots/5/snapshot 0 0\n/dev/vda3 /var btrfs rw,relatime 0 0\n"
	host.WriteFile(a.Path("/proc/self/mounts"), []byte(mounts), 0o644)
	if !a.transactional() {
		t.Error("read-only root: transactional")
	}
	host.WriteFile(a.Path("/proc/self/mounts"), []byte(strings.Replace(mounts, "ro,", "rw,", 1)), 0o644)
	if a.transactional() {
		t.Error("writable root: not transactional")
	}
}

func TestBadTemplate(t *testing.T) {
	a, _ := withTemplates(t)
	dir := a.Path(filepath.Join(TemplatesSite, "bad"))
	for _, c := range []struct{ file, body, want string }{
		{"template.yaml", "title: x\nsystem: any\nmode: bounded\n", "never created bounded"},
		{"template.yaml", "title: x\nsystem: suse\nmode: supervised\n", "want classic, transactional or any"},
		{"template.yaml", "title: x\nsystem: any\nmode: supervised\nextra: 1\n", "field extra not found"},
		{"agent.yaml", "version: 1\nname: other\nmodel: {provider: openai, name: m, endpoint: 'http://127.0.0.1:1/v1'}\ngateway: {servers: [systemd]}\n", "want the template's name"},
		{"envelopes/patch-errata.yaml", "windows: [{from: '01:00', to: '02:00'}]\n", "no permission has category patch-errata"},
		{"envelopes/config-lifecycle.yaml", "windows: []\n", "no windows"},
	} {
		t.Run(c.file, func(t *testing.T) {
			os.RemoveAll(dir)
			copyTemplate(t, "service-doctor", "bad", dir)
			host.WriteFile(filepath.Join(dir, c.file), []byte(c.body), 0o644)
			_, err := a.template("bad")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err %v, want %q", err, c.want)
			}
		})
	}
}

func TestWithName(t *testing.T) {
	in := "# name: keep\nversion: 1\nname: triage\ndescription: x\nmodel:\n  name: qwen3-32b\n"
	want := "# name: keep\nversion: 1\nname: mine\ndescription: x\nmodel:\n  name: qwen3-32b\n"
	if got := string(WithName([]byte(in), "mine")); got != want {
		t.Errorf("got\n%s", got)
	}
}

// agent-admin playbooks lists the shipped set (§14c).
func TestPlaybooksShipped(t *testing.T) {
	a, _ := newAdmin(t)
	m, _ := filepath.Glob("../../packaging/playbooks/*.yaml")
	for _, f := range m {
		b, _ := os.ReadFile(f)
		host.WriteFile(a.Path(filepath.Join(playbook.Vendor, filepath.Base(f))), b, 0o644)
	}
	p, err := a.Playbooks()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != len(m) || p["failed-units"] == nil || p["failed-units"].Error != "" || p["failed-units"].File != "/usr/share/custos/playbooks/failed-units.yaml" {
		t.Errorf("%d of %d: %+v", len(p), len(m), p["failed-units"])
	}
}

// health-check, the bundled diagnostics agent (§20b): read-only and in
// diagnostics only, so it runs on a diagnostics-only host; it can run
// every shipped playbook, and its schedules name shipped ones.
func TestHealthCheckTemplate(t *testing.T) {
	src := "../../templates/health-check"
	perms := readPerms(t, filepath.Join(src, "permissions.yaml"))
	allowed := map[string]bool{}
	for _, p := range perms {
		if p["read_only"] != true || p["category"] != Diagnostics {
			t.Errorf("not read-only in diagnostics: %v", p)
		}
		allowed[p["server"].(string)+"__"+p["tool"].(string)] = true
	}
	files, _ := filepath.Glob("../../packaging/playbooks/*.yaml")
	shipped := map[string]bool{}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		doc, err := playbook.Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		for n, p := range doc {
			shipped[n] = true
			for _, s := range p.Steps {
				if !allowed[s.Tool] {
					t.Errorf("playbook %s: health-check has no permission for %s", n, s.Tool)
				}
			}
		}
	}
	cfg, err := config.Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Schedules) == 0 {
		t.Error("no schedules")
	}
	for _, s := range cfg.Schedules {
		if !shipped[s.Playbook] {
			t.Errorf("schedule %s: playbook %q is not shipped", s.Name, s.Playbook)
		}
	}

	// On a diagnostics-only host it is created, supervised, with its
	// permissions; another template is refused there.
	a, _ := withTemplates(t)
	mem := &journal.Memory{}
	a.Journal = func(string) journal.Logger { return mem }
	if err := a.InitDiagnosticsOnly("setup"); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateFromTemplate("health-check", "health-check", ""); err != nil {
		t.Fatalf("on a diagnostics-only host: %v", err)
	}
	d, _ := a.readGateway("health-check")
	if d.Mode != "supervised" || len(d.Permissions) != len(perms) {
		t.Errorf("gateway data: %s, %d permissions", d.Mode, len(d.Permissions))
	}
	if err := a.CreateFromTemplate("pp", "patch-pilot", ""); err == nil {
		t.Error("patch-pilot created on a diagnostics-only host")
	}
}

// The agents that look at failed units know mcp-gateway's server
// instances for what they are (see the playbook failed-units).
func TestTemplatesKnowGatewayInstances(t *testing.T) {
	for _, name := range []string{"health-check", "triage"} {
		cfg, err := config.Load("../../templates/" + name)
		if err != nil {
			t.Fatal(err)
		}
		in := strings.Join(strings.Fields(cfg.Instruction), " ")
		for _, want := range []string{"mcp-<server>-<hash>.service", "transient server instances", "never propose to restart it"} {
			if !strings.Contains(in, want) {
				t.Errorf("%s: the instruction lacks %q", name, want)
			}
		}
	}
}
