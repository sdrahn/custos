package relay

import (
	"bytes"
	"context"
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
