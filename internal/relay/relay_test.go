package relay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/peercred"
)

// The relay and the agent's Relayed check together: the caller the host
// saw arrives, a caller's own headers do not, streams flow.
func TestRelayToAgent(t *testing.T) {
	ForwardUID = os.Getuid() // the test's listener stands in for the forward
	var seen peercred.Caller
	agent := httptest.NewServer(peercred.Relayed("s", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = peercred.From(r.Context())
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"RUN_STARTED\"}\n\n")
	})))
	defer agent.Close()
	u, _ := url.Parse(agent.URL)
	h := Handler(u, "s")

	r := httptest.NewRequest("POST", "/agui", strings.NewReader("{}"))
	r.Header.Set(peercred.HeaderCaller, "0:root") // forged by the caller
	r.Header.Set(peercred.HeaderSecret, "guess")
	r = r.WithContext(peercred.WithCaller(context.Background(), peercred.Caller{UID: 1000, Name: "alice"}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "RUN_STARTED") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if seen.UID != 1000 || seen.Name != "alice" {
		t.Errorf("agent saw %+v, want alice", seen)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/status", nil))
	if w.Code != 403 {
		t.Errorf("no caller: %d", w.Code)
	}
	// A wrong secret on the relay's side is refused by the agent.
	w = httptest.NewRecorder()
	Handler(u, "other").ServeHTTP(w, httptest.NewRequest("GET", "/v1/status", nil).WithContext(peercred.WithCaller(context.Background(), peercred.Caller{Name: "root"})))
	if w.Code != 403 {
		t.Errorf("wrong secret: %d", w.Code)
	}
}

func TestBridge(t *testing.T) {
	var jl bytes.Buffer
	(&journal.JSON{Name: "pp", W: &jl}).Log(journal.ApprovalPending, "Waiting for approval a-1", journal.Fields{"APPROVAL_ID": "a-1", "SESSION": "s1"})
	stream := "2026-11-03T06:00:00Z agent-pp | " + jl.String() +
		"openshell: supervisor connected\n" +
		`{"contract":"1","name":"other","event":"tool.call","message":"x"}` + "\n" +
		`{"contract":"1","name":"pp","event":"made.up","message":"x"}` + "\n" +
		"{not json\n"
	m := &journal.Memory{}
	n, err := Bridge(strings.NewReader(stream), "pp", m)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	r := m.Records[0]
	if r.Event != journal.ApprovalPending || r.Message != "Waiting for approval a-1" || r.Fields["APPROVAL_ID"] != "a-1" || r.Fields["SESSION"] != "s1" {
		t.Errorf("record %+v", r)
	}
}

// A listener another user owns gets nothing: the relay checks the
// socket's owner before it sends the request with its secret (security
// review H3). Over IPv4 and IPv6.
func TestRelayOnlyToTheForward(t *testing.T) {
	for _, network := range []string{"tcp4", "tcp6"} {
		var got atomic.Int32
		ln, err := net.Listen(network, map[string]string{"tcp4": "127.0.0.1:0", "tcp6": "[::1]:0"}[network])
		if err != nil {
			t.Logf("%s: %v", network, err)
			continue
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				b := make([]byte, 1)
				if n, _ := c.Read(b); n > 0 {
					got.Add(1)
				}
				c.Close()
			}
		}()
		u, _ := url.Parse("http://" + ln.Addr().String())
		call := func() int {
			w := httptest.NewRecorder()
			Handler(u, "s3cret").ServeHTTP(w, httptest.NewRequest("GET", "/v1/status", nil).
				WithContext(peercred.WithCaller(context.Background(), peercred.Caller{Name: "root"})))
			return w.Code
		}
		ForwardUID = os.Getuid() + 1 // someone else's listener
		if code := call(); code != http.StatusBadGateway {
			t.Errorf("%s: another user's listener: %d", network, code)
		}
		time.Sleep(50 * time.Millisecond)
		if n := got.Load(); n != 0 {
			t.Errorf("%s: another user's listener received %d request(s)", network, n)
		}
		ForwardUID = os.Getuid()
		call()
		time.Sleep(50 * time.Millisecond)
		if n := got.Load(); n != 1 {
			t.Errorf("%s: the forward's listener received %d request(s)", network, n)
		}
		ln.Close()
	}
}

