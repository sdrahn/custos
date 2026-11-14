// Package openai is an ADK model for OpenAI-compatible chat completion
// endpoints (vLLM, Ollama, llama.cpp, SUSE AI, …), so agents can run
// on-premises and air-gapped (CONTRACT.md §4). The requests and the
// answers' types are the OpenAI Go SDK's (github.com/openai/openai-go);
// this package maps them to ADK's and reads what local servers add.
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
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type Model struct {
	Endpoint    string // base URL, e.g. https://ai.example/v1
	Name        string
	APIKey      string
	Temperature *float32
	// ReasoningEffort is the request's reasoning_effort (model.
	// reasoning_effort): none, low, medium, high; "": not sent.
	ReasoningEffort string
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

// GenerateContent sends one request; it does not stream (the answer
// arrives whole, which ADK accepts in either mode).
func (m *Model) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		resp, err := m.generate(ctx, req)
		yield(resp, err)
	}
}

// answer is what the endpoint sent: read by the middleware below, for the
// errors a server words in its own way (an HTML page, a JSON error with
// status 200).
type answer struct {
	status int
	body   []byte
}

// service is the SDK's chat completion service for m. It is made from
// options alone, never from the environment (OPENAI_API_KEY,
// OPENAI_BASE_URL): the agent's configuration says where its model is.
// No retries: a second attempt would outlast the activity's limits
// unseen. No X-Stainless headers: the model server need not learn the
// host's system and Go version.
func (m *Model) service(got *answer) oai.ChatCompletionService {
	c := m.HTTP
	if c == nil {
		c = &http.Client{Timeout: m.Timeout}
	}
	opts := []option.RequestOption{
		option.WithBaseURL(strings.TrimRight(m.Endpoint, "/") + "/"),
		option.WithHTTPClient(c),
		option.WithMaxRetries(0),
		option.WithMiddleware(func(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			res, err := next(r)
			if err != nil {
				return res, err
			}
			b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
			res.Body.Close()
			if err != nil {
				return nil, err
			}
			got.status, got.body = res.StatusCode, b
			res.Body = io.NopCloser(bytes.NewReader(b))
			// A server that sends its JSON without saying so (as
			// text/plain) is read all the same, as before the SDK.
			if json.Valid(b) {
				res.Header.Set("Content-Type", "application/json")
			}
			return res, nil
		}),
	}
	for _, h := range []string{"Lang", "Package-Version", "OS", "Arch", "Runtime", "Runtime-Version", "Retry-Count", "Timeout"} {
		opts = append(opts, option.WithHeaderDel("X-Stainless-"+h))
	}
	if m.APIKey != "" {
		opts = append(opts, option.WithAPIKey(m.APIKey))
	}
	return oai.NewChatCompletionService(opts...)
}

func (m *Model) generate(ctx context.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	params, err := m.request(req)
	if err != nil {
		return nil, err
	}
	var got answer
	start := time.Now()
	svc := m.service(&got)
	r, err := svc.New(ctx, params)
	if got.status == 0 && err != nil {
		// No answer at all.
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
	// An answer the server words as an error, whatever its status.
	var said struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(got.body, &said) != nil {
		return nil, fmt.Errorf("model endpoint: HTTP %d: %.200s", got.status, got.body)
	}
	if said.Error != nil {
		return nil, fmt.Errorf("model endpoint: %s", said.Error.Message)
	}
	if err != nil || got.status != http.StatusOK || len(r.Choices) == 0 {
		return nil, fmt.Errorf("model endpoint: HTTP %d: %.200s", got.status, got.body)
	}
	msg := r.Choices[0].Message
	// "length": the answer reached max_tokens (model.max_output_tokens)
	// and was cut off.
	cutOff := r.Choices[0].FinishReason == "length"
	content := &genai.Content{Role: genai.RoleModel}
	// The thinking, as a thought: kept out of the answer's text (the
	// summary, the response) and never sent back, but counted
	// (model.call's THOUGHT_BYTES). Ollama's reasoning, or
	// reasoning_content (vLLM, llama.cpp, DeepSeek): fields the SDK does
	// not know.
	var thought string
	for _, k := range []string{"reasoning", "reasoning_content"} {
		var t string
		if f, ok := msg.JSON.ExtraFields[k]; ok && json.Unmarshal([]byte(f.Raw()), &t) == nil {
			thought += t
		}
	}
	if thought != "" {
		content.Parts = append(content.Parts, &genai.Part{Text: thought, Thought: true})
	}
	if msg.Content != "" {
		content.Parts = append(content.Parts, &genai.Part{Text: msg.Content})
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
			PromptTokenCount: int32(r.Usage.PromptTokens), CandidatesTokenCount: int32(r.Usage.CompletionTokens),
		},
	}, nil
}

