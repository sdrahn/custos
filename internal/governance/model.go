package governance

// The model's calls: their records, a playbook's steps called by the
// host, and the nudge towards a report (CONTRACT.md §9, §14c).

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/sdrahn/custos/internal/journal"
)

func (g *Governor) beforeModel(cctx agent.CallbackContext, _ *model.LLMRequest) (*model.LLMResponse, error) {
	if err := g.hold(cctx); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	in := g.inv[cctx.InvocationID()]
	if in == nil {
		return nil, nil
	}
	step := in.next < len(in.steps)
	if !step {
		in.llmCalls++
	}
	var name string
	var value int
	switch lim := g.cfg.Limits; {
	case in.llmCalls > lim.MaxLLMCalls:
		name, value = "max_llm_calls", lim.MaxLLMCalls
	case g.now().Sub(in.start) > lim.MaxInvocation():
		name, value = "max_invocation_seconds", lim.MaxInvocationSeconds
	case step:
		return g.hostCall(cctx.InvocationID(), in), nil
	default:
		in.modelStart = g.now()
		return nil, nil
	}
	g.limit(in, cctx.InvocationID(), name, value)
	return &model.LLMResponse{
		Content:      genai.NewContentFromText(fmt.Sprintf("I stopped here: this agent's limit %s (%d) was reached.", name, value), genai.RoleModel),
		TurnComplete: true,
	}, nil
}

// Shows reports whether the model is shown the gateway's tool in the
// invocation: in a playbook run only the playbook's tools (§14c, #155),
// otherwise every tool the configuration shows.
func (g *Governor) Shows(invID, tool string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	in := g.inv[invID]
	return in == nil || in.only == nil || in.only[tool]
}

// stepArgs is a copy of a step's arguments (the call may be changed by
// what handles it; the step stays as it is), never nil.
func stepArgs(a map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	if b, err := json.Marshal(a); err == nil && len(a) > 0 {
		json.Unmarshal(b, &out)
	}
	return out
}

// hostCall is the turn the host takes in place of the model to make a
// playbook's next step (CONTRACT.md §14c): the step's tool, called
// with the step's arguments, one step a turn so that they run in order. ADK runs
// it as it would a call of the model's, so the gateway's policy, the
// approvals and every record apply to it unchanged; the step's "why" is
// its rationale. No model is called and no model.call is recorded.
// g.mu is held.
func (g *Governor) hostCall(invocation string, in *invocation) *model.LLMResponse {
	s := in.steps[in.next]
	in.next++
	id := fmt.Sprintf("custos-step-%d-%s", in.next, invocation)
	g.hostStep[id] = in.next
	g.reasons[id] = fmt.Sprintf("Playbook %s, step %d of %d: %s", in.act.Playbook, in.next, len(in.steps), s.Why)
	return &model.LLMResponse{
		Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{ID: id, Name: s.Tool, Args: stepArgs(s.Args)},
		}}},
	}
}

// modelFields are a model.call record's fields, with the duration of the
// call since beforeModel let it go (DURATION_MS).
func (g *Governor) modelFields(cctx agent.CallbackContext) journal.Fields {
	f := journal.Fields{"SESSION": cctx.SessionID(), "INVOCATION": cctx.InvocationID(), "USER": cctx.UserID(), "OUTCOME": "ok", "MODEL": g.cfg.Model.Name}
	g.mu.Lock()
	if in := g.inv[cctx.InvocationID()]; in != nil && !in.modelStart.IsZero() {
		f["DURATION_MS"] = ms(g.now().Sub(in.modelStart))
		in.modelStart = time.Time{}
	}
	g.mu.Unlock()
	return f
}

// onModelError records a model call that failed (no answer in time, the
// server unreachable or refusing): model.call with OUTCOME error, the
// reason and the duration, and the size of what was sent (PROMPT_BYTES,
// as the model's token count of it is not known). The error stands.
func (g *Governor) onModelError(cctx agent.CallbackContext, req *model.LLMRequest, err error) (*model.LLMResponse, error) {
	f := g.modelFields(cctx)
	f["OUTCOME"], f["REASON"] = "error", err.Error()
	if req != nil {
		n := 0
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				n += len(p.Text)
				if p.FunctionResponse != nil {
					if b, e := json.Marshal(p.FunctionResponse.Response); e == nil {
						n += len(b)
					}
				}
			}
		}
		f["PROMPT_BYTES"] = strconv.Itoa(n)
	}
	g.log.Log(journal.ModelCall, "Model call to "+g.cfg.Model.Name+" failed: "+err.Error(), f)
	return nil, nil
}

