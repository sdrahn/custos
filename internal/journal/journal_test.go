package journal

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNativeFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.sock")
	srv, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: p, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: p, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	l := &native{ident: "agent-pp", name: "pp", conn: c, fallback: &JSON{Name: "pp"}}
	l.Log(ToolCall, "two\nlines", Fields{"TOOL": "zypp__install", "EMPTY": ""})
	buf := make([]byte, 4096)
	n, _ := srv.Read(buf)
	got := buf[:n]
	var want bytes.Buffer
	want.WriteString("MESSAGE\n")
	binary.Write(&want, binary.LittleEndian, uint64(9))
	want.WriteString("two\nlines\n")
	if !bytes.HasPrefix(got, want.Bytes()) {
		t.Errorf("multi-line MESSAGE not length-prefixed: %q", got)
	}
	for _, s := range []string{"MESSAGE_ID=" + MessageIDs[ToolCall] + "\n", "SYSLOG_IDENTIFIER=agent-pp\n", "AGENT_EVENT=tool.call\n", "AGENT_TOOL=zypp__install\n"} {
		if !bytes.Contains(got, []byte(s)) {
			t.Errorf("missing %q", s)
		}
	}
	if bytes.Contains(got, []byte("AGENT_EMPTY")) {
		t.Error("empty field written")
	}
	// A field name journald would read otherwise is never written: a
	// newline in it would add a field of the caller's choosing.
	l.Log(ToolCall, "m", Fields{"X=1\nSYSLOG_IDENTIFIER": "custos-promotion", "A\nOBJECT_SYSTEMD_UNIT": "mcp-gateway.service", "TOOL": "t"})
	n, _ = srv.Read(buf)
	got = buf[:n]
	if bytes.Contains(got, []byte("custos-promotion")) || bytes.Contains(got, []byte("OBJECT_SYSTEMD_UNIT")) || !bytes.Contains(got, []byte("AGENT_TOOL=t\n")) {
		t.Errorf("field names not checked: %q", got)
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
	p := filepath.Join(t.TempDir(), "j")
	srv, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: p, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: p, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	l := &native{ident: "custos-promotion", name: "pp", conn: c, fallback: &JSON{Name: "pp"}}
	l.Log(CategoryPromoted, "promoted", Fields{"CATEGORY": "patch-errata"})
	buf := make([]byte, 4096)
	n, _ := srv.Read(buf)
	for _, s := range []string{"SYSLOG_IDENTIFIER=custos-promotion\n", "AGENT_NAME=pp\n", "AGENT_EVENT=category.promoted\n", "MESSAGE_ID=6f3c0e1a9b2d4c58a7e1f2d3c4b5a660\n", "AGENT_CATEGORY=patch-errata\n"} {
		if !bytes.Contains(buf[:n], []byte(s)) {
			t.Errorf("missing %q in %q", s, buf[:n])
		}
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
