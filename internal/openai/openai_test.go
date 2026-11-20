package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// request is the chat completion request as a server reads it.
type request struct {
	Model    string `json:"model"`
	Messages []struct {
		Role      string  `json:"role"`
		Content   *string `json:"content"`
		ToolCalls []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"tool_calls"`
		ToolCallID string `json:"tool_call_id"`
	} `json:"messages"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name       string `json:"name"`
			Parameters any    `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	Temperature     json.Number `json:"temperature"`
	MaxTokens       int32       `json:"max_tokens"`
	ReasoningEffort string      `json:"reasoning_effort"`
}

func TestRoundTrip(t *testing.T) {
	var got request
	var sent string
	t.Setenv("OPENAI_API_KEY", "from-the-environment")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1/elsewhere")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("path %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		// Nothing about the host, and nothing from the environment.
		for k := range r.Header {
			if strings.HasPrefix(k, "X-Stainless") {
				t.Errorf("header %s sent", k)
			}
		}
		raw, _ := io.ReadAll(r.Body)
		sent = string(raw)
		json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"zypp__list_updates","arguments":"{\"repo\":\"main\"}"}}]}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
	}))
	defer srv.Close()
	temp := float32(0.2)
	m := &Model{Endpoint: srv.URL + "/v1/", Name: "qwen", APIKey: "k", Temperature: &temp}
	req := &model.LLMRequest{
		Contents: []*genai.Content{
			genai.NewContentFromText("patch it", genai.RoleUser),
			{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "c0", Name: "zypp__x", Args: map[string]any{}}}}},
			{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "c0", Name: "zypp__x", Response: map[string]any{"output": "ok"}}}}},
		},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("be careful", genai.RoleUser),
			Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
				{Name: "zypp__list_updates", ParametersJsonSchema: map[string]any{"type": "object"}},
				{Name: "g", Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"n": {Type: genai.TypeInteger}}}},
			}}},
		},
	}
	var resp *model.LLMResponse
	for r, err := range m.LLM().GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		resp = r
	}
	fc := resp.Content.Parts[0].FunctionCall
	if fc == nil || fc.ID != "c1" || fc.Args["repo"] != "main" || resp.UsageMetadata.PromptTokenCount != 7 {
		t.Errorf("response %+v", resp.Content.Parts[0])
	}
	roles := []string{}
	for _, m := range got.Messages {
		roles = append(roles, m.Role)
	}
	if want := []string{"system", "user", "assistant", "tool"}; len(roles) != 4 || roles[0] != want[0] || roles[2] != want[2] || roles[3] != want[3] {
		t.Errorf("roles %v", roles)
	}
	if got.Messages[2].ToolCalls[0].ID != "c0" || got.Messages[3].ToolCallID != "c0" {
		t.Error("tool call ids not carried")
	}
	// The assistant's message with tool calls only has the content "",
	// not none (a local server's chat template reads it); the
	// temperature is the configured decimal.
	if got.Messages[2].Content == nil || *got.Messages[2].Content != "" || got.Messages[2].ToolCalls[0].Type != "function" || got.Model != "qwen" || got.Temperature != "0.2" {
		t.Errorf("request: %s", sent)
	}
	p := got.Tools[1].Function.Parameters.(map[string]any)
	if p["type"] != "object" || p["properties"].(map[string]any)["n"].(map[string]any)["type"] != "integer" {
		t.Errorf("schema types not lowered: %v", p)
	}
}

func TestError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"model not loaded"}}`))
	}))
	defer srv.Close()
	for _, err := range (&Model{Endpoint: srv.URL, Name: "x"}).LLM().GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err == nil || err.Error() != "model endpoint: model not loaded" {
			t.Errorf("err %v", err)
		}
	}
	// One request: a retry would outlast the activity's limits unseen.
	if calls != 1 {
		t.Errorf("%d requests for one failed call", calls)
	}
	// What is no JSON at all (a proxy's page) is shown as it came, with
	// its status, also with status 200.
	for _, status := range []int{502, 200} {
		page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte("<html>Bad Gateway</html>"))
		}))
		for _, err := range (&Model{Endpoint: page.URL, Name: "x"}).LLM().GenerateContent(context.Background(), &model.LLMRequest{}, false) {
			if want := fmt.Sprintf("model endpoint: HTTP %d: <html>Bad Gateway</html>", status); err == nil || err.Error() != want {
				t.Errorf("err %v, want %s", err, want)
			}
		}
		page.Close()
	}
}

