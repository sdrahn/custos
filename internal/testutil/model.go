// Package testutil has a scripted model for tests: no network, fixed
// answers, so the whole agent runs end to end in a test.
package testutil

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Step answers one model call. It sees the request (to check what the
// model was shown) and returns the response content.
type Step func(req *model.LLMRequest) *genai.Content

// Model plays its steps in order; after the last it answers "done".
type Model struct {
	mu    sync.Mutex
	Steps []Step
	Calls []*model.LLMRequest
}

func (m *Model) Name() string { return "scripted" }

func (m *Model) GenerateContent(_ context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	n := len(m.Calls)
	m.Calls = append(m.Calls, req)
	var c *genai.Content
	if n < len(m.Steps) {
		c = m.Steps[n](req)
	} else {
		c = genai.NewContentFromText("done", genai.RoleModel)
	}
	m.mu.Unlock()
	return func(yield func(*model.LLMResponse, error) bool) {
		if c.Role == failRole {
			yield(nil, errors.New(c.Parts[0].Text))
			return
		}
		var finish genai.FinishReason
		if c.Role == cutOffRole {
			c = genai.NewContentFromText(c.Parts[0].Text, genai.RoleModel)
			finish = genai.FinishReasonMaxTokens
		}
		usage := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5}
		if stream && len(c.Parts) == 1 && c.Parts[0].Text != "" {
			// Stream text in two halves, then the whole as final.
			t := c.Parts[0].Text
			for _, half := range []string{t[:len(t)/2], t[len(t)/2:]} {
				if !yield(&model.LLMResponse{Content: genai.NewContentFromText(half, genai.RoleModel), Partial: true}, nil) {
					return
				}
			}
		}
		yield(&model.LLMResponse{Content: c, UsageMetadata: usage, FinishReason: finish, TurnComplete: true}, nil)
	}
}

const (
	failRole   = "custos-test-failure"
	cutOffRole = "custos-test-cut-off"
)

// CutOff answers with text that reached the output limit
// (model.max_output_tokens) and was cut off.
func CutOff(s string) Step {
	return func(*model.LLMRequest) *genai.Content {
		return &genai.Content{Role: cutOffRole, Parts: []*genai.Part{{Text: s}}}
	}
}

// Fail makes the model call fail with the error (a model server that does
// not answer in time, say).
func Fail(msg string) Step {
	return func(*model.LLMRequest) *genai.Content {
		return &genai.Content{Role: failRole, Parts: []*genai.Part{{Text: msg}}}
	}
}

// Text answers with text.
func Text(s string) Step {
	return func(*model.LLMRequest) *genai.Content { return genai.NewContentFromText(s, genai.RoleModel) }
}

// Call calls a tool.
func Call(id, name string, args map[string]any) Step {
	return func(*model.LLMRequest) *genai.Content {
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}}}
	}
}

// ToolNames lists the function declarations of a request.
func ToolNames(req *model.LLMRequest) []string {
	var out []string
	if req.Config == nil {
		return nil
	}
	for _, t := range req.Config.Tools {
		for _, d := range t.FunctionDeclarations {
			out = append(out, d.Name)
		}
	}
	return out
}

// LastFunctionResponse returns the last function response sent to the model.
func LastFunctionResponse(req *model.LLMRequest) *genai.FunctionResponse {
	for i := len(req.Contents) - 1; i >= 0; i-- {
		for _, p := range req.Contents[i].Parts {
			if p.FunctionResponse != nil {
				return p.FunctionResponse
			}
		}
	}
	return nil
}

// Has reports whether s contains all of subs.
func Has(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}

// SayAndCall says something and calls a tool in one answer, as a model
// explaining what it is about to do.
func SayAndCall(text, id, name string, args map[string]any) Step {
	return func(*model.LLMRequest) *genai.Content {
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: text}, {FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}}}
	}
}
