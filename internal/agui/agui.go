// Package agui serves the AG-UI interaction endpoint (CONTRACT.md §11):
// RunAgentInput in, AG-UI events out as server-sent events, translated
// from the ADK runner's events and the governance plugin's.
package agui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/sdrahn/custos/internal/clienttools"
	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/governance"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/peercred"
)

// RunAgentInput is AG-UI's request. Unknown fields are ignored (O3).
type RunAgentInput struct {
	ThreadID       string          `json:"threadId"`
	RunID          string          `json:"runId"`
	Messages       []Message       `json:"messages"`
	Tools          []Tool          `json:"tools"`
	State          map[string]any  `json:"state"`
	Context        json.RawMessage `json:"context"`
	ForwardedProps json.RawMessage `json:"forwardedProps"`
}

type Message struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
}

type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// Handler is the endpoint.
type Handler struct {
	App         string
	Config      *config.Config
	Runner      *runner.Runner
	Sessions    session.Service
	Governor    *governance.Governor
	ClientTools *clienttools.Registry
	Log         journal.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST a RunAgentInput", http.StatusMethodNotAllowed)
		return
	}
	caller, ok := peercred.From(r.Context())
	if !ok {
		http.Error(w, "caller unknown", http.StatusForbidden)
		return
	}
	var in RunAgentInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
		http.Error(w, "bad RunAgentInput: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.ThreadID == "" {
		in.ThreadID = uuid.NewString()
	}
	if in.RunID == "" {
		in.RunID = uuid.NewString()
	}
	ctx := r.Context()
	sess, err := h.session(ctx, caller.Name, in.ThreadID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	msg, err := newInput(in.Messages, sess)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	s := &stream{w: w, fl: fl, thread: in.ThreadID, run: in.RunID}

	// Client tools: only those the configuration allows (§11).
	var offered []clienttools.Def
	var refused []string
	for _, t := range in.Tools {
		if h.Config.Interaction.ClientTools.Allowed(t.Name) && !strings.Contains(t.Name, "__") {
			offered = append(offered, clienttools.Def{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
		} else {
			refused = append(refused, t.Name)
		}
	}
	if len(refused) > 0 {
		h.Log.Log(journal.ToolDeniedLocal, "Client tools not allowed: "+strings.Join(refused, ", "), journal.Fields{
			"SESSION": in.ThreadID, "USER": caller.Name, "TOOL": strings.Join(refused, ","), "OUTCOME": "denied", "REASON": "client_tools",
		})
	}
	defer h.ClientTools.Set(in.ThreadID, offered)()

	delta := map[string]any{}
	for k, v := range in.State {
		delta["ui:"+k] = v
	}

	s.emit("RUN_STARTED", map[string]any{"threadId": in.ThreadID, "runId": in.RunID})
	s.emit("STATE_SNAPSHOT", map[string]any{"snapshot": snapshot(sess.State())})

	gov, cancel := h.Governor.Subscribe(in.ThreadID)
	defer cancel()

	ctx, cancelRun := context.WithTimeout(ctx, h.Config.Limits.MaxInvocation())
	defer cancelRun()
	h.Governor.Expect(in.ThreadID, governance.Start{Origin: governance.Origin{Kind: "chat", By: caller.Name}, Cancel: cancelRun})
	type item struct {
		ev  *session.Event
		err error
	}
	events := make(chan item)
	go func() {
		defer close(events)
		for ev, err := range h.Runner.Run(ctx, caller.Name, in.ThreadID, msg, agent.RunConfig{StreamingMode: agent.StreamingModeSSE}, runner.WithStateDelta(delta)) {
			select {
			case events <- item{ev, err}:
			case <-ctx.Done():
				return
			}
		}
	}()
	var runErr error
loop:
	for {
		select {
		case it, ok := <-events:
			if !ok {
				break loop
			}
			if it.err != nil {
				runErr = it.err
				continue
			}
			s.event(it.ev)
		case e := <-gov:
			s.custom(e)
		}
	}
	// Events the plugin published at the very end (e.g. a resolution).
	for {
		select {
		case e := <-gov:
			s.custom(e)
			continue
		default:
		}
		break
	}
	s.endText()
	if runErr == nil && ctx.Err() != nil {
		runErr = ctx.Err()
	}
	if runErr != nil {
		h.Governor.Failed(s.invocation, runErr)
		s.emit("RUN_ERROR", map[string]any{"message": runErr.Error()})
		return
	}
	s.emit("RUN_FINISHED", map[string]any{"threadId": in.ThreadID, "runId": in.RunID})
}

func (h *Handler) session(ctx context.Context, user, id string) (session.Session, error) {
	got, err := h.Sessions.Get(ctx, &session.GetRequest{AppName: h.App, UserID: user, SessionID: id})
	if err == nil {
		return got.Session, nil
	}
	if !errors.Is(err, session.ErrNotFound) {
		return nil, err
	}
	c, err := h.Sessions.Create(ctx, &session.CreateRequest{AppName: h.App, UserID: user, SessionID: id})
	if err != nil {
		return nil, err
	}
	return c.Session, nil
}

// newInput takes the new input from the messages: the trailing tool
// messages (answers to client tool calls), else the last user message.
// The history is the session's, never the frontend's copy (§11).
func newInput(msgs []Message, sess session.Session) (*genai.Content, error) {
	var parts []*genai.Part
	for i := len(msgs) - 1; i >= 0 && msgs[i].Role == "tool"; i-- {
		name := callName(sess, msgs[i].ToolCallID)
		if name == "" {
			return nil, fmt.Errorf("tool message for unknown call %q", msgs[i].ToolCallID)
		}
		var result any
		text := textOf(msgs[i].Content)
		if json.Unmarshal([]byte(text), &result) != nil {
			result = text
		}
		resp, ok := result.(map[string]any)
		if !ok {
			resp = map[string]any{"result": result}
		}
		// A client tool's result is a tool's output like any other: the
		// host's notice when it reads like instructions or has a
		// custos_notice of its own (security review 0.6, P5).
		if shown, _ := governance.WithNotice(name, resp); shown != nil {
			resp = shown
		}
		parts = append([]*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: msgs[i].ToolCallID, Name: name, Response: resp}}}, parts...)
	}
	if len(parts) > 0 {
		return &genai.Content{Role: genai.RoleUser, Parts: parts}, nil
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			t := textOf(msgs[i].Content)
			if t == "" {
				return nil, errors.New("the last user message has no text")
			}
			return genai.NewContentFromText(t, genai.RoleUser), nil
		}
	}
	return nil, errors.New("no user message")
}