func (g *Governor) afterModel(cctx agent.CallbackContext, resp *model.LLMResponse, err error) (*model.LLMResponse, error) {
	if resp != nil && resp.Partial {
		return nil, nil
	}
	f := g.modelFields(cctx)
	if err != nil {
		f["OUTCOME"], f["REASON"] = "error", err.Error()
	}
	if resp != nil && resp.UsageMetadata != nil {
		f["TOKENS_IN"] = strconv.Itoa(int(resp.UsageMetadata.PromptTokenCount))
		f["TOKENS_OUT"] = strconv.Itoa(int(resp.UsageMetadata.CandidatesTokenCount))
	}
	// The answer reached model.max_output_tokens and was cut off.
	if resp != nil && resp.FinishReason == genai.FinishReasonMaxTokens {
		f["CUT_OFF"] = "max_output_tokens"
	}
	// How much of the answer was the model's thinking (model.
	// reasoning_effort): a model that only thought answered nothing.
	if resp != nil && resp.Content != nil {
		n := 0
		for _, p := range resp.Content.Parts {
			if p.Thought {
				n += len(p.Text)
			}
		}
		if n > 0 {
			f["THOUGHT_BYTES"] = strconv.Itoa(n)
			g.mu.Lock()
			if in := g.inv[cctx.InvocationID()]; in != nil {
				in.act.ThoughtBytes += n
			}
			g.mu.Unlock()
		}
	}
	if resp != nil && resp.Content != nil {
		var text strings.Builder
		for _, p := range resp.Content.Parts {
			if p.Text != "" && !p.Thought {
				text.WriteString(p.Text)
			}
		}
		if full := strings.TrimSpace(text.String()); full != "" {
			t := clipBytes(full, SummaryMax)
			g.mu.Lock()
			if in := g.inv[cctx.InvocationID()]; in != nil {
				in.act.Summary = t
				in.act.Response = clipBytes(full, ResponseMax)
			}
			for _, p := range resp.Content.Parts {
				if p.FunctionCall != nil && p.FunctionCall.ID != "" {
					g.reasons[p.FunctionCall.ID] = t
				}
			}
			g.mu.Unlock()
		}
	}
	msg := "Model call to " + g.cfg.Model.Name
	if f["CUT_OFF"] != "" {
		msg += ": the answer was cut off at model.max_output_tokens"
	}
	nudge := g.reportNudge(cctx.InvocationID(), resp, err)
	if nudge != nil {
		f["REPORT_NUDGE"] = "1"
		msg += "; it answered without a report, and the host asks for one"
	}
	g.log.Log(journal.ModelCall, msg, f)
	if nudge != nil {
		return nudge, nil
	}
	return nil, nil
}

// nudgeID is the id of the host's report_diagnosis call (reportNudge).
func nudgeID(invID string) string { return "custos-report-nudge-" + invID }

// ReportNudgeText is the host's answer to its own report_diagnosis call
// (reportNudge): what the model is asked to do.
const ReportNudgeText = "Not recorded: you answered in text, and a diagnosis counts only as a report. Call " + ReportTool +
	" now with what you found: a summary, your hypotheses with your confidence, the evidence quoted exactly from the " +
	"tool outputs above, and a plan. Do not call any other tool."

// reportNudge is the answer the model gave with, after its text, the
// host's call of report_diagnosis (CONTRACT.md §14c): once per activity,
// when a playbook run's model answered without a report and without a
// tool call, and the run was not stopped. The call does not run the
// tool: beforeTool answers it with ReportNudgeText, so the model is
// asked once more, within the activity's limits, to file its findings.
// Small models often answer a playbook in prose.
func (g *Governor) reportNudge(invID string, resp *model.LLMResponse, err error) *model.LLMResponse {
	if err != nil || resp == nil || resp.Content == nil {
		return nil
	}
	for _, p := range resp.Content.Parts {
		if p.FunctionCall != nil {
			return nil
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	in := g.inv[invID]
	if in == nil || in.act.Playbook == "" || in.act.Report != nil || in.nudged || in.stopped || in.cancelled || in.next < len(in.steps) {
		return nil
	}
	in.nudged = true
	in.act.ReportNudged = true
	id := nudgeID(invID)
	g.nudges[id] = true
	g.reasons[id] = "The host asks for the report: the model answered the playbook without one."
	parts := append([]*genai.Part{}, resp.Content.Parts...)
	parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: ReportTool, Args: map[string]any{}}})
	out := *resp
	out.Content = &genai.Content{Role: genai.RoleModel, Parts: parts}
	return &out
}