// A model server slow to answer: without model.timeout_seconds the call
// waits (the caller's context bounds it), with it the call ends, saying
// which limit ended it.
func TestTimeouts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"slow but fine"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	first := func(m *Model, ctx context.Context) error {
		for _, err := range m.LLM().GenerateContent(ctx, &model.LLMRequest{}, false) {
			return err
		}
		return nil
	}
	if err := first(&Model{Endpoint: srv.URL, Name: "x"}, context.Background()); err != nil {
		t.Errorf("no timeout of its own: %v", err)
	}
	err := first(&Model{Endpoint: srv.URL, Name: "x", Timeout: 50 * time.Millisecond}, context.Background())
	if err == nil || !strings.Contains(err.Error(), "no answer within model.timeout_seconds (50ms)") {
		t.Errorf("timeout_seconds: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = first(&Model{Endpoint: srv.URL, Name: "x"}, ctx)
	if err == nil || !strings.Contains(err.Error(), "the activity's time limit (limits.max_invocation_seconds) ran out") {
		t.Errorf("the activity's deadline: %v", err)
	}
}

// Each model call goes on a new connection. The server here answers the
// first request on a connection and then, like a proxy whose model server
// closed the idle connection behind it (OpenShell's, before LiteLLM),
// takes the next request on that connection and closes it unanswered:
// with keep-alive, the second call failed with "model endpoint: … EOF".
func TestFreshConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conns := make(chan struct{}, 10)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- struct{}{}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				io.Copy(io.Discard, req.Body)
				body := `{"choices":[{"message":{"role":"assistant","content":"fine"},"finish_reason":"stop"}]}`
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
				// The next request on this connection: read, then closed.
				if req, err := http.ReadRequest(br); err == nil {
					io.Copy(io.Discard, req.Body)
				}
			}(c)
		}
	}()
	m := &Model{Endpoint: "http://" + ln.Addr().String() + "/v1", Name: "x"}
	for i := 1; i <= 3; i++ {
		for resp, err := range m.LLM().GenerateContent(context.Background(), &model.LLMRequest{}, false) {
			if err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
			if got := resp.Content.Parts[0].Text; got != "fine" {
				t.Errorf("call %d: %q", i, got)
			}
		}
	}
	if n := len(conns); n != 3 {
		t.Errorf("%d connections for 3 calls, not one each", n)
	}
}

// A tool without arguments goes to the model with the plain schema of an
// empty object, whatever extras the MCP server's schema has; one with
// arguments is passed on as it is.
func TestNoArgumentSchema(t *testing.T) {
	strict := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	withArgs := map[string]any{"type": "object", "properties": map[string]any{"unit": map[string]any{"type": "string"}}, "required": []any{"unit"}, "additionalProperties": false}
	for _, c := range []struct {
		in   any
		want string
	}{
		{strict, `{"properties":{},"type":"object"}`},
		{map[string]any{"type": "object"}, `{"properties":{},"type":"object"}`},
		{map[string]any{"type": "object", "required": []any{}}, `{"properties":{},"type":"object"}`},
		{withArgs, `{"additionalProperties":false,"properties":{"unit":{"type":"string"}},"required":["unit"],"type":"object"}`},
		{map[string]any{"anyOf": []any{map[string]any{"type": "object"}}}, `{"anyOf":[{"type":"object"}]}`},
	} {
		b, _ := json.Marshal(plainIfNoArgs(c.in))
		if string(b) != c.want {
			t.Errorf("%v: %s, want %s", c.in, b, c.want)
		}
	}
}

