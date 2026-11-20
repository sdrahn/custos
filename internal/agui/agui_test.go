package agui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"

	"github.com/sdrahn/custos/internal/clienttools"
	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/governance"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/peercred"
	"github.com/sdrahn/custos/internal/testutil"
)

func TestTextOf(t *testing.T) {
	for raw, want := range map[string]string{
		`"hello"`: "hello",
		`[{"type":"text","text":"a"},{"type":"image","text":"x"},{"type":"text","text":"b"}]`: "ab",
		`42`: "",
	} {
		if got := textOf(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

func TestPointerEscape(t *testing.T) {
	if got := pointerEscape("ui:a/b~c"); got != "ui:a~1b~0c" {
		t.Errorf("%q", got)
	}
}

func TestNewInputNeedsUserMessage(t *testing.T) {
	if _, err := newInput([]Message{{Role: "assistant", Content: json.RawMessage(`"x"`)}}, nil); err == nil {
		t.Error("no user message accepted")
	}
	c, err := newInput([]Message{{Role: "user", Content: json.RawMessage(`"first"`)}, {Role: "assistant", Content: json.RawMessage(`"y"`)}, {Role: "user", Content: json.RawMessage(`"last"`)}}, nil)
	if err != nil || c.Parts[0].Text != "last" {
		t.Errorf("only the last user message is input: %v %v", c, err)
	}
}

func TestOneRunPerThread(t *testing.T) {
	h := &Handler{}
	release, ok := h.claim("alice\x00t1")
	if !ok {
		t.Fatal("a free thread was refused")
	}
	if _, ok := h.claim("alice\x00t1"); ok {
		t.Error("a second run on the same thread was allowed")
	}
	other, ok := h.claim("alice\x00t2")
	if !ok {
		t.Error("another thread was refused")
	}
	other()
	if r, ok := h.claim("bob\x00t1"); !ok {
		t.Error("another caller's thread of the same id was refused")
	} else {
		r()
	}

	// The request on a busy thread is refused before anything runs.
	body := `{"threadId":"t1","messages":[{"role":"user","content":"again"}]}`
	req := httptest.NewRequest(http.MethodPost, "/agui", strings.NewReader(body))
	req = req.WithContext(peercred.WithCaller(req.Context(), peercred.Caller{Name: "alice", UID: 1000}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "a run is in progress") {
		t.Errorf("busy thread: %d %q", rec.Code, rec.Body.String())
	}

	release()
	if r, ok := h.claim("alice\x00t1"); !ok {
		t.Error("the thread stayed busy after its run")
	} else {
		r()
	}
}

// endWatch calls atEnd when the run's last event is written: what a
// client sees as the end of the run.
type endWatch struct {
	*httptest.ResponseRecorder
	atEnd func()
	seen  bool
}

func (w *endWatch) Write(p []byte) (int, error) {
	if !w.seen && bytes.Contains(p, []byte(`"RUN_FINISHED"`)) {
		w.seen = true
		w.atEnd()
	}
	return w.ResponseRecorder.Write(p)
}

// The thread is free when the client sees the run's end: a client tool's
// answer, or the next message, sent at once is not refused (409).
func TestThreadFreeAtRunEnd(t *testing.T) {
	cfg, err := config.Parse([]byte(`version: 1
name: tt
model: {provider: openai, name: test, endpoint: "http://127.0.0.1:1"}
instruction: Answer.
gateway: {socket: /run/mcp-gateway/mcp.sock, servers: [zypp]}
interaction: {agui: true}
`))
	if err != nil {
		t.Fatal(err)
	}
	log := &journal.Memory{}
	gov := governance.New(cfg, log)
	plug, err := gov.Plugin()
	if err != nil {
		t.Fatal(err)
	}
	a, err := llmagent.New(llmagent.Config{Name: "t", Model: &testutil.Model{}})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	r, err := runner.New(runner.Config{AppName: "t", Agent: a, SessionService: sessions, PluginConfig: runner.PluginConfig{Plugins: []*plugin.Plugin{plug}}})
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{App: "t", Config: cfg, Runner: r, Sessions: sessions, Governor: gov, ClientTools: clienttools.NewRegistry(), Log: log}
	w := &endWatch{ResponseRecorder: httptest.NewRecorder()}
	w.atEnd = func() {
		if release, ok := h.claim("alice\x00t1"); !ok {
			t.Error("the thread is still taken when the client sees RUN_FINISHED")
		} else {
			release()
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/agui", strings.NewReader(`{"threadId":"t1","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(peercred.WithCaller(req.Context(), peercred.Caller{Name: "alice", UID: 1000}))
	h.ServeHTTP(w, req)
	if !w.seen {
		t.Fatalf("no RUN_FINISHED:\n%s", w.Body.String())
	}
	if release, ok := h.claim("alice\x00t1"); !ok {
		t.Error("the thread stayed taken after the run")
	} else {
		release()
	}
}