// request is the chat completion request for an ADK request.
func (m *Model) request(req *model.LLMRequest) (oai.ChatCompletionNewParams, error) {
	out := oai.ChatCompletionNewParams{Model: m.Name}
	if m.Temperature != nil {
		// The shortest decimal of the float32, not its float64 expansion
		// (0.2, not 0.20000000298).
		t, _ := strconv.ParseFloat(strconv.FormatFloat(float64(*m.Temperature), 'g', -1, 32), 64)
		out.Temperature = param.NewOpt(t)
	}
	// model.reasoning_effort. Ollama (0.12) maps none to think: false;
	// OpenAI's reasoning models and vLLM read it too.
	if m.ReasoningEffort != "" {
		out.ReasoningEffort = shared.ReasoningEffort(m.ReasoningEffort)
	}
	if req.Config != nil {
		// model.max_output_tokens. max_tokens, not OpenAI's newer
		// max_completion_tokens: the servers Custos is used with (vLLM,
		// Ollama, llama.cpp, LM Studio) all read max_tokens.
		if n := req.Config.MaxOutputTokens; n > 0 {
			out.MaxTokens = param.NewOpt(int64(n))
		}
		if si := req.Config.SystemInstruction; si != nil {
			if t := text(si); t != "" {
				out.Messages = append(out.Messages, oai.SystemMessage(t))
			}
		}
		for _, t := range req.Config.Tools {
			for _, d := range t.FunctionDeclarations {
				var schema any
				switch {
				case d.ParametersJsonSchema != nil:
					schema = plainIfNoArgs(d.ParametersJsonSchema)
				case d.Parameters != nil:
					schema = plainIfNoArgs(lowerTypes(d.Parameters))
				default:
					schema = noArgs()
				}
				params, err := object(schema)
				if err != nil {
					return out, fmt.Errorf("the schema of tool %s: %w", d.Name, err)
				}
				f := shared.FunctionDefinitionParam{Name: d.Name, Parameters: params}
				if d.Description != "" {
					f.Description = param.NewOpt(d.Description)
				}
				out.Tools = append(out.Tools, oai.ChatCompletionFunctionTool(f))
			}
		}
	}
	for _, c := range req.Contents {
		var calls []oai.ChatCompletionMessageToolCallUnionParam
		var texts []string
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				a, _ := json.Marshal(p.FunctionCall.Args)
				calls = append(calls, oai.ChatCompletionMessageToolCallUnionParam{OfFunction: &oai.ChatCompletionMessageFunctionToolCallParam{
					ID:       p.FunctionCall.ID,
					Function: oai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: p.FunctionCall.Name, Arguments: string(a)},
				}})
			case p.FunctionResponse != nil:
				r, _ := json.Marshal(p.FunctionResponse.Response)
				out.Messages = append(out.Messages, oai.ToolMessage(string(r), p.FunctionResponse.ID))
			case p.Text != "" && !p.Thought:
				texts = append(texts, p.Text)
			}
		}
		if len(texts) == 0 && len(calls) == 0 {
			continue
		}
		t := strings.Join(texts, "\n")
		if c.Role != genai.RoleModel {
			out.Messages = append(out.Messages, oai.UserMessage(t))
			continue
		}
		// An assistant message with tool calls only has no content.
		msg := oai.ChatCompletionAssistantMessageParam{ToolCalls: calls}
		if t != "" {
			msg.Content.OfString = param.NewOpt(t)
		}
		out.Messages = append(out.Messages, oai.ChatCompletionMessageParamUnion{OfAssistant: &msg})
	}
	return out, nil
}

// object is a JSON Schema as the SDK takes one: a JSON object.
func object(schema any) (map[string]any, error) {
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
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