// model.max_output_tokens goes to the server as max_tokens; an answer the
// server cut off there ("length") says so, and a tool call cut off in its
// arguments is an error that names the limit.
func TestMaxOutputTokens(t *testing.T) {
	var got request
	answer := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = request{}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(answer))
	}))
	defer srv.Close()
	m := &Model{Endpoint: srv.URL, Name: "qwen"}
	call := func(cfg *genai.GenerateContentConfig) (*model.LLMResponse, error) {
		var resp *model.LLMResponse
		var err error
		for r, e := range m.LLM().GenerateContent(context.Background(), &model.LLMRequest{Config: cfg,
			Contents: []*genai.Content{genai.NewContentFromText("why", genai.RoleUser)}}, false) {
			resp, err = r, e
		}
		return resp, err
	}

	answer = `{"choices":[{"message":{"role":"assistant","content":"The unit failed because"},"finish_reason":"length"}],"usage":{"prompt_tokens":7,"completion_tokens":64}}`
	resp, err := call(&genai.GenerateContentConfig{MaxOutputTokens: 64})
	if err != nil || got.MaxTokens != 64 || resp.FinishReason != genai.FinishReasonMaxTokens || resp.Content.Parts[0].Text != "The unit failed because" {
		t.Errorf("cut off: max_tokens %d, %+v %v", got.MaxTokens, resp, err)
	}

	answer = `{"choices":[{"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}],"usage":{}}`
	resp, err = call(&genai.GenerateContentConfig{})
	if err != nil || got.MaxTokens != 0 || resp.FinishReason != "" {
		t.Errorf("no limit: max_tokens %d, %+v %v", got.MaxTokens, resp, err)
	}
	params, _, _ := m.request(&model.LLMRequest{Config: &genai.GenerateContentConfig{}})
	b, _ := json.Marshal(params)
	if strings.Contains(string(b), "max_tokens") {
		t.Errorf("max_tokens sent without a limit: %s", b)
	}

	answer = `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"report_diagnosis","arguments":"{\"summary\":\"The unit"}}]},"finish_reason":"length"}],"usage":{"completion_tokens":64}}`
	_, err = call(&genai.GenerateContentConfig{MaxOutputTokens: 64})
	if err == nil || err.Error() != "model endpoint: the answer was cut off after 64 tokens (model.max_output_tokens), in the arguments of report_diagnosis" {
		t.Errorf("cut off in a tool call: %v", err)
	}
}

// model.reasoning_effort goes to the server as reasoning_effort (only
// when set); the model's thinking in an answer (Ollama's reasoning,
// vLLM's reasoning_content) is a thought, not the answer's text.
func TestReasoning(t *testing.T) {
	var got request
	var sent []byte
	answer := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = request{}
		sent, _ = io.ReadAll(r.Body)
		json.Unmarshal(sent, &got)
		w.Write([]byte(answer))
	}))
	defer srv.Close()
	call := func(m *Model) *model.LLMResponse {
		var resp *model.LLMResponse
		for r, err := range m.LLM().GenerateContent(context.Background(), &model.LLMRequest{
			Contents: []*genai.Content{genai.NewContentFromText("why", genai.RoleUser)}}, false) {
			if err != nil {
				t.Fatal(err)
			}
			resp = r
		}
		return resp
	}
	answer = `{"choices":[{"message":{"role":"assistant","content":"","reasoning":"Let me think about the unit."},"finish_reason":"length"}],"usage":{"completion_tokens":64}}`
	resp := call(&Model{Endpoint: srv.URL, Name: "qwen3"})
	if got.ReasoningEffort != "" || len(resp.Content.Parts) != 1 || !resp.Content.Parts[0].Thought || resp.Content.Parts[0].Text != "Let me think about the unit." {
		t.Errorf("Ollama's reasoning: sent %q, %+v", got.ReasoningEffort, resp.Content.Parts)
	}
	if strings.Contains(string(sent), "reasoning") {
		t.Errorf("reasoning sent without a setting, or back: %s", sent)
	}
	answer = `{"choices":[{"message":{"role":"assistant","content":"The unit failed.","reasoning_content":"Hmm."},"finish_reason":"stop"}],"usage":{}}`
	resp = call(&Model{Endpoint: srv.URL, Name: "qwen3", ReasoningEffort: "none"})
	if got.ReasoningEffort != "none" || len(resp.Content.Parts) != 2 || !resp.Content.Parts[0].Thought || resp.Content.Parts[1].Thought ||
		resp.Content.Parts[1].Text != "The unit failed." {
		t.Errorf("reasoning_content and the answer: sent %q, %+v", got.ReasoningEffort, resp.Content.Parts)
	}
}