func callName(sess session.Session, id string) string {
	for ev := range sess.Events().All() {
		if ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.FunctionCall != nil && p.FunctionCall.ID == id {
				return p.FunctionCall.Name
			}
		}
	}
	return ""
}

// textOf reads AG-UI content: a string, or a list of parts with text.
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type, Text string
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func snapshot(st session.State) map[string]any {
	out := map[string]any{}
	for k, v := range st.All() {
		if !strings.HasPrefix(k, "temp:") {
			out[k] = v
		}
	}
	return out
}

type stream struct {
	w           http.ResponseWriter
	fl          http.Flusher
	thread, run string
	textID      string // open text message
	invocation  string // the ADK invocation (activity) of the run
}

func (s *stream) emit(typ string, fields map[string]any) {
	fields["type"] = typ
	fields["timestamp"] = time.Now().UnixMilli()
	b, _ := json.Marshal(fields)
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	if s.fl != nil {
		s.fl.Flush()
	}
}

func (s *stream) endText() {
	if s.textID != "" {
		s.emit("TEXT_MESSAGE_END", map[string]any{"messageId": s.textID})
		s.textID = ""
	}
}

func (s *stream) event(ev *session.Event) {
	if s.invocation == "" {
		s.invocation = ev.InvocationID
	}
	if ev.Content != nil && ev.Author != "user" {
		for _, p := range ev.Content.Parts {
			switch {
			case p.Text != "" && !p.Thought:
				if ev.Partial {
					if s.textID == "" {
						s.textID = ev.ID
						s.emit("TEXT_MESSAGE_START", map[string]any{"messageId": s.textID, "role": "assistant"})
					}
					s.emit("TEXT_MESSAGE_CONTENT", map[string]any{"messageId": s.textID, "delta": p.Text})
				} else if s.textID != "" {
					s.endText() // the final event repeats the streamed text
				} else {
					s.emit("TEXT_MESSAGE_START", map[string]any{"messageId": ev.ID, "role": "assistant"})
					s.emit("TEXT_MESSAGE_CONTENT", map[string]any{"messageId": ev.ID, "delta": p.Text})
					s.emit("TEXT_MESSAGE_END", map[string]any{"messageId": ev.ID})
				}
			case p.FunctionCall != nil && !ev.Partial:
				s.endText()
				fc := p.FunctionCall
				args, _ := json.Marshal(fc.Args)
				s.emit("TOOL_CALL_START", map[string]any{"toolCallId": fc.ID, "toolCallName": fc.Name, "parentMessageId": ev.ID})
				s.emit("TOOL_CALL_ARGS", map[string]any{"toolCallId": fc.ID, "delta": string(args)})
				s.emit("TOOL_CALL_END", map[string]any{"toolCallId": fc.ID})
			case p.FunctionResponse != nil && !ev.Partial:
				s.endText()
				fr := p.FunctionResponse
				content, _ := json.Marshal(fr.Response)
				s.emit("TOOL_CALL_RESULT", map[string]any{"messageId": ev.ID + "-" + fr.ID, "toolCallId": fr.ID, "content": string(content), "role": "tool"})
			}
		}
	}
	if !ev.Partial && len(ev.Actions.StateDelta) > 0 {
		var ops []map[string]any
		for k, v := range ev.Actions.StateDelta {
			if strings.HasPrefix(k, "temp:") {
				continue
			}
			ops = append(ops, map[string]any{"op": "add", "path": "/" + pointerEscape(k), "value": v})
		}
		if len(ops) > 0 {
			s.emit("STATE_DELTA", map[string]any{"delta": ops})
		}
	}
}

func (s *stream) custom(e governance.Event) {
	v := map[string]any{"tool": e.Tool, "call_id": e.CallID}
	switch e.Kind {
	case governance.EvApprovalPending:
		v["approval_id"], v["approval_url"] = e.ApprovalID, e.ApprovalURL
	case governance.EvApprovalResolved:
		v["approval_id"], v["outcome"] = e.ApprovalID, e.Outcome
	case governance.EvLimitExceeded:
		v = map[string]any{"limit": e.Limit, "value": e.Value}
	case governance.EvReport:
		s.emit("CUSTOM", map[string]any{"name": e.Kind, "value": e.Report})
		return
	}
	s.emit("CUSTOM", map[string]any{"name": e.Kind, "value": v})
}

func pointerEscape(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}
