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

// The owner is the listening socket's. The accepted connection's own
// entry is listed with uid 0 until the listener accepts it on older
// kernels (SLE 16's), and an earlier connection's TIME_WAIT entry with uid 0
// always: neither may decide, or a local user's listener that waits before
// accept() passes as root's (security review 0.6, P10), and the check
// fails for a slow listener that is not root's (OBS's aarch64 builds). The
// connection's entry must still be there, in a connected state.
func TestPeerUIDIsTheListeners(t *testing.T) {
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
	ra, la := c.RemoteAddr().(*net.TCPAddr), c.LocalAddr().(*net.TCPAddr)
	remote, local := procAddr(ra), procAddr(la)
	port := fmt.Sprintf(":%04X", ra.Port)
	row := func(l, r, st string, uid int) string {
		return fmt.Sprintf("   1: %s %s %s 00000000:00000000 00:00000000 00000000 %5d        0 1 0000000000000000 20 4 30 10 -1", l, r, st, uid)
	}
	conn := func(st string, uid int) string { return row(remote, local, st, uid) }
	listen := func(addr string, uid int) string { return row(addr, "00000000:0000", "0A", uid) }
	header := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode"
	dir := t.TempDir()
	defer func(p []string) { ProcNetTCP = p }(ProcNetTCP)
	for _, tc := range []struct {
		name      string
		tcp, tcp6 []string
		want      int // -1: an error
	}{
		{"not accepted yet: the listener's", []string{conn("01", 0), listen(remote, 1234)}, nil, 1234},
		{"syn-recv", []string{listen(remote, 1234), conn("03", 0)}, nil, 1234},
		{"time-wait first", []string{conn("06", 0), conn("01", 0), listen(remote, 1234)}, nil, 1234},
		{"the address's listener before the wildcard's", []string{listen("00000000"+port, 0), conn("01", 0), listen(remote, 1234)}, nil, 1234},
		{"the wildcard's", []string{conn("01", 0), listen("00000000"+port, 1234)}, nil, 1234},
		{"a dual-stack listener", nil, []string{row(procAddr6(ra), procAddr6(la), "01", 0), listen(strings.Repeat("0", 32)+port, 1234)}, 1234},
		{"root's listener", []string{conn("01", 0), listen(remote, 0)}, nil, 0},
		{"only time-wait", []string{conn("06", 0), listen(remote, 1234)}, nil, -1},
		{"no listener", []string{conn("01", 1234)}, nil, -1},
		{"a listener on another port", []string{conn("01", 0), listen("00000000:0001", 1234)}, nil, -1},
	} {
		var paths []string
		for k, rows := range map[string][]string{"tcp": tc.tcp, "tcp6": tc.tcp6} {
			p := dir + "/" + k
			os.WriteFile(p, []byte(header+"\n"+strings.Join(rows, "\n")+"\n"), 0o644)
			paths = append(paths, p)
		}
		ProcNetTCP = paths
		uid, err := peerUID(c)
		if tc.want < 0 {
			if err == nil {
				t.Errorf("%s: uid %d, want an error", tc.name, uid)
			}
			continue
		}
		if err != nil || uid != tc.want {
			t.Errorf("%s: uid %d, %v; want %d", tc.name, uid, err, tc.want)
		}
	}
}

// Against the kernel's real tables: a listener that has not accepted the
// connection yet is the test's, not root's.
func TestPeerUIDBeforeAccept(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp4", ln.Addr().String()) // never accepted
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if uid, err := peerUID(c); err != nil || uid != os.Getuid() {
		t.Errorf("uid %d, %v; want %d (the listener's)", uid, err, os.Getuid())
	}
}
