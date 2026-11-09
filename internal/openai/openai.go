// Package openai is an ADK model for OpenAI-compatible chat completion
// endpoints (vLLM, Ollama, llama.cpp, SUSE AI, …), so agents can run
// on-premises and air-gapped (CONTRACT.md §4).
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type Model struct {
	Endpoint    string // base URL, e.g. https://ai.example/v1
	Name        string
	APIKey      string
	Temperature *float32
	// Timeout bounds one request (0: none; the caller's context, the
	// activity's limit, does). HTTP, if set, is used as it is.
	Timeout time.Duration
	HTTP    *http.Client
}

// LLM adapts m to model.LLM (whose Name method would clash with the field).
func (m *Model) LLM() model.LLM { return llm{m} }

type llm struct{ m *Model }

func (l llm) Name() string { return l.m.Name }
func (l llm) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return l.m.GenerateContent(ctx, req, stream)
}

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

type toolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Parameters  any    `json:"parameters,omitempty"`
	} `json:"function"`
}

type request struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Tools       []toolDef `json:"tools,omitempty"`
	Temperature *float32  `json:"temperature,omitempty"`
	MaxTokens   int32     `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

type response struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int32 `json:"prompt_tokens"`
		CompletionTokens int32 `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// GenerateContent sends one request; it does not stream (the answer
// arrives whole, which ADK accepts in either mode).
func (m *Model) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		resp, err := m.generate(ctx, req)
		yield(resp, err)
	}
}

func (m *Model) generate(ctx context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	body, err := json.Marshal(m.request(req))
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.Endpoint, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if m.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+m.APIKey)
	}
	c := m.HTTP
	if c == nil {
		c = &http.Client{Timeout: m.Timeout}
	}
	start := time.Now()
	res, err := c.Do(hr)
	if err != nil {
		var ne net.Error
		switch {
		case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("model endpoint: no answer after %s, when the activity's time limit (limits.max_invocation_seconds) ran out: %w",
				time.Since(start).Round(time.Second), err)
		case errors.As(err, &ne) && ne.Timeout() && m.Timeout > 0:
			return nil, fmt.Errorf("model endpoint: no answer within model.timeout_seconds (%s): %w", m.Timeout, err)
		}
		return nil, fmt.Errorf("model endpoint: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var r response
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("model endpoint: HTTP %d: %.200s", res.StatusCode, b)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("model endpoint: %s", r.Error.Message)
	}
	if res.StatusCode != http.StatusOK || len(r.Choices) == 0 {
		return nil, fmt.Errorf("model endpoint: HTTP %d: %.200s", res.StatusCode, b)
	}
	msg := r.Choices[0].Message
	// "length": the answer reached max_tokens (model.max_output_tokens)
	// and was cut off.
	cutOff := r.Choices[0].FinishReason == "length"
	content := &genai.Content{Role: genai.RoleModel}
	if msg.Content != nil && *msg.Content != "" {
		content.Parts = append(content.Parts, &genai.Part{Text: *msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		args := map[string]any{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				if cutOff {
					return nil, fmt.Errorf("model endpoint: the answer was cut off after %d tokens (model.max_output_tokens), in the arguments of %s",
						r.Usage.CompletionTokens, tc.Function.Name)
				}
				return nil, fmt.Errorf("model endpoint: arguments of %s are not a JSON object", tc.Function.Name)
			}
		}
		id := tc.ID
		if id == "" {
			id = "call_" + uuid.NewString()
		}
		content.Parts = append(content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: tc.Function.Name, Args: args}})
	}
	var finish genai.FinishReason
	if cutOff {
		finish = genai.FinishReasonMaxTokens
	}
	return &model.LLMResponse{
		Content:      content,
		FinishReason: finish,
		TurnComplete: true,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount: r.Usage.PromptTokens, CandidatesTokenCount: r.Usage.CompletionTokens,
		},
	}, nil
}

func (m *Model) request(req *model.LLMRequest) request {
	out := request{Model: m.Name, Temperature: m.Temperature}
	if req.Config != nil {
		// model.max_output_tokens. max_tokens, not OpenAI's newer
		// max_completion_tokens: the servers Custos is used with (vLLM,
		// Ollama, llama.cpp, LM Studio) all read max_tokens.
		out.MaxTokens = req.Config.MaxOutputTokens
		if si := req.Config.SystemInstruction; si != nil {
			if t := text(si); t != "" {
				out.Messages = append(out.Messages, message{Role: "system", Content: &t})
			}
		}
		for _, t := range req.Config.Tools {
			for _, d := range t.FunctionDeclarations {
				var td toolDef
				td.Type = "function"
				td.Function.Name, td.Function.Description = d.Name, d.Description
				switch {
				case d.ParametersJsonSchema != nil:
					td.Function.Parameters = plainIfNoArgs(d.ParametersJsonSchema)
				case d.Parameters != nil:
					td.Function.Parameters = plainIfNoArgs(lowerTypes(d.Parameters))
				default:
					td.Function.Parameters = noArgs()
				}
				out.Tools = append(out.Tools, td)
			}
		}
	}
	for _, c := range req.Contents {
		role := "user"
		if c.Role == genai.RoleModel {
			role = "assistant"
		}
		var calls []toolCall
		var texts []string
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				var tc toolCall
				tc.ID, tc.Type = p.FunctionCall.ID, "function"
				tc.Function.Name = p.FunctionCall.Name
				a, _ := json.Marshal(p.FunctionCall.Args)
				tc.Function.Arguments = string(a)
				calls = append(calls, tc)
			case p.FunctionResponse != nil:
				r, _ := json.Marshal(p.FunctionResponse.Response)
				s := string(r)
				out.Messages = append(out.Messages, message{Role: "tool", ToolCallID: p.FunctionResponse.ID, Content: &s})
			case p.Text != "" && !p.Thought:
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 || len(calls) > 0 {
			t := strings.Join(texts, "\n")
			msg := message{Role: role, ToolCalls: calls}
			if t != "" || role != "assistant" {
				msg.Content = &t
			}
			out.Messages = append(out.Messages, msg)
		}
	}
	return out
}

func text(c *genai.Content) string {
	var b []string
	for _, p := range c.Parts {
		if p.Text != "" {
			b = append(b, p.Text)
		}
	}
	return strings.Join(b, "\n")
}

// lowerTypes turns a genai.Schema into JSON Schema (genai's type names
// are upper case).
// noArgs is the schema of a tool without arguments as models are used to
// it.
func noArgs() map[string]any { return map[string]any{"type": "object", "properties": map[string]any{}} }

// plainIfNoArgs gives a tool that takes no arguments the plain schema
// noArgs. An MCP server may describe such a tool as an object with no
// properties and "additionalProperties": false (mcp-gateway's exec
// server does); some models and their tool-call parsers (gpt-oss-20b in
// LM Studio) then write a truncated argument object, and the call is
// lost. A schema with properties is passed on unchanged.
func plainIfNoArgs(schema any) any {
	b, err := json.Marshal(schema)
	if err != nil {
		return schema
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return schema
	}
	if t, _ := m["type"].(string); t != "" && t != "object" {
		return schema
	}
	if p, ok := m["properties"].(map[string]any); ok && len(p) > 0 {
		return schema
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf", "$ref", "patternProperties"} {
		if _, ok := m[k]; ok {
			return schema
		}
	}
	return noArgs()
}

func lowerTypes(s *genai.Schema) any {
	b, _ := json.Marshal(s)
	var v any
	json.Unmarshal(b, &v)
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if ty, ok := t["type"].(string); ok {
				t["type"] = strings.ToLower(ty)
			}
			for _, e := range t {
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return v
}
