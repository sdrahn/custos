package admin

import (
	"os"
	"strings"
	"testing"
)

// An agent created from a template records it, compares the same, and
// shows what changed after an edit and a permissions change; applying
// the template's takes them back but keeps the agent's own keys, its
// mode and its categories.
func TestCompareAndApplyTemplate(t *testing.T) {
	a, _ := withTemplates(t)
	if err := a.CreateFromTemplate("web-doctor", "service-doctor", ""); err != nil {
		t.Fatal(err)
	}
	if got := a.RecordedTemplate("web-doctor"); got != "service-doctor" {
		t.Fatalf("recorded %q", got)
	}
	c := compareOne(t, a, "web-doctor", "")
	if !c.Recorded || !c.Configuration.Same || !c.Permissions.Same {
		t.Fatalf("fresh agent differs: %+v %+v %+v", c, c.Configuration, c.Permissions)
	}

	// The administrator changes the instruction, the model and a limit,
	// changes its schedule, drops a permission and adds one, and sets the mode.
	path := a.p("/etc/custos/agents/web-doctor/agent.yaml")
	b, _ := os.ReadFile(path)
	s := strings.Replace(string(b), "instruction: |\n", "instruction: |\n  Only look at nginx.\n", 1)
	s = strings.Replace(s, "qwen3-32b", "granite-4", 1)
	s = strings.Replace(s, "max_llm_calls: ", "max_llm_calls: 9", 1)
	s = strings.Replace(s, "on_calendar: hourly\n    prompt: List the failed units", "on_calendar: daily\n    prompt: check nginx. List the failed units", 1)
	a.In = strings.NewReader(s)
	if err := a.Edit("web-doctor", "-"); err != nil {
		t.Fatal(err)
	}
	d, _ := a.readGateway("web-doctor")
	want := len(d.Permissions)
	extra := map[string]any{"server": "systemd", "tool": "get_file", "read_only": true}
	d.Permissions = append(d.Permissions[1:], extra)
	d.Mode = "advisory"
	if err := a.writeGateway("web-doctor", d, false); err != nil {
		t.Fatal(err)
	}

	c = compareOne(t, a, "web-doctor", "")
	diff := c.Configuration.Diff
	if c.Configuration.Same || !strings.Contains(diff, "Only look at nginx.") ||
		!strings.Contains(diff, "max_llm_calls: 9") {
		t.Errorf("configuration diff:\n%s", diff)
	}
	// The agent's own keys are not differences.
	for _, ln := range strings.Split(diff, "\n") {
		for _, own := range []string{"granite-4", "daily", "check nginx"} {
			if strings.HasPrefix(ln, "+") || strings.HasPrefix(ln, "-") {
				if strings.Contains(ln, own) {
					t.Errorf("diff shows the agent's own %q as a difference:\n%s", own, diff)
				}
			}
		}
	}
	if c.Permissions.Same || len(c.Permissions.Added) != 1 || len(c.Permissions.Removed) != 1 || permKey(c.Permissions.Removed[0]) != permKey(extra) {
		t.Errorf("permissions %+v", c.Permissions)
	}

	// The permissions only: the configuration stays as edited.
	if err := a.ApplyTemplate("web-doctor", "", false, true); err != nil {
		t.Fatal(err)
	}
	c = compareOne(t, a, "web-doctor", "")
	if !c.Permissions.Same || c.Configuration.Same {
		t.Errorf("after --permissions: %+v %+v", c.Configuration, c.Permissions)
	}
	if d, _ := a.readGateway("web-doctor"); d.Mode != "advisory" || len(d.Permissions) != want {
		t.Errorf("mode %q, %d permissions (want %d): the mode is the agent's", d.Mode, len(d.Permissions), want)
	}

	// Then the configuration: the template's, with the agent's model and
	// schedule.
	if err := a.ApplyTemplate("web-doctor", "", true, false); err != nil {
		t.Fatal(err)
	}
	c = compareOne(t, a, "web-doctor", "")
	if !c.Configuration.Same || !c.Permissions.Same {
		t.Errorf("after --configuration: %+v", c.Configuration)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "Only look at nginx") || !strings.Contains(string(b), "granite-4") || !strings.Contains(string(b), "check nginx") ||
		!strings.Contains(string(b), "name: web-doctor\n") || !strings.Contains(string(b), "# Agent template service-doctor") {
		t.Errorf("agent.yaml after apply:\n%s", b)
	}
}

// An agent made before templates were recorded is compared with the
// template named, which applying records.
func TestCompareUnrecorded(t *testing.T) {
	a, _ := withTemplates(t)
	if err := a.CreateFromTemplate("look", "triage", ""); err != nil {
		t.Fatal(err)
	}
	os.Remove(a.p("/etc/custos/agents/look/template"))
	c := compareOne(t, a, "look", "")
	if c.Template != "" || !strings.Contains(c.Error, "no template recorded") {
		t.Errorf("%+v", c)
	}
	c = compareOne(t, a, "look", "triage")
	if c.Recorded || c.Error != "" || !c.Configuration.Same {
		t.Errorf("%+v", c)
	}
	if err := a.ApplyTemplate("look", "", true, true); err == nil || !strings.Contains(err.Error(), "no template recorded") {
		t.Errorf("err %v", err)
	}
	if err := a.ApplyTemplate("look", "triage", true, true); err != nil {
		t.Fatal(err)
	}
	if a.RecordedTemplate("look") != "triage" {
		t.Error("not recorded")
	}
	if _, err := a.Compare([]string{"look", "web"}, "triage"); err == nil {
		t.Error("--template with two agents")
	}
	// Compared with another template, the differences show.
	if c := compareOne(t, a, "look", "service-doctor"); c.Configuration.Same || c.Permissions.Same {
		t.Errorf("triage against service-doctor: %+v", c)
	}
}

func compareOne(t *testing.T, a *Admin, name, tmpl string) Comparison {
	t.Helper()
	cs, err := a.Compare([]string{name}, tmpl)
	if err != nil || len(cs) != 1 {
		t.Fatalf("compare: %v %v", cs, err)
	}
	return cs[0]
}

// The diff's form is the contract's (§17): "- " the agent's, "+ " the
// template's, "  " both, two lines around each change, "…" between the
// parts; keys sorted and comments left out; "" for the same content.
func TestLineDiffForm(t *testing.T) {
	cur := "# mine\nz: 1\na: 1\nb: 2\nc: 3\nd: 4\ne: 5\nf: 6\ng: 7\nh: 8\ni: 9\n"
	want := "a: 0\nb: 2\nc: 3\nd: 4\ne: 5\nf: 6\ng: 7\nh: 8\ni: 9\nj: 10\nz: 1\n"
	got, err := lineDiff([]byte(cur), []byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if exp := "- a: 1\n+ a: 0\n  b: 2\n  c: 3\n…\n  h: 8\n  i: 9\n+ j: 10\n  z: 1\n"; got != exp {
		t.Errorf("diff:\n%s\nwant:\n%s", got, exp)
	}
	if same, err := lineDiff([]byte("b: 1\na: 2 # x\n"), []byte("a: 2\nb: 1\n")); err != nil || same != "" {
		t.Errorf("the same content: %q %v", same, err)
	}
}
