package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// proxied is an MCP server behind a stand-in for OpenShell's proxy, which
// answers 500 to the next fail requests (all of them while fail < 0).
func proxied(t *testing.T) (url string, fail *atomic.Int32, token *atomic.Value) {
	url, fail, _, token = proxiedDropping(t)
	return url, fail, token
}

// proxiedDropping is proxied, whose proxy also closes the connection
// without an answer to the next drop POSTs, as OpenShell's proxy may
// around a renewal.
func proxiedDropping(t *testing.T) (url string, fail, drop *atomic.Int32, token *atomic.Value) {
	t.Helper()
	old1, old2 := retryFirst, retryLast
	retryFirst, retryLast = 5*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { retryFirst, retryLast = old1, old2 })

	srv := mcp.NewServer(&mcp.Implementation{Name: "gw"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "probe"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "probe ok"}}}, nil, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	fail, drop, token = &atomic.Int32{}, &atomic.Int32{}, &atomic.Value{}
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token.Store(r.Header.Get("Authorization"))
		if r.Method == http.MethodPost {
			if n := drop.Load(); n > 0 && drop.CompareAndSwap(n, n-1) {
				c, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					c.Close()
				}
				return
			}
			if n := fail.Load(); n < 0 || (n > 0 && fail.CompareAndSwap(n, n-1)) {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(p.Close)
	t.Setenv("GW_TOKEN", "placeholder-1")
	return p.URL, fail, drop, token
}

// A dropped connection to a listing is retried: the agent's tool list
// survives the proxy closing a connection around a token renewal.
func TestDroppedListingRetried(t *testing.T) {
	url, _, drop, _ := proxiedDropping(t)
	ctx := context.Background()
	s, err := mcp.NewClient(&mcp.Implementation{Name: "agent"}, nil).Connect(ctx, HTTPTransport(url, "GW_TOKEN", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	drop.Store(2)
	r, err := s.ListTools(ctx, nil)
	if err != nil || len(r.Tools) != 1 {
		t.Fatalf("tools/list through dropped connections: %v %v", r, err)
	}
	if drop.Load() != 0 {
		t.Errorf("not every drop was retried: %d left", drop.Load())
	}
}

// A dropped tools/call is not sent again: it may have reached the
// gateway, and a change must not run twice.
func TestDroppedCallNotRetried(t *testing.T) {
	url, _, drop, _ := proxiedDropping(t)
	ctx := context.Background()
	s, err := mcp.NewClient(&mcp.Implementation{Name: "agent"}, nil).Connect(ctx, HTTPTransport(url, "GW_TOKEN", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	drop.Store(2)
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := s.CallTool(tctx, &mcp.CallToolParams{Name: "probe"}); err == nil {
		t.Fatal("a dropped call succeeded: it was sent again")
	}
	if drop.Load() != 1 {
		t.Errorf("the call was sent %d times, want once", 2-drop.Load())
	}
}

// refusing fails the dial of the next n requests, as the sandbox's
// network fence did around a token renewal (connect: permission denied).
type refusing struct {
	n    atomic.Int32
	next http.RoundTripper
}

func (f *refusing) RoundTrip(r *http.Request) (*http.Response, error) {
	if n := f.n.Load(); n > 0 && f.n.CompareAndSwap(n, n-1) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EACCES)}
	}
	return f.next.RoundTrip(r)
}

// A connection that could not be made sent nothing, so every request is
// tried again, a tools/call too.
func TestRefusedConnectRetried(t *testing.T) {
	url, _, _, _ := proxiedDropping(t)
	ctx := context.Background()
	f := &refusing{next: http.DefaultTransport}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "agent"}, nil).Connect(ctx, HTTPTransport(url, "GW_TOKEN", f), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.n.Store(2)
	if r, err := s.ListTools(ctx, nil); err != nil || len(r.Tools) != 1 {
		t.Fatalf("tools/list through refused connects: %v %v", r, err)
	}
	f.n.Store(2)
	r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "probe"})
	if err != nil || r.Content[0].(*mcp.TextContent).Text != "probe ok" {
		t.Fatalf("tools/call through refused connects: %v %v", r, err)
	}
	if f.n.Load() != 0 {
		t.Errorf("not every refused connect was retried: %d left", f.n.Load())
	}
}