// The relay writes as root, and root's records of agent-admin's events
// are trusted (readiness.ByRoot): from the sandbox it passes only what an
// agent host writes, with field names that cannot become other journal
// fields (security review H1).
func TestBridgeRefusesRootsEvents(t *testing.T) {
	var stream strings.Builder
	for _, e := range []string{journal.CategoryReview, journal.DiagnosisFeedback, journal.PermissionsChanged, journal.AgentsStopped,
		journal.AgentsReleased, journal.CategoryPromoted, journal.CategoryDemoted, journal.CategoriesEnabled, journal.CategoriesDisabled,
		journal.CategoryRefused, journal.EventSkipped} {
		fmt.Fprintf(&stream, `{"contract":"1","name":"pp","event":%q,"message":"forged","verdict":"ok","by":"alice"}`+"\n", e)
	}
	// A host event with keys that would add fields: a newline, "=",
	// upper case, a leading "_".
	stream.WriteString(`{"contract":"1","name":"pp","event":"tool.call","message":"call","tool":"zypp__x",` +
		`"x=1\nSYSLOG_IDENTIFIER":"custos-promotion","a\nOBJECT_SYSTEMD_UNIT":"mcp-gateway.service","Mixed":"y","_hidden":"z","agent name":"w"}` + "\n")
	m := &journal.Memory{}
	n, err := Bridge(strings.NewReader(stream.String()), "pp", m)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v records %+v", n, err, m.Records)
	}
	r := m.Records[0]
	if r.Event != journal.ToolCall || len(r.Fields) != 1 || r.Fields["TOOL"] != "zypp__x" {
		t.Errorf("record %+v", r)
	}
	// Every event an agent host writes passes.
	for e := range journal.HostEvents {
		m := &journal.Memory{}
		if n, _ := Bridge(strings.NewReader(fmt.Sprintf(`{"contract":"1","name":"pp","event":%q,"message":"m"}`+"\n", e)), "pp", m); n != 1 {
			t.Errorf("host event %s not passed", e)
		}
	}
}

// An earlier connection's TIME_WAIT entry for the same addresses, which
// the kernel lists with uid 0, is not the socket the relay reached: only
// a connected entry counts. Otherwise the check failed on OBS's aarch64
// builds, and a local user's listener could pass as root's by closing a
// connection first.
func TestPeerUIDSkipsTimeWait(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	remote := procAddr(c.RemoteAddr().(*net.TCPAddr))
	local := procAddr(c.LocalAddr().(*net.TCPAddr))
	row := func(st string, uid int) string {
		return fmt.Sprintf("   1: %s %s %s 00000000:00000000 00:00000000 00000000 %5d        0 1 0000000000000000 20 4 30 10 -1", remote, local, st, uid)
	}
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode"
	dir := t.TempDir()
	defer func(p []string) { ProcNetTCP = p }(ProcNetTCP)
	for name, rows := range map[string][]string{
		"time-wait first": {row("06", 0), row("01", 1234)},
		"syn-recv":        {row("06", 0), row("03", 1234)},
		"only time-wait":  {row("06", 0)},
	} {
		p := dir + "/" + strings.ReplaceAll(name, " ", "-")
		os.WriteFile(p, []byte(header+"\n"+strings.Join(rows, "\n")+"\n"), 0o644)
		ProcNetTCP = []string{p}
		uid, err := peerUID(c)
		if name == "only time-wait" {
			if err == nil {
				t.Errorf("%s: uid %d, want no socket", name, uid)
			}
			continue
		}
		if err != nil || uid != 1234 {
			t.Errorf("%s: uid %d, %v; want 1234", name, uid, err)
		}
	}
}
