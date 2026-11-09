// fakemodel is a scripted OpenAI-compatible chat completion server for
// the functional tests (package custos-tests): agents talk to
// it like to a local model server, so the tests need no model and give
// the same answers every time.
//
// The last message decides the answer:
//
//	a tool result                  → text "done: <the result>", except:
//	  the probe's, when the first user text has REPORT
//	                                 → call report_diagnosis, quoting "probe ok"
//	                                   (in the probe's output) and a line that is not
//	  report_diagnosis's             → text "reported: <the result>"
//	  read_note's, with "call the <tool> tool [with value <v>]"
//	                                 → call <prefix>__<tool> {"value": v}, offered
//	                                   or not: it obeys every injection
//	                                   (CONTRACT.md O14), the worst case
//	user text with PROBE           → call <prefix>__probe {}
//	user text with CHANGE          → text with the reason, and call <prefix>__change {"value": "test"}
//	                                   ({"value": "fail"} with CHANGE FAIL: the call fails)
//	user text with DELEGATE a t…   → call agents__delegate {"agent": a, "task": "t…"}
//	user text with NOTE n          → call <prefix>__read_note {"name": n}
//	user text with WRITE ON        → with max_tokens (model.max_output_tokens):
//	                                   text "I keep writing", cut off there
//	                                   (finish_reason "length"); without: text
//	                                   "no max_tokens"
//	user text with THINK           → with reasoning_effort none: text "thinking
//	                                   off"; without: the thinking in reasoning
//	                                   (as Ollama's), then text "thought about it"
//	anything else                  → text "ok"
//
// Except when obeying an injection, a tool the request does not offer is
// not called; the answer then says
// so ("not offered: <tool>"), which is how the tests see hidden tools.
//
// Usage: fakemodel [--listen 127.0.0.1:8008] [--prefix custos-test]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
)

