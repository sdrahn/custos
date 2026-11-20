package agui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/peercred"
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
