package launcher

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/governance"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/peercred"
	"github.com/sdrahn/custos/internal/testutil"
)

// TestApprovalOverHTTP is the OpenShell backend's gateway connection
// (CONTRACT.md §3a): Streamable HTTP with a bearer token read from the
// environment variable OpenShell fills with its placeholder. The
// approval notice arrives on the call's SSE stream and is handled as on
// the socket.
func TestApprovalOverHTTP(t *testing.T) {
	gw, url := testutil.StartHTTPGateway(t, []string{"zypp__install_patches"})
	gw.Ask["zypp__install_patches"] = "a-77"
	t.Setenv("AGENT_GATEWAY_TOKEN", "openshell:resolve:env:AGENT_GATEWAY_TOKEN")
	cfg, err := config.Parse([]byte(`
version: 1
name: patch-pilot
model: {provider: openai, name: test, endpoint: "http://127.0.0.1:1"}
gateway: {url: "` + url + `", token_env: AGENT_GATEWAY_TOKEN, servers: [zypp]}
sandbox: {kind: openshell}
`))
	if err != nil {
		t.Fatal(err)
	}
	log := &journal.Memory{}
	m := &testutil.Model{Steps: []testutil.Step{
		testutil.SayAndCall("One security patch.", "c1", "zypp__install_patches", map[string]any{"ids": []any{"x"}}),
		testutil.Text("Installed."),
	}}
	l, err := New(context.Background(), Options{Config: cfg, Log: log, Model: m})
	if err != nil {
		t.Fatal(err)
	}
	asRoot := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(peercred.WithCaller(r.Context(), peercred.Caller{UID: 0, Name: "root"}))
		w := httptest.NewRecorder()
		l.Handler.ServeHTTP(w, r)
		return w
	}
	w := asRoot("POST", "/v1/activities", `{"prompt":"go"}`)
	var started struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &started)
	if w.Code != 202 || started.ID == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		a, _ := l.Governor.Activity(started.ID)
		if a.State == governance.StateWaiting {
			if c := a.Calls[0]; c.ApprovalID != "a-77" || c.Rationale != "One security patch." {
				t.Errorf("call %+v", c)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no approval notice over HTTP: %+v", a)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(gw.Ran()) != 0 {
		t.Fatal("ran before the decision")
	}
	gw.Decide("a-77", true)
	l.Wait()
	if a, _ := l.Governor.Activity(started.ID); a.State != governance.StateFinished || a.Summary != "Installed." {
		t.Errorf("activity %+v", a)
	}
	if len(gw.Ran()) != 1 {
		t.Errorf("ran %v", gw.Ran())
	}
	for _, tok := range gw.Tokens() {
		if tok != "openshell:resolve:env:AGENT_GATEWAY_TOKEN" {
			t.Errorf("request without the token placeholder: %q", tok)
		}
	}
	if r := log.Events(journal.ApprovalResolved); len(r) != 1 || r[0].Fields["OUTCOME"] != "approved" {
		t.Errorf("resolved %+v", r)
	}
	b := asRoot("GET", "/v1/status", "").Body.Bytes()
	if !testutil.Has(string(b), `"lifecycle":{"driver":"container","gateway":"openshell","kind":"openshell"`, `"url":"`+url+`"`) || strings.Contains(string(b), `"units"`) {
		t.Errorf("status %s", b)
	}
}
