package chat

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/admintest"
)

// agent serves AG-UI on agent sb's socket in a's test host: each run
// answers with events from answer, given the message's text.
type agent struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func serve(t *testing.T, a *admin.Admin, answer func(text string) (int, []string)) *agent {
	t.Helper()
	sock := a.Path(filepath.Join(admin.SocketDir, "sb", "api.sock"))
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agui" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		ag.mu.Lock()
		ag.bodies = append(ag.bodies, body)
		ag.mu.Unlock()
		msgs, _ := body["messages"].([]any)
		text := ""
		if len(msgs) == 1 {
			text, _ = msgs[0].(map[string]any)["content"].(string)
		}
		status, events := answer(text)
		if status != http.StatusOK {
			http.Error(w, "busy", status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
		}
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return ag
}

func newAdmin(t *testing.T) (*admin.Admin, *strings.Builder) {
	a, _ := admintest.New(t)
	var out strings.Builder
	a.Out = &out
	return a, &out
}

// Two messages: one thread, each request with its own message alone; the
// text, the tool call and the approval waited for are printed.
func TestChat(t *testing.T) {
	a, out := newAdmin(t)
	ag := serve(t, a, func(text string) (int, []string) {
		if text == "Patch the host." {
			return 200, []string{
				`{"type":"RUN_STARTED"}`,
				`{"type":"TEXT_MESSAGE_START","messageId":"x1"}`,
				`{"type":"TEXT_MESSAGE_CONTENT","messageId":"x1","delta":"Installing 1 "}`,
				`{"type":"TEXT_MESSAGE_CONTENT","messageId":"x1","delta":"patch."}`,
				`{"type":"TEXT_MESSAGE_END","messageId":"x1"}`,
				`{"type":"TOOL_CALL_START","toolCallId":"c1","toolCallName":"zypp__install_patches"}`,
				`{"type":"CUSTOM","name":"agent.approval.pending","value":{"approval_id":"a-42","tool":"zypp__install_patches","approval_url":"https://host:9090/mcp-gateway#/approvals/a-42"}}`,
				`{"type":"CUSTOM","name":"agent.approval.resolved","value":{"approval_id":"a-42","tool":"zypp__install_patches","outcome":"approved"}}`,
				`{"type":"TOOL_CALL_RESULT","toolCallId":"c1"}`,
				`{"type":"TEXT_MESSAGE_CONTENT","messageId":"x2","delta":"Done."}`,
				`{"type":"RUN_FINISHED"}`,
			}
		}
		return 200, []string{`{"type":"TEXT_MESSAGE_CONTENT","messageId":"y","delta":"Yes."}`, `{"type":"RUN_FINISHED"}`}
	})
	a.In = strings.NewReader("Patch the host.\n\n  Is it done?  \n")
	if err := Run(a, Options{Name: "sb"}); err != nil {
		t.Fatal(err, out.String())
	}
	want := "Installing 1 patch.\n" +
		"  [tool] zypp__install_patches\n" +
		"  [approval] waiting for approval a-42 of zypp__install_patches: https://host:9090/mcp-gateway#/approvals/a-42\n" +
		"  [approval] a-42 of zypp__install_patches: approved\n" +
		"  [tool] zypp__install_patches: done\n" +
		"Done.\n" +
		"Yes.\n"
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out.String(), want)
	}
	if len(ag.bodies) != 2 {
		t.Fatalf("%d requests, want 2 (the empty line is skipped)", len(ag.bodies))
	}
	t1, t2 := ag.bodies[0]["threadId"], ag.bodies[1]["threadId"]
	if t1 != t2 || !strings.HasPrefix(t1.(string), "t-cli-") {
		t.Errorf("threads %v, %v: want one, t-cli-…", t1, t2)
	}
	if ag.bodies[0]["runId"] == ag.bodies[1]["runId"] {
		t.Error("the same run id twice")
	}
	if m := ag.bodies[1]["messages"].([]any); len(m) != 1 || m[0].(map[string]any)["content"] != "Is it done?" || m[0].(map[string]any)["role"] != "user" {
		t.Errorf("second request's messages: %v", m)
	}
}

// --message: one message, on the thread given, and the chat ends; a run
// that fails is the command's error.
func TestMessage(t *testing.T) {
	a, out := newAdmin(t)
	ag := serve(t, a, func(string) (int, []string) {
		return 200, []string{`{"type":"TEXT_MESSAGE_CONTENT","messageId":"x","delta":"Looking"}`, `{"type":"RUN_ERROR","message":"the model endpoint did not answer"}`}
	})
	a.In = strings.NewReader("not read\n")
	err := Run(a, Options{Name: "sb", Thread: "t-cockpit-1", Message: "Status?"})
	if err == nil || err.Error() != "the model endpoint did not answer" {
		t.Errorf("err %v", err)
	}
	if out.String() != "Looking\n" {
		t.Errorf("output %q", out.String())
	}
	if len(ag.bodies) != 1 || ag.bodies[0]["threadId"] != "t-cockpit-1" {
		t.Errorf("requests %v", ag.bodies)
	}
}

// A thread still busy (409): the chat says so and goes on with the next
// line; the exit status says a message was not answered.
func TestBusy(t *testing.T) {
	a, out := newAdmin(t)
	n := 0
	serve(t, a, func(string) (int, []string) {
		n++
		if n == 1 {
			return http.StatusConflict, nil
		}
		return 200, []string{`{"type":"TEXT_MESSAGE_CONTENT","messageId":"x","delta":"OK"}`}
	})
	a.In = strings.NewReader("one\ntwo\n")
	err := Run(a, Options{Name: "sb"})
	if err == nil || !strings.Contains(err.Error(), "not answered") {
		t.Errorf("err %v", err)
	}
	if !strings.Contains(out.String(), "error: the agent is still working on the previous message") || !strings.HasSuffix(out.String(), "OK\n") {
		t.Errorf("output:\n%s", out.String())
	}
}

// No socket: the agent is not there or not started.
func TestNoSocket(t *testing.T) {
	a, _ := newAdmin(t)
	err := Run(a, Options{Name: "nope", Message: "hi"})
	if err == nil || !strings.Contains(err.Error(), "no socket") {
		t.Errorf("err %v", err)
	}
	if err := Run(a, Options{Name: "../x", Message: "hi"}); err == nil {
		t.Error("a bad name was taken")
	}
}
