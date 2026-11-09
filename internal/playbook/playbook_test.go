package playbook

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const sample = `disk-pressure:
  title: Disk and inode pressure
  description: Which file systems are full.
  needs: [exec disk_usage]
  steps:
    - {tool: exec__disk_usage, why: Space per file system.}
    - {tool: snapper__list_snapshots, why: Snapshots., optional: true}
  look_for: A file system above 90 %.
`

func TestParse(t *testing.T) {
	doc, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	p := doc["disk-pressure"]
	if p == nil || p.Name != "disk-pressure" || len(p.Steps) != 2 || !p.Steps[1].Optional {
		t.Fatalf("%+v", p)
	}
	for _, bad := range []struct{ in, want string }{
		{strings.Replace(sample, "title: Disk and inode pressure", "titel: x", 1), "titel"},
		{strings.Replace(sample, "exec__disk_usage", "disk_usage", 1), "want server__tool"},
		{strings.Replace(sample, "look_for: A file system above 90 %.", "", 1), "look_for"},
		{strings.Replace(sample, "why: Space per file system.", "why: ''", 1), "why"},
		{strings.Replace(sample, "disk-pressure:", "Disk:", 1), "not a valid playbook name"},
		{strings.Replace(sample, "why: Space per file system.}", "why: Space., optional: true}", 1), "not optional"},
	} {
		if _, err := Parse([]byte(bad.in)); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("want %q: %v", bad.want, err)
		}
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The administrator's file replaces the package's of the same name, an
// empty one hides it, and a broken one is listed with its error.
func TestLoadSiteReplacesAndHides(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, Vendor, "disk-pressure.yaml"), sample)
	write(t, filepath.Join(root, Vendor, "time-sync.yaml"), strings.ReplaceAll(sample, "disk-pressure", "time-sync"))
	write(t, filepath.Join(root, Vendor, "boot-health.yaml"), strings.ReplaceAll(sample, "disk-pressure", "boot-health"))
	write(t, filepath.Join(root, Site, "disk-pressure.yaml"), strings.Replace(sample, "Disk and inode pressure", "Disks (site)", 1))
	write(t, filepath.Join(root, Site, "time-sync.yaml"), "")
	write(t, filepath.Join(root, Site, "broken.yaml"), "broken: {title: x}\n")
	all, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if all["disk-pressure"].Title != "Disks (site)" || all["disk-pressure"].File != filepath.Join(Site, "disk-pressure.yaml") {
		t.Errorf("not replaced: %+v", all["disk-pressure"])
	}
	if _, ok := all["time-sync"]; ok {
		t.Error("an empty file does not hide it")
	}
	if all["boot-health"] == nil || all["boot-health"].Error != "" {
		t.Errorf("the package's: %+v", all["boot-health"])
	}
	if all["broken"] == nil || all["broken"].Error == "" {
		t.Errorf("broken: %+v", all["broken"])
	}
}

func TestMissingAndPrompt(t *testing.T) {
	doc, _ := Parse([]byte(sample))
	p := doc["disk-pressure"]
	if m := p.Missing(map[string]bool{}); len(m) != 1 || m[0] != "exec__disk_usage" {
		t.Errorf("missing %v", m)
	}
	if m := p.Missing(map[string]bool{"exec__disk_usage": true}); len(m) != 0 {
		t.Errorf("an optional step counts as missing: %v", m)
	}
	pr := p.Prompt(map[string]bool{"exec__disk_usage": true}, "", "/var in particular")
	for _, want := range []string{`"disk-pressure"`, "1. exec__disk_usage: Space per file system.", "Look for: A file system above 90 %.",
		"quote it", "Change nothing yourself", "The administrator adds: /var in particular"} {
		if !strings.Contains(pr, want) {
			t.Errorf("prompt lacks %q:\n%s", want, pr)
		}
	}
	if strings.Contains(pr, "snapper__list_snapshots") {
		t.Error("an optional step that is not offered is in the prompt")
	}
	if r := p.Run(map[string]bool{"exec__disk_usage": true}, ""); len(r) != 1 || r[0].Tool != "exec__disk_usage" {
		t.Errorf("run %v", r)
	}
	if r := p.Run(map[string]bool{"exec__disk_usage": true, "snapper__list_snapshots": true}, ""); len(r) != 2 || r[1].Tool != "snapper__list_snapshots" {
		t.Errorf("run with the optional step offered %v", r)
	}
	if pr := p.Prompt(map[string]bool{"exec__disk_usage": true, "snapper__list_snapshots": true}, "", ""); !strings.Contains(pr, "2. snapper__list_snapshots") ||
		strings.Contains(pr, "administrator adds") {
		t.Errorf("with the optional step offered:\n%s", pr)
	}
}

