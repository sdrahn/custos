package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/admin/admintest"
	"github.com/sdrahn/custos/internal/journal"
)

// Options and arguments in any order, "-" as an argument and as an
// option's value, an option given more than once, and what is refused.
func TestParse(t *testing.T) {
	type got struct {
		pos    []string
		from   string
		force  bool
		accept []string
	}
	for _, c := range []struct {
		args string
		need int
		want got
		err  string
	}{
		{args: "a b", need: 2, want: got{pos: []string{"a", "b"}}},
		{args: "a --from f", need: 1, want: got{pos: []string{"a"}, from: "f"}},
		{args: "--from f a", need: 1, want: got{pos: []string{"a"}, from: "f"}},
		{args: "a --from - --force b", need: 2, want: got{pos: []string{"a", "b"}, from: "-", force: true}},
		{args: "-", need: 1, want: got{pos: []string{"-"}}},
		{args: "--from=f -force", want: got{from: "f", force: true}},
		{args: "--accept x a --accept y", want: got{pos: []string{"a"}, accept: []string{"x", "y"}}},
		{args: "", want: got{}},
		// "--" ends the options (security review 0.7, R3).
		{args: "a -- --force b", need: 2, want: got{pos: []string{"a", "--force", "b"}}},
		{args: "--force -- --from", need: 1, want: got{pos: []string{"--from"}, force: true}},
		{args: "a --", need: 1, want: got{pos: []string{"a"}}},
		{args: "-- -", need: 1, want: got{pos: []string{"-"}}},
		{args: "a --from -- --force", need: 1, want: got{pos: []string{"a"}, from: "--", force: true}},
		{args: "a", need: 2, err: "cmd: missing arguments\nusage: agent-admin"},
		{args: "--force", need: 1, err: "cmd: missing arguments\n"},
		{args: "a --no-such", need: 1, err: "cmd: flag provided but not defined: -no-such\nusage: agent-admin"},
		{args: "a --from", need: 1, err: "cmd: flag needs an argument: -from\n"},
		{args: "a --help", need: 1, err: "usage: agent-admin"},
	} {
		fs := flags("cmd")
		var g got
		fs.StringVar(&g.from, "from", "", "")
		fs.BoolVar(&g.force, "force", false, "")
		fs.Var((*list)(&g.accept), "accept", "")
		pos, err := parse(fs, strings.Fields(c.args), c.need)
		g.pos = pos
		if c.err != "" {
			if err == nil || !strings.HasPrefix(err.Error(), c.err) {
				t.Errorf("%q: %v, want %q", c.args, err, c.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(g, c.want) {
			t.Errorf("%q: %+v %v, want %+v", c.args, g, err, c.want)
		}
	}
}

// The help and the table name the same commands, and no two groups the
// same one.
func TestHelpNamesEveryCommand(t *testing.T) {
	inHelp := map[string]bool{}
	for _, g := range help {
		for _, e := range g.entries {
			inHelp[helpName(e)] = true
		}
	}
	inTable := map[string]bool{}
	for _, g := range groups {
		for _, c := range g {
			if inTable[c.name] {
				t.Errorf("%s: in the table twice", c.name)
			}
			inTable[c.name] = true
			if c.run == nil {
				t.Errorf("%s: nothing to run", c.name)
			}
		}
	}
	if !reflect.DeepEqual(inHelp, inTable) {
		t.Errorf("the help names %v\nthe table has %v", keys(inHelp), keys(inTable))
	}
	if len(commands) != len(inTable) {
		t.Errorf("%d commands by name, %d in the groups", len(commands), len(inTable))
	}
}

// The help reads on a terminal: no line wider than 78 columns, a blank
// line between two commands, each command's text under its synopsis.
func TestHelpLayout(t *testing.T) {
	lines := strings.Split(usage, "\n")
	for i, l := range lines {
		if n := len([]rune(l)); n > helpWidth {
			t.Errorf("line %d is %d wide: %q", i+1, n, l)
		}
	}
	for _, g := range help {
		if !strings.Contains(usage, "\n\n"+g.title+"\n  ") {
			t.Errorf("group %q: not a heading of its own", g.title)
		}
		for i, e := range g.entries {
			first := strings.SplitN(synopsis(e, "  "), "\n", 2)[0]
			if i > 0 && !strings.Contains(usage, "\n\n"+first+"\n") {
				t.Errorf("%q: no blank line before it", e.synopsis)
			}
		}
	}
}

// Within a group, the commands are in alphabetical order.
func TestHelpGroupsSorted(t *testing.T) {
	for _, g := range help {
		for i := 1; i < len(g.entries); i++ {
			if a, b := helpName(g.entries[i-1]), helpName(g.entries[i]); a > b {
				t.Errorf("%s: %s before %s", g.title, a, b)
			}
		}
	}
	if !strings.Contains(usage, "\nAgents\n  chat NAME") {
		t.Error("Agents does not start with chat")
	}
}

func TestHelpCommand(t *testing.T) {
	a, _ := admintest.New(t)
	out := &bytes.Buffer{}
	a.Out = out
	if err := run(a, []string{"help"}); err != nil || !strings.Contains(out.String(), "\nEmergency stop\n") {
		t.Errorf("help: %v\n%s", err, out)
	}
	out.Reset()
	if err := run(a, []string{"help", "idp-client"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Count(got, "  idp-client NAME") != 2 || strings.Contains(got, "check-token") {
		t.Errorf("help idp-client: both forms, nothing else:\n%s", got)
	}
	out.Reset()
	if err := run(a, []string{"promote", "x", "--help"}); err != nil || !strings.HasPrefix(out.String(), "  promote NAME CATEGORY") {
		t.Errorf("promote --help: %v\n%s", err, out)
	}
	if err := run(a, []string{"help", "no-such"}); err == nil || !strings.HasPrefix(err.Error(), `unknown command "no-such"`) {
		t.Errorf("help no-such: %v", err)
	}
	// A command line not understood shows the command's synopsis only.
	err := run(a, []string{"mode", "one"})
	if err == nil || err.Error() != "mode: missing arguments\nusage: agent-admin mode NAME advisory|supervised|bounded\n(agent-admin help mode: what it does)" {
		t.Errorf("mode one: %v", err)
	}
}

func keys(m map[string]bool) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// Commands reach the packages they are built on, with their arguments in
// either order.
func TestRun(t *testing.T) {
	a, mem, r := admintest.WithAgent(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	yml := func(name string) {
		os.WriteFile(f, []byte(strings.Replace(admintest.Yml, "name: patch-pilot", "name: "+name, 1)), 0o644)
	}
	mode := func(name string) string {
		t.Helper()
		g, err := a.ReadGateway(name)
		if err != nil {
			t.Fatal(err)
		}
		return g.Mode
	}
	yml("one")
	if err := run(a, []string{"create", "one", "--from", f}); err != nil {
		t.Fatal(err)
	}
	yml("two")
	if err := run(a, []string{"create", "--from", f, "two"}); err != nil {
		t.Fatal(err)
	}
	names, _ := a.List()
	if !reflect.DeepEqual(names, []string{"one", "patch-pilot", "two"}) {
		t.Errorf("agents: %v", names)
	}
	if err := run(a, []string{"mode", "one", "advisory"}); err != nil || mode("one") != "advisory" {
		t.Errorf("mode: %v %s", err, mode("one"))
	}
	if err := run(a, []string{"check", f}); err != nil {
		t.Errorf("check: %v", err)
	}
	// The emergency stop is recorded with who asked.
	t.Setenv("SUDO_USER", "alice")
	mem.Records = nil
	if err := run(a, []string{"stop-all"}); err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(r.Cmds, "\n"); !strings.Contains(s, "systemctl stop") {
		t.Errorf("stop-all: %s", s)
	}
	if len(mem.Records) == 0 || !strings.Contains(strings.Join(flat(mem.Records), " "), "alice") {
		t.Errorf("stop-all: records %v", mem.Records)
	}
	if err := run(a, []string{"release-all"}); err != nil {
		t.Fatal(err)
	}
	if err := run(a, []string{"delete", "--keep-state", "two"}); err != nil {
		t.Fatal(err)
	}
	if names, _ := a.List(); !reflect.DeepEqual(names, []string{"one", "patch-pilot"}) {
		t.Errorf("after delete: %v", names)
	}

	for args, want := range map[string]string{
		"":                                    "usage: agent-admin",
		"no-such":                             `unknown command "no-such"` + "\nusage:",
		"mode one":                            "mode: missing arguments\nusage:",
		"create three":                        "create: --from FILE is needed",
		"edit one":                            "edit: --from FILE is needed",
		"edit one --template triage":          "edit: flag provided but not defined: -template\n",
		"seal":                                "seal: --agents or --rekey\n",
		"seal --agents --rekey":               "seal: --agents or --rekey\n",
		"seal --agents x":                     `seal: "x": want --accept NAME` + "\n",
		"issuers x":                           `issuers: "x": want --add ISSUER or --remove ISSUER` + "\n",
		"promote one patch-errata":            "promote: --envelope FILE is needed",
		"feedback one act --hypothesis h1":    "--hypothesis h1: want ID=confirmed|rejected|unsure",
		"feedback one act more":               `feedback: unknown argument "more"`,
		"stop-all --now":                      "stop-all: flag provided but not defined: -now\n",
		"mode one no-such-mode":               "",
		"apply-template one --configuration":  "",
		"review one a b c ok --reason r":      "",
		"create four --template no-such":      "",
		"remove-credential one c --force":     "",
		"demote one patch-errata --reason r":  "",
		"draft five --template no-such":       "",
		"defaults --from /no/such/file":       "",
		"enable no-such-category":             "",
		"idp-client one --gateway-config /no": "",
	} {
		err := run(a, strings.Fields(args))
		if err == nil {
			t.Errorf("%q: no error", args)
			continue
		}
		// The ones without a text here are the packages' own refusals:
		// the command line was understood.
		if want == "" {
			if strings.Contains(err.Error(), "usage:") {
				t.Errorf("%q: %v", args, err)
			}
			continue
		}
		if !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q: %v, want %q", args, err, want)
		}
	}
}

func flat(recs []journal.Record) []string {
	var out []string
	for _, r := range recs {
		out = append(out, fmt.Sprint(r.Event, " ", r.Message, " ", r.Fields))
	}
	return out
}