// A tool's schema is sent as its author wrote it: a model writes its
// arguments along the order of the properties (the report's summary
// before its hypotheses and plan), so the client must not sort them.
func TestSchemaOrder(t *testing.T) {
	const report = `{"type":"object","properties":{"summary":{"type":"string"},"hypotheses":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"},"confidence":{"type":"string"}}}},"plan":{"type":"array"}},"required":["summary","hypotheses"],"additionalProperties":false}`
	const lines = `{"type":"object","properties":{"unit":{"type":"string"},"lines":{"type":"integer"}}}`
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()
	m := &Model{Endpoint: srv.URL + "/v1"}
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
		{Name: "report_diagnosis", Description: "The report.", ParametersJsonSchema: json.RawMessage(report)},
		{Name: "exec__time_status"},
		{Name: "systemd__list_log", ParametersJsonSchema: json.RawMessage(lines)},
	}}}}}
	for _, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(sent, &got); err != nil || len(got.Tools) != 3 {
		t.Fatalf("%v: %s", err, sent)
	}
	for i, want := range []string{report, `{"properties":{},"type":"object"}`, lines} {
		if f := got.Tools[i].Function; string(f.Parameters) != want || got.Tools[i].Type != "function" {
			t.Errorf("tool %d (%s): %s %s, want %s", i, f.Name, got.Tools[i].Type, f.Parameters, want)
		}
	}
	if f := got.Tools[0].Function; f.Name != "report_diagnosis" || f.Description != "The report." {
		t.Errorf("%+v", f)
	}
}

// A history out of order, as two runs on one thread or a run cut off
// left it: each tool result goes right after its call, a call without a
// result gets NoResult, a result without a call is left out, and a call
// without arguments has {}.
func TestHistoryOrder(t *testing.T) {
	call := func(id, name string, args map[string]any) *genai.Content {
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}}}
	}
	result := func(id, name, out string) *genai.Content {
		return &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: id, Name: name, Response: map[string]any{"output": out}}}}}
	}
	req := &model.LLMRequest{Contents: []*genai.Content{
		genai.NewContentFromText("What is your status?", genai.RoleUser),
		genai.NewContentFromText("What is your status?", genai.RoleUser),
		genai.NewContentFromText("Idle.", genai.RoleModel),
		genai.NewContentFromText("Give me disk usage information.", genai.RoleUser),
		call("a", "exec__disk_usage", nil),           // its result never came
		result("b", "exec__list_snapshots", "snaps"), // before its call
		genai.NewContentFromText("Here is the report.", genai.RoleModel),
		call("b", "exec__list_snapshots", map[string]any{"limit": 10}),
		result("c", "exec__get_status", "orphan"), // no call at all
		genai.NewContentFromText("What is the current disk usage?", genai.RoleUser),
	}}
	params, _, err := (&Model{Name: "m"}).request(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Messages []struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	var seq []string
	for _, m := range got.Messages {
		s := m.Role
		for _, c := range m.ToolCalls {
			s += " call " + c.ID + " " + c.Function.Arguments
		}
		if m.Role == "tool" {
			s += " " + m.ToolCallID + " " + m.Content
		}
		seq = append(seq, s)
	}
	want := []string{
		"user", "user", "assistant", "user",
		"assistant call a {}",
		"tool a " + NoResult,
		"assistant",
		`assistant call b {"limit":10}`,
		`tool b {"output":"snaps"}`,
		"user",
	}
	if strings.Join(seq, "\n") != strings.Join(want, "\n") {
		t.Errorf("messages:\n%s\nwant:\n%s", strings.Join(seq, "\n"), strings.Join(want, "\n"))
	}
}
