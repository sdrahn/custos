package delegate

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func serve(t *testing.T, h http.HandlerFunc) string {
	dir, _ := os.MkdirTemp("", "dlg")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "api.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestRun(t *testing.T) {
	var got map[string]any
	sock := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/activities" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"id":"e-1","state":"finished","summary":"done"}`))
	})
	res, err := Run(context.Background(), sock, map[string]any{"prompt": "x", "wait": true, "trace": map[string]string{"parent_agent": "a"}})
	if err != nil || res["summary"] != "done" {
		t.Fatalf("%v %v", res, err)
	}
	if got["prompt"] != "x" || got["wait"] != true || got["trace"].(map[string]any)["parent_agent"] != "a" {
		t.Errorf("request %v", got)
	}
	if err := Post(context.Background(), sock, "/v1/activities"); err != nil {
		t.Errorf("Post on a 200: %v", err)
	}
}

func TestRunErrors(t *testing.T) {
	sock := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the agent is paused", http.StatusConflict)
	})
	if _, err := Run(context.Background(), sock, map[string]any{"prompt": "x"}); err == nil || err.Error() != "agent answered 409: the agent is paused" {
		t.Errorf("err %v", err)
	}
	if err := Post(context.Background(), sock, "/v1/pause"); err == nil {
		t.Error("409 not an error")
	}
	if _, err := Run(context.Background(), "/nonexistent/api.sock", map[string]any{}); err == nil {
		t.Error("unreachable agent not an error")
	}
}

// What the delegating agent gets: the other activity's full response
// (§4), not its 500-byte summary; the summary from an agent before the
// response was kept; the diagnosis report when there is one; and an
// error, with the reason, when the activity did not finish.
func TestAnswer(t *testing.T) {
	text := func(r *mcp.CallToolResult) map[string]any {
		var m map[string]any
		json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &m)
		return m
	}
	long := strings.Repeat("| /dev/vda2 | 98 % |\n", 200) // 4 KB, far beyond a summary
	r := answer("t1", map[string]any{"id": "e-1", "state": "finished", "summary": long[:500] + "…", "response": long,
		"report": map[string]any{"kind": "diagnosis", "summary": "/var is full."}})
	m := text(r)
	if r.IsError || m["answer"] != long || m["activity"] != "e-1" || m["agent"] != "t1" {
		t.Errorf("full response: %v %v", r.IsError, m)
	}
	if rep, _ := m["report"].(map[string]any); rep["summary"] != "/var is full." {
		t.Errorf("report %v", m["report"])
	}
	if m := text(answer("t1", map[string]any{"id": "e-2", "state": "finished", "summary": "done"})); m["answer"] != "done" || m["report"] != nil {
		t.Errorf("an older agent's summary: %v", m)
	}
	r = answer("t1", map[string]any{"id": "e-3", "state": "limit", "summary": "I stopped here.", "reason": "max_tool_calls (5)"})
	if m := text(r); !r.IsError || m["reason"] != "max_tool_calls (5)" || m["state"] != "limit" {
		t.Errorf("an unfinished activity: %v %v", r.IsError, m)
	}
}

// Delegator names the agent from what mcp-gateway says about the caller
// (MCP_GATEWAY_PRINCIPAL): the kind, and for a token its issuer, before
// the subject.
func TestDelegator(t *testing.T) {
	issuers := []string{"https://idp.example/realms/agents"}
	for _, tc := range []struct {
		principal, want string
	}{
		{`{"kind":"local","sub":"agent-health-check","uid":981}`, "health-check"},
		{`{"kind":"remote","sub":"agent-dwt-osh","iss":"https://idp.example/realms/agents"}`, "dwt-osh"},
		// Not an agent:
		{``, ""},                // a gateway before 1.17.1
		{`{"kind":"none"}`, ""}, // the discovery instance
		{`{"kind":"local","sub":"alice","uid":1000}`, ""},                                              // a person
		{`{"kind":"local","sub":"agent-x"}`, ""},                                                       // local without a uid
		{`{"kind":"remote","sub":"agent-x","iss":"https://other.example"}`, ""},                        // another issuer
		{`{"kind":"remote","sub":"agent-x","iss":"https://idp.example/realms/agents","uid":1000}`, ""}, // mapped to an account
		{`{"kind":"remote","sub":"agent-../x","iss":"https://idp.example/realms/agents"}`, ""},
		{`{"kind":"remote","sub":"agent-x"}`, ""}, // no issuer
		{`not json`, ""},
	} {
		got, err := Delegator(tc.principal, issuers)
		if got != tc.want || (tc.want == "") != (err != nil) {
			t.Errorf("%s: %q, %v; want %q", tc.principal, got, err, tc.want)
		}
	}
}

// The bridge names the delegating agent to the callee.
func TestAskNamesTheDelegator(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "callee"), 0o755)
	var got string
	sock := filepath.Join(dir, "callee", "api.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(HeaderDelegator)
		w.Write([]byte(`{"id":"e-1","state":"finished","summary":"done"}`))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	res := Ask(context.Background(), dir, "health-check", Args{Agent: "callee", Task: "x"})
	if res.IsError || got != "health-check" {
		t.Errorf("delegator %q, result %+v", got, res)
	}
}
