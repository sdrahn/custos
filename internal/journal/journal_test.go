package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	sdjournal "github.com/coreos/go-systemd/v22/journal"
)

// sent is what a native logger handed to journald.
type sent struct {
	message  string
	priority sdjournal.Priority
	vars     map[string]string
}

func capture(got *[]sent, err error) func(string, sdjournal.Priority, map[string]string) error {
	return func(m string, p sdjournal.Priority, v map[string]string) error {
		*got = append(*got, sent{m, p, v})
		return err
	}
}

func TestNativeRecord(t *testing.T) {
	var got []sent
	l := &native{ident: "agent-pp", name: "pp", send: capture(&got, nil), fallback: &JSON{Name: "pp"}}
	l.Log(ToolCall, "two\nlines", Fields{"TOOL": "zypp__install", "EMPTY": ""})
	r := got[0]
	if r.message != "two\nlines" || r.priority != sdjournal.PriInfo {
		t.Errorf("message %q, priority %d", r.message, r.priority)
	}
	for k, want := range map[string]string{"MESSAGE_ID": MessageIDs[ToolCall], "SYSLOG_IDENTIFIER": "agent-pp", "AGENT_CONTRACT": "1",
		"AGENT_NAME": "pp", "AGENT_EVENT": "tool.call", "AGENT_TOOL": "zypp__install"} {
		if r.vars[k] != want {
			t.Errorf("%s = %q, want %q", k, r.vars[k], want)
		}
	}
	if _, ok := r.vars["AGENT_EMPTY"]; ok {
		t.Error("empty field written")
	}
	// A field name journald would read otherwise is never written: a
	// newline in it would add a field of the caller's choosing.
	l.Log(LimitExceeded, "m", Fields{"X=1\nSYSLOG_IDENTIFIER": "custos-promotion", "A\nOBJECT_SYSTEMD_UNIT": "mcp-gateway.service", "TOOL": "t"})
	r = got[1]
	if len(r.vars) != 6 || r.vars["SYSLOG_IDENTIFIER"] != "agent-pp" || r.vars["AGENT_TOOL"] != "t" || r.priority != sdjournal.PriNotice {
		t.Errorf("field names not checked: %v", r.vars)
	}
	for k, want := range map[string]bool{"TOOL": true, "APPROVAL_ID": true, "A1": true, "": false, "_X": false, "x": false, "A B": false, "A\nB": false, "A=B": false} {
		if ValidField(k) != want {
			t.Errorf("ValidField(%q) = %v", k, !want)
		}
	}
}

func TestJSON(t *testing.T) {
	var b bytes.Buffer
	(&JSON{Name: "pp", W: &b}).Log(ApprovalPending, "waiting", Fields{"APPROVAL_ID": "a-1"})
	var m map[string]string
	if err := json.Unmarshal(b.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["approval_id"] != "a-1" || m["event"] != "approval.pending" || m["name"] != "pp" {
		t.Errorf("%v", m)
	}
}

func TestEveryEventHasID(t *testing.T) {
	seen := map[string]bool{}
	for e, id := range MessageIDs {
		if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" || seen[id] {
			t.Errorf("%s: bad or duplicate id %q", e, id)
		}
		seen[id] = true
	}
}

func TestNewAsIdentifier(t *testing.T) {
	var got []sent
	l := &native{ident: "custos-promotion", name: "pp", send: capture(&got, nil), fallback: &JSON{Name: "pp"}}
	l.Log(CategoryPromoted, "promoted", Fields{"CATEGORY": "patch-errata"})
	for k, want := range map[string]string{"SYSLOG_IDENTIFIER": "custos-promotion", "AGENT_NAME": "pp", "AGENT_EVENT": "category.promoted",
		"MESSAGE_ID": "6f3c0e1a9b2d4c58a7e1f2d3c4b5a660", "AGENT_CATEGORY": "patch-errata"} {
		if got[0].vars[k] != want {
			t.Errorf("%s = %q, want %q", k, got[0].vars[k], want)
		}
	}
}

// A record journald does not take is kept as a JSON line.
func TestNativeFallsBack(t *testing.T) {
	var got []sent
	var b bytes.Buffer
	l := &native{ident: "agent-pp", name: "pp", send: capture(&got, errors.New("no journal")), fallback: &JSON{Name: "pp", W: &b}}
	l.Log(ToolCall, "m", Fields{"TOOL": "t"})
	if !strings.Contains(b.String(), `"tool":"t"`) {
		t.Errorf("fallback wrote %q", b.String())
	}
}

// The MESSAGE_IDs are the contract's (§10), both ways.
func TestMessageIDsAreTheContracts(t *testing.T) {
	b, err := os.ReadFile("../../CONTRACT.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile("(?m)^\\| `([a-z]+\\.[a-z.]+)` \\| `([0-9a-f]{32})`")
	inContract := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		inContract[m[1]] = m[2]
	}
	for e, id := range MessageIDs {
		if inContract[e] != id {
			t.Errorf("%s: %s here, %q in CONTRACT.md", e, id, inContract[e])
		}
	}
	for e := range inContract {
		if _, ok := MessageIDs[e]; !ok {
			t.Errorf("%s: in CONTRACT.md, not here", e)
		}
	}
}
