package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestRoundTrip(t *testing.T) {
	var got request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("path %s auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"zypp__list_updates","arguments":"{\"repo\":\"main\"}"}}]}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
	}))
	defer srv.Close()
	m := &Model{Endpoint: srv.URL + "/v1/", Name: "qwen", APIKey: "k"}
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
	p := got.Tools[1].Function.Parameters.(map[string]any)
	if p["type"] != "object" || p["properties"].(map[string]any)["n"].(map[string]any)["type"] != "integer" {
		t.Errorf("schema types not lowered: %v", p)
	}
}

func TestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"model not loaded"}}`))
	}))
	defer srv.Close()
	for _, err := range (&Model{Endpoint: srv.URL, Name: "x"}).LLM().GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err == nil || err.Error() != "model endpoint: model not loaded" {
			t.Errorf("err %v", err)
		}
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
	b, _ := json.Marshal(m.request(&model.LLMRequest{Config: &genai.GenerateContentConfig{}}))
	if strings.Contains(string(b), "max_tokens") {
		t.Errorf("max_tokens sent without a limit: %s", b)
	}

	answer = `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"report_diagnosis","arguments":"{\"summary\":\"The unit"}}]},"finish_reason":"length"}],"usage":{"completion_tokens":64}}`
	_, err = call(&genai.GenerateContentConfig{MaxOutputTokens: 64})
	if err == nil || err.Error() != "model endpoint: the answer was cut off after 64 tokens (model.max_output_tokens), in the arguments of report_diagnosis" {
		t.Errorf("cut off in a tool call: %v", err)
	}
}