// A step's arguments go to the call, {unit} replaced by the activity's
// unit; a step that needs a unit runs only with one, and a playbook
// needs a step that runs without.
func TestArgsAndUnit(t *testing.T) {
	const svc = `service-health:
  title: Health of a service
  steps:
    - {tool: systemd__list_loaded_units, why: Failed units., args: {state: failed}}
    - {tool: systemd__list_log, why: Its journal., args: {unit: ["{unit}"], count: 50, exact_unit: true}}
  look_for: Restarts.
`
	doc, err := Parse([]byte(svc))
	if err != nil {
		t.Fatal(err)
	}
	p := doc["service-health"]
	offered := map[string]bool{"systemd__list_loaded_units": true, "systemd__list_log": true}
	if p.Steps[0].NeedsUnit() || !p.Steps[1].NeedsUnit() {
		t.Errorf("needs a unit: %v %v", p.Steps[0].NeedsUnit(), p.Steps[1].NeedsUnit())
	}
	if r := p.Run(offered, ""); len(r) != 1 || r[0].Args["state"] != "failed" {
		t.Errorf("without a unit %+v", r)
	}
	r := p.Run(offered, "nginx.service")
	if len(r) != 2 {
		t.Fatalf("with a unit %+v", r)
	}
	if u, _ := r[1].Args["unit"].([]any); len(u) != 1 || u[0] != "nginx.service" || r[1].Args["count"] != 50 {
		t.Errorf("args %#v", r[1].Args)
	}
	if u, _ := p.Steps[1].Args["unit"].([]any); u[0] != "{unit}" {
		t.Errorf("the playbook was changed: %#v", p.Steps[1].Args)
	}
	pr := p.Prompt(offered, "nginx.service", "")
	for _, want := range []string{"The unit in question: nginx.service", `2. systemd__list_log {"count":50,"exact_unit":true,"unit":["nginx.service"]}: Its journal.`,
		"Do not repeat these calls"} {
		if !strings.Contains(pr, want) {
			t.Errorf("prompt lacks %q:\n%s", want, pr)
		}
	}
	if pr := p.Prompt(offered, "", ""); strings.Contains(pr, "systemd__list_log") || strings.Contains(pr, "unit in question") {
		t.Errorf("without a unit:\n%s", pr)
	}
	if a := (Step{}).ArgsFor("x"); a == nil || len(a) != 0 {
		t.Errorf("no args: %#v", a)
	}
	// Only steps that need a unit: refused.
	only := strings.Replace(svc, "args: {state: failed}", `args: {patterns: ["{unit}"]}`, 1)
	if _, err := Parse([]byte(only)); err == nil || !strings.Contains(err.Error(), "does not need a unit") {
		t.Errorf("only steps that need a unit: %v", err)
	}
	for _, u := range []string{"nginx.service", "getty@tty1.service", "srv-data.mount"} {
		if !ValidUnit(u) {
			t.Errorf("%s: not valid", u)
		}
	}
	for _, u := range []string{"", "nginx", "../x.service", "a b.service", ".x.service", "x.service\n"} {
		if ValidUnit(u) {
			t.Errorf("%q: valid", u)
		}
	}
}

// The shipped set: each file parses, is named after its playbook, and
// the set is the one CONTRACT.md §14c lists for GA.
func TestShippedPlaybooks(t *testing.T) {
	files, _ := filepath.Glob("../../packaging/playbooks/*.yaml")
	var names []string
	for _, f := range files {
		b, _ := os.ReadFile(f)
		doc, err := Parse(b)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		base := strings.TrimSuffix(filepath.Base(f), ".yaml")
		if len(doc) != 1 || doc[base] == nil {
			t.Errorf("%s: want one playbook named %s", f, base)
		}
		names = append(names, base)
	}
	sort.Strings(names)
	b, _ := os.ReadFile("../../CONTRACT.md")
	m := regexp.MustCompile(`(?s)The\s+set\s+GA\s+ships[^:]*:(.*?)\.\s+[A-Z]`).FindSubmatch(b)
	if m == nil {
		t.Fatal("CONTRACT.md §14c: no GA set")
	}
	var want []string
	for _, x := range regexp.MustCompile("`([a-z-]+)`").FindAllSubmatch(m[1], -1) {
		want = append(want, string(x[1]))
	}
	sort.Strings(want)
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("shipped %v, the contract lists %v", names, want)
	}
}

