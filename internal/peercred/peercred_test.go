package peercred

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := Guard("pp", ok)
	alice := Caller{UID: 1000, Name: "alice"}
	root := Caller{UID: 0, Name: "root"}
	for _, tc := range []struct {
		c            *Caller
		method, path string
		body         string
		want         int
	}{
		{&alice, "GET", "/health", "", 200},
		{&alice, "GET", "/apps/pp/users/alice/sessions", "", 200},
		{&alice, "GET", "/apps/pp/users/alice/sessions/s1/artifacts/a", "", 200},
		{&alice, "GET", "/apps/pp/users/bob/sessions", "", 403},
		{&root, "GET", "/apps/pp/users/bob/sessions/s1", "", 200},
		{&alice, "GET", "/apps/other/users/alice/sessions", "", 404},
		{&alice, "POST", "/builder/save", "", 404},
		{&root, "GET", "/debug/trace/session/s1", "", 404},
		{&alice, "POST", "/run_sse", `{"appName":"pp","userId":"alice"}`, 200},
		{&alice, "POST", "/run", `{"appName":"pp","userId":"bob"}`, 403},
		{&root, "POST", "/run", `{"appName":"pp","userId":"bob"}`, 200},
		{&alice, "POST", "/run", `{"appName":"x","userId":"alice"}`, 403},
		{nil, "GET", "/health", "", 403},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.c != nil {
			r = r.WithContext(WithCaller(r.Context(), *tc.c))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%v %s %s: %d, want %d", tc.c, tc.method, tc.path, w.Code, tc.want)
		}
	}
}

func TestConnContext(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s")
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, _ := net.Dial("unix", p)
		if c != nil {
			defer c.Close()
		}
		select {}
	}()
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := From(ConnContext(context.Background(), c))
	if !ok || got.UID != os.Getuid() || got.Name == "" {
		t.Errorf("caller %+v %v", got, ok)
	}
}

func TestGuardAgents(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := Guard("pp", ok)
	a := Caller{UID: 990, Name: "agent-triage"}
	for path, want := range map[string]int{
		"/v1/activities": 200, "/v1/activities/i1": 200, "/v1/activities/i1/cancel": 403,
		"/v1/pause": 403, "/agui": 403, "/apps/pp/users/agent-triage/sessions": 403, "/health": 200,
	} {
		r := httptest.NewRequest("POST", path, nil).WithContext(WithCaller(context.Background(), a))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: %d, want %d", path, w.Code, want)
		}
	}
	if n, ok := a.Agent(); !ok || n != "triage" {
		t.Error("Agent()")
	}
}

func TestRelayed(t *testing.T) {
	var got Caller
	h := Relayed("s3cret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = From(r.Context())
		if r.Header.Get(HeaderSecret) != "" {
			t.Error("secret passed on")
		}
	}))
	for _, tc := range []struct {
		secret, caller string
		want           int
	}{
		{"s3cret", "1000:alice", 200},
		{"wrong", "0:root", 403},
		{"", "0:root", 403},
		{"s3cret", "", 403},
		{"s3cret", "x:alice", 403},
		{"s3cret", "5:", 403},
	} {
		r := httptest.NewRequest("GET", "/v1/status", nil)
		if tc.secret != "" {
			r.Header.Set(HeaderSecret, tc.secret)
		}
		if tc.caller != "" {
			r.Header.Set(HeaderCaller, tc.caller)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%+v: %d", tc, w.Code)
		}
	}
	if got.UID != 1000 || got.Name != "alice" {
		t.Errorf("caller %+v", got)
	}
	// No secret configured: nothing gets through.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(HeaderSecret, "")
	r.Header.Set(HeaderCaller, "0:root")
	Relayed("", http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("empty secret: %d", w.Code)
	}
}

// The delegation bridge runs as DelegateUser for every agent: a request
// of it is the named agent's, as if from agent-<name> (CONTRACT.md §9).
// Anyone else's HeaderDelegator is ignored; the bridge without one is
// refused.
func TestGuardDelegator(t *testing.T) {
	var seen Caller
	h := Guard("pp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = From(r.Context())
		if r.Header.Get(HeaderDelegator) != "" {
			t.Error("the delegator header went on")
		}
		w.WriteHeader(200)
	}))
	bridge := Caller{UID: 990, Name: DelegateUser}
	alice := Caller{UID: 1000, Name: "alice"}
	for _, tc := range []struct {
		c         Caller
		path, hdr string
		want      int
		as        string
	}{
		{bridge, "/v1/activities", "health-check", 200, "agent-health-check"},
		{bridge, "/v1/pause", "health-check", 403, ""}, // agents only delegate
		{bridge, "/v1/activities", "", 403, ""},
		{bridge, "/v1/activities", "../root", 403, ""},
		{bridge, "/v1/activities", "Health", 403, ""},
		{alice, "/v1/activities", "health-check", 200, "alice"}, // not the bridge: ignored
	} {
		seen = Caller{}
		r := httptest.NewRequest("POST", tc.path, nil).WithContext(WithCaller(context.Background(), tc.c))
		if tc.hdr != "" {
			r.Header.Set(HeaderDelegator, tc.hdr)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want || (tc.want == 200 && seen.Name != tc.as) {
			t.Errorf("%s %s %q: %d as %q, want %d as %q", tc.c.Name, tc.path, tc.hdr, w.Code, seen.Name, tc.want, tc.as)
		}
	}
}