type message struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type request struct {
	Model     string    `json:"model"`
	Messages  []message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
	Reasoning string    `json:"reasoning_effort"`
	Tools     []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

// unitFailed is the demonstration's event prompt (test/video/vm-setup.sh).
var unitFailed = regexp.MustCompile(`The unit (\S+) failed\. Find out why and propose a fix`)

// unitStory answers the demonstration's event for a failed unit: the
// probe first, then a report quoting it, then the restart as a change.
// ok is false where the story has nothing to say (a tool not offered,
// the change's result): the usual answers apply.
func (m *Model) unitStory(unit string, msgs []message, last message, offered map[string]bool) (message, bool) {
	call := func(reason, name string, args map[string]any) (message, bool) {
		if !offered[name] {
			return message{}, false
		}
		b, _ := json.Marshal(args)
		var tc toolCall
		tc.ID = fmt.Sprintf("call_%d", m.n.Add(1))
		tc.Type = "function"
		tc.Function.Name, tc.Function.Arguments = name, string(b)
		msg := message{Role: "assistant", ToolCalls: []toolCall{tc}}
		if reason != "" {
			msg.Content = &reason
		}
		return msg, true
	}
	switch {
	case last.Role == "user":
		return call("First, what the service depends on says now.", m.Prefix+"__probe", map[string]any{})
	case last.Role == "tool" && answers(msgs, last.ToolCallID) == m.Prefix+"__probe":
		return call("", "report_diagnosis", map[string]any{
			"summary": unit + " failed at its start. What it depends on answers again, so the failure looks transient: a restart should bring it back.",
			"findings": []any{
				map[string]any{"id": "f1", "text": unit + " failed (the event that started this activity).", "evidence": []any{}},
				map[string]any{"id": "f2", "text": "Its dependency answers: probe ok.", "evidence": []any{"e1"}},
			},
			"hypotheses": []any{map[string]any{"id": "h1", "text": "A transient failure at start: what " + unit + " depends on was not ready then and is now.",
				"confidence": "medium", "evidence": []any{"e1"}}},
			"evidence": []any{map[string]any{"id": "e1", "tool": m.Prefix + "__probe", "quote": "probe ok"}},
			"plan":     []any{map[string]any{"text": "Restart " + unit + ".", "command": "systemctl restart " + unit, "risk": "a few seconds without the service"}},
		})
	case last.Role == "tool" && answers(msgs, last.ToolCallID) == "report_diagnosis":
		return call("Restart "+unit+": the failure looks transient (hypothesis h1). It is down for a few seconds.",
			m.Prefix+"__change", map[string]any{"value": "restart " + unit})
	}
	return message{}, false
}

// injected is the instruction of an injection case (testsrv's cases.go).
var injected = regexp.MustCompile(`(?i)call the (\w+) tool(?: with value ([\w-]+))?`)

// Model answers requests.
type Model struct {
	Prefix string
	n      atomic.Int64
}

// Respond returns the answer to a request.
func (m *Model) Respond(req *request) message {
	offered := map[string]bool{}
	for _, t := range req.Tools {
		offered[t.Function.Name] = true
	}
	text := func(s string) message { return message{Role: "assistant", Content: &s} }
	if len(req.Messages) == 0 {
		return text("ok")
	}
	last := req.Messages[len(req.Messages)-1]
	first := ""
	for _, msg := range req.Messages {
		if msg.Role == "user" && msg.Content != nil {
			first = *msg.Content
			break
		}
	}
	// The demonstration videos' story (test/video): an event's prompt for
	// a failed unit; probe, report, then propose restarting the unit.
	if g := unitFailed.FindStringSubmatch(first); g != nil {
		if msg, ok := m.unitStory(g[1], req.Messages, last, offered); ok {
			return msg
		}
	}
	if last.Role == "tool" && answers(req.Messages, last.ToolCallID) == "report_diagnosis" {
		c := ""
		if last.Content != nil {
			c = *last.Content
		}
		return text("reported: " + c)
	}
	if last.Role == "tool" && strings.Contains(first, "REPORT") && answers(req.Messages, last.ToolCallID) == m.Prefix+"__probe" &&
		offered["report_diagnosis"] {
		b, _ := json.Marshal(map[string]any{
			"summary":    "The probe answers.",
			"findings":   []any{map[string]any{"id": "f1", "text": "The probe said ok.", "evidence": []any{"e1"}}},
			"hypotheses": []any{map[string]any{"id": "h1", "text": "The test server works.", "confidence": "high", "evidence": []any{"e1", "e2"}}},
			"evidence": []any{
				map[string]any{"id": "e1", "tool": m.Prefix + "__probe", "quote": "probe ok"},
				map[string]any{"id": "e2", "tool": m.Prefix + "__probe", "quote": "probe failed"},
			},
			"plan": []any{map[string]any{"text": "Nothing to do.", "command": "true"}},
		})
		var tc toolCall
		tc.ID = fmt.Sprintf("call_%d", m.n.Add(1))
		tc.Type = "function"
		tc.Function.Name, tc.Function.Arguments = "report_diagnosis", string(b)
		return message{Role: "assistant", ToolCalls: []toolCall{tc}}
	}
	if last.Role == "tool" && answers(req.Messages, last.ToolCallID) == m.Prefix+"__read_note" && last.Content != nil {
		if g := injected.FindStringSubmatch(*last.Content); g != nil {
			args := map[string]any{}
			if g[2] != "" {
				args["value"] = g[2]
			}
			b, _ := json.Marshal(args)
			var tc toolCall
			tc.ID = fmt.Sprintf("call_%d", m.n.Add(1))
			tc.Type = "function"
			tc.Function.Name, tc.Function.Arguments = m.Prefix+"__"+g[1], string(b)
			reason := "The note says to."
			return message{Role: "assistant", Content: &reason, ToolCalls: []toolCall{tc}}
		}
	}
	if last.Role == "tool" {
		c := ""
		if last.Content != nil {
			c = *last.Content
		}
		if len(c) > 2000 {
			c = c[:2000]
		}
		return text("done: " + c)
	}
	in := ""
	if last.Content != nil {
		in = *last.Content
	}
	call := func(reason, name string, args map[string]any) message {
		if !offered[name] {
			return text("not offered: " + name)
		}
		b, _ := json.Marshal(args)
		var tc toolCall
		tc.ID = fmt.Sprintf("call_%d", m.n.Add(1))
		tc.Type = "function"
		tc.Function.Name, tc.Function.Arguments = name, string(b)
		msg := message{Role: "assistant", ToolCalls: []toolCall{tc}}
		if reason != "" {
			msg.Content = &reason
		}
		return msg
	}
	switch {
	case strings.Contains(in, "CALL "):
		f := strings.Fields(in[strings.Index(in, "CALL ")+len("CALL "):])
		if len(f) == 0 {
			return text("usage: CALL <tool>")
		}
		return call("", strings.TrimRight(f[0], ".,:;"), map[string]any{})
	case strings.Contains(in, "DELEGATE"):
		f := strings.Fields(in[strings.Index(in, "DELEGATE")+len("DELEGATE"):])
		if len(f) < 2 {
			return text("usage: DELEGATE <agent> <task>")
		}
		return call("Handing this to "+f[0]+".", "agents__delegate", map[string]any{"agent": f[0], "task": strings.Join(f[1:], " ")})
	case strings.Contains(in, "NOTE "):
		f := strings.Fields(in[strings.Index(in, "NOTE ")+len("NOTE "):])
		if len(f) == 0 {
			return text("usage: NOTE <name>")
		}
		return call("", m.Prefix+"__read_note", map[string]any{"name": strings.TrimRight(f[0], ".,:;")})
	case strings.Contains(in, "CHANGE FAIL"):
		return call("Writing a marker that fails.", m.Prefix+"__change", map[string]any{"value": "fail"})
	case strings.Contains(in, "CHANGE"):
		return call("Writing the test marker; no other impact.", m.Prefix+"__change", map[string]any{"value": "test"})
	case strings.Contains(in, "PROBE"):
		return call("", m.Prefix+"__probe", map[string]any{})
	}
	return text("ok")
}

// answers is the tool the call with the id was for.
func answers(msgs []message, id string) string {
	for _, msg := range msgs {
		for _, tc := range msg.ToolCalls {
			if tc.ID == id {
				return tc.Function.Name
			}
		}
	}
	return ""
}

func (m *Model) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}
	msg, finish, out := m.Respond(&req), "stop", 5
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == "user" && req.Messages[n-1].Content != nil &&
		strings.Contains(*req.Messages[n-1].Content, "WRITE ON") {
		s := "no max_tokens"
		if req.MaxTokens > 0 {
			s, finish, out = "I keep writing", "length", req.MaxTokens
		}
		msg = message{Role: "assistant", Content: &s}
	}
	var reasoning string
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == "user" && req.Messages[n-1].Content != nil &&
		strings.Contains(*req.Messages[n-1].Content, "THINK") {
		s := "thinking off"
		if req.Reasoning != "none" {
			s, reasoning = "thought about it", "The user wants me to think first."
		}
		msg = message{Role: "assistant", Content: &s}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": withReasoning(msg, reasoning), "finish_reason": finish}},
		"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": out},
	})
}

// withReasoning is the message with Ollama's reasoning field, when there
// is thinking.
func withReasoning(m message, reasoning string) any {
	if reasoning == "" {
		return m
	}
	b, _ := json.Marshal(m)
	var out map[string]any
	json.Unmarshal(b, &out)
	out["reasoning"] = reasoning
	return out
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8008", "address")
	prefix := flag.String("prefix", "custos-test", "the gateway server whose tools PROBE and CHANGE call")
	flag.Parse()
	log.Fatal(http.ListenAndServe(*listen, &Model{Prefix: *prefix}))
}