// The arguments of the shipped playbooks' systemd steps are those
// systemd-mcp (0.3.5) takes, with values it accepts: a call it refuses
// shows the model an error instead of the host's state.
func TestShippedSystemdArgs(t *testing.T) {
	takes := map[string]map[string]string{
		"systemd__list_loaded_units": {"state": "string", "patterns": "list", "properties": "bool", "include_description": "bool", "verbose": "bool"},
		"systemd__get_file":          {"path": "string", "show_content": "bool", "offset": "int", "limit": "int"},
		"systemd__list_log":          {"count": "int", "offset": "int", "from": "string", "to": "string", "pattern": "string", "unit": "list", "exact_unit": "bool", "allboots": "bool"},
	}
	states := map[string]bool{"active": true, "inactive": true, "loaded": true, "not-found": true, "all": true, "failed": true}
	pbs, _ := filepath.Glob("../../packaging/playbooks/*.yaml")
	for _, f := range pbs {
		b, _ := os.ReadFile(f)
		doc, _ := Parse(b)
		for _, p := range doc {
			for i, s := range p.Steps {
				if strings.HasPrefix(s.Tool, "exec__") || strings.HasPrefix(s.Tool, "custos-net__") {
					if len(s.Args) > 0 {
						t.Errorf("%s: step %d: %s takes no arguments", f, i+1, s.Tool)
					}
					continue
				}
				want, ok := takes[s.Tool]
				if !ok {
					continue
				}
				for k, v := range s.Args {
					var got string
					switch v := v.(type) {
					case string:
						got = "string"
						if _, err := regexp.Compile(v); k == "pattern" && err != nil {
							t.Errorf("%s: step %d: pattern: %v", f, i+1, err)
						}
					case int:
						got = "int"
					case bool:
						got = "bool"
					case []any:
						got = "list"
					}
					if want[k] != got {
						t.Errorf("%s: step %d: %s takes no %s %s", f, i+1, s.Tool, got, k)
					}
				}
				if st, ok := s.Args["state"].(string); ok && !states[st] {
					t.Errorf("%s: step %d: state %q", f, i+1, st)
				}
			}
		}
	}
}

// Every exec__ step of a shipped playbook is a command of a shipped
// exec.d file, which takes no arguments and only reads.
func TestShippedExecCommands(t *testing.T) {
	commands := map[string]map[string]any{}
	files, _ := filepath.Glob("../../templates/exec.d-*.yaml")
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var doc struct {
			Version  int                       `yaml:"version"`
			Commands map[string]map[string]any `yaml:"commands"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil || doc.Version != 1 {
			t.Fatalf("%s: %v", f, err)
		}
		for n, c := range doc.Commands {
			commands[n] = c
			if c["read_only"] != true || c["args"] != nil {
				t.Errorf("%s: %s must be read_only and take no arguments", f, n)
			}
			argv, _ := c["argv"].([]any)
			if len(argv) == 0 || !strings.HasPrefix(argv[0].(string), "/") {
				t.Errorf("%s: %s: argv[0] must be an absolute path", f, n)
			}
			if strings.HasPrefix(n, "list_") || strings.HasPrefix(n, "read_") || strings.HasPrefix(n, "get_") {
				t.Errorf("%s: %s: the shipped viewer role allows list_*, read_*, get_* on every server", f, n)
			}
		}
	}
	pbs, _ := filepath.Glob("../../packaging/playbooks/*.yaml")
	for _, f := range pbs {
		b, _ := os.ReadFile(f)
		doc, _ := Parse(b)
		for _, p := range doc {
			for _, s := range p.Steps {
				if c, ok := strings.CutPrefix(s.Tool, "exec__"); ok && commands[c] == nil {
					t.Errorf("%s: %s is no shipped exec command", f, s.Tool)
				}
			}
		}
	}
}

// {unit_re} is the unit escaped for a regular expression (security
// review 0.6, N5): its dots match only dots. It needs a unit like {unit}.
func TestUnitRE(t *testing.T) {
	s := Step{Tool: "systemd__list_log", Why: "w", Args: map[string]any{"pattern": "{unit_re}|Failed to start {unit}"}}
	if !s.NeedsUnit() {
		t.Error("{unit_re} needs a unit")
	}
	got := s.ArgsFor("nginx@a.b.service")["pattern"]
	if got != `nginx@a\.b\.service|Failed to start nginx@a.b.service` {
		t.Errorf("pattern %q", got)
	}
	re := regexp.MustCompile(got.(string))
	if re.MatchString("nginx@aXbXservice") || !re.MatchString("x nginx@a.b.service y") {
		t.Error("the escaped unit matches what it should not, or not itself")
	}
}

// tools: further tools a run offers, server__tool; Offer is the steps'
// and those, of the tools the gateway offers, without repeats.
func TestTools(t *testing.T) {
	p := &Playbook{Title: "t", LookFor: "x", Steps: []Step{{Tool: "a__one", Why: "w"}, {Tool: "b__two", Why: "w", Optional: true}},
		Tools: []string{"c__three", "a__one", "d__four"}}
	if err := p.check(); err != nil {
		t.Fatal(err)
	}
	got := p.Offer(map[string]bool{"a__one": true, "c__three": true, "d__four": true, "e__five": true})
	if strings.Join(got, " ") != "a__one c__three d__four" {
		t.Errorf("offer %v", got)
	}
	p.Tools = []string{"no-server"}
	if err := p.check(); err == nil || !strings.Contains(err.Error(), "tools[0]") {
		t.Errorf("a tool without a server: %v", err)
	}
}