func TestIdempotent(t *testing.T) {
	for body, want := range map[string]bool{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`:                       true,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`:           true,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`:               true,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"x"}}`: false,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`:                     false,
		`{"jsonrpc":"2.0","id":3,"method":"something/new"}`:                    false,
		`not json`: false,
	} {
		r, _ := http.NewRequest(http.MethodPost, "http://gw/mcp", strings.NewReader(body))
		if got := idempotent(r); got != want {
			t.Errorf("%s: %v, want %v", body, got, want)
		}
	}
	get, _ := http.NewRequest(http.MethodGet, "http://gw/mcp", nil)
	del, _ := http.NewRequest(http.MethodDelete, "http://gw/mcp", nil)
	if !idempotent(get) || idempotent(del) {
		t.Error("GET is the session's stream (again is fine); DELETE ends the session")
	}
}

// A short burst of 500s, as OpenShell's proxy sends around a token
// renewal, is retried: the session and its calls go on.
func TestProxy500BurstRetried(t *testing.T) {
	url, fail, token := proxied(t)
	ctx := context.Background()
	s, err := mcp.NewClient(&mcp.Implementation{Name: "agent"}, nil).Connect(ctx, HTTPTransport(url, "GW_TOKEN", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if token.Load() != "Bearer placeholder-1" {
		t.Errorf("token %v", token.Load())
	}
	fail.Store(3)
	r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "probe"})
	if err != nil || r.Content[0].(*mcp.TextContent).Text != "probe ok" {
		t.Fatalf("call through a 500 burst: %v %v", r, err)
	}
	if fail.Load() != 0 {
		t.Errorf("not every 500 was retried: %d left", fail.Load())
	}
}

// 500s that last past the retries, on a session: reported as a lost
// session (what ADK's toolset reconnects on), not a hang.
func TestProxy500OnSessionIsLostSession(t *testing.T) {
	url, fail, _ := proxied(t)
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "agent"}, nil)
	s, err := client.Connect(ctx, HTTPTransport(url, "GW_TOKEN", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(-1)
	// Without the mapping the client waits for an answer that never comes.
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = s.ListTools(tctx, nil)
	if !errors.Is(err, mcp.ErrSessionMissing) {
		t.Fatalf("lasting 500s on the session: %v, want ErrSessionMissing", err)
	}
	fail.Store(0)
	s2, err := client.Connect(ctx, HTTPTransport(url, "GW_TOKEN", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.ListTools(ctx, nil); err != nil {
		t.Fatalf("the new session: %v", err)
	}
}

// Lasting 500s to an initialize (no session) stay a 500 once the retries
// are spent.
func TestInitialize500Stays(t *testing.T) {
	old1, old2 := retryFirst, retryLast
	retryFirst, retryLast = time.Millisecond, 4*time.Millisecond
	defer func() { retryFirst, retryLast = old1, old2 }()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL, nil)
	res, err := (bearer{next: http.DefaultTransport}).RoundTrip(req)
	if err != nil || res.StatusCode != 500 {
		t.Fatalf("%v %v", res, err)
	}
	res.Body.Close()
	if n.Load() != 4 {
		t.Errorf("tried %d times, want 4 (1 ms doubling to 4 ms: 3 retries)", n.Load())
	}
}

// A tools/call goes on a connection of its own (issue #375): OpenShell
// closes a sandbox's idle connections at a token renewal, and a call sent
// on one it closed ends in EOF, which may not be retried.
func TestToolCallOnFreshConnection(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "gw"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "probe"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "probe ok"}}}, nil, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	var mu sync.Mutex
	seen := map[string]bool{} // client addresses, one per connection
	var calls []string        // the address of each tools/call, and whether it was new
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(b))
			mu.Lock()
			if bytes.Contains(b, []byte(`"method":"tools/call"`)) {
				calls = append(calls, fmt.Sprint(!seen[r.RemoteAddr]))
			}
			seen[r.RemoteAddr] = true
			mu.Unlock()
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(p.Close)
	t.Setenv("GW_TOKEN", "placeholder-1")
	ctx := context.Background()
	s, err := mcp.NewClient(&mcp.Implementation{Name: "agent"}, nil).Connect(ctx, HTTPTransport(p.URL, "GW_TOKEN", nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		if _, err := s.ListTools(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "probe"}); err != nil || res.IsError {
			t.Fatalf("call %d: %v %+v", i, err, res)
		}
	}
	// Every idle connection closed by the other end, as OpenShell does:
	// the next call does not go on one of them.
	p.CloseClientConnections()
	if res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "probe"}); err != nil || res.IsError {
		t.Fatalf("the call after the connections were closed: %v %+v", err, res)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(calls, " ") != "true true true" {
		t.Errorf("tools/call on a new connection: %v, want each", calls)
	}
}
