// Package governance is the ADK plugin every agent loads (CONTRACT.md
// §8): local limits, audit records (§9), and the gateway's approval
// notices (§7) passed on to the journal and to AG-UI streams (§11).
//
// It runs inside the agent's process. Its limits keep an honest agent
// within budget; they are not a security boundary (§1).
package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/plugin"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gateway"
	"github.com/sdrahn/custos/internal/journal"
)

// Event kinds published to subscribers (the AG-UI CUSTOM event names).
const (
	EvApprovalPending  = "agent.approval.pending"
	EvApprovalResolved = "agent.approval.resolved"
	EvLimitExceeded    = "agent.limit.exceeded"
)

// Event is published to the subscribers of a session.
type Event struct {
	Kind        string
	Session     string
	Invocation  string
	CallID      string
	Tool        string
	ApprovalID  string
	ApprovalURL string
	Outcome     string
	Limit       string
	Value       int
	// Report is EvReport's (§4a).
	Report *Report
}

// Call is a tool call of an activity.
type Call struct {
	Session    string     `json:"session"`
	Invocation string     `json:"activity"`
	CallID     string     `json:"call_id"`
	Tool       string     `json:"tool"`
	ArgsSHA256 string     `json:"args_sha256"`
	Since      time.Time  `json:"since"`
	Ended      *time.Time `json:"ended,omitempty"`
	State      string     `json:"state"` // running | waiting_approval | ok | error
	ApprovalID string     `json:"approval_id,omitempty"`
	Approval   string     `json:"approval,omitempty"` // pending | approved | declined
	Reason     string     `json:"reason,omitempty"`
	// Rationale is the model's text that came with the call: the agent's
	// stated reason and expected impact. Self-asserted (CONTRACT.md §7).
	Rationale string `json:"rationale,omitempty"`
	// SuspectedInjection are signs of a prompt injection in the outputs
	// the model read before the call (injection.go, O14); Preceding are
	// those outputs, for a call that waits for an approval. From the
	// agent's process: self-asserted, like the rationale.
	SuspectedInjection []Suspicion `json:"suspected_injection,omitempty"`
	Preceding          []Preceding `json:"preceding,omitempty"`
	// PlaybookStep is the number (from 1) of the playbook step this call
	// is: the host made it, not the model (CONTRACT.md §14c).
	PlaybookStep int `json:"playbook_step,omitempty"`
}

// Origin says what started an activity (CONTRACT.md §4).
type Origin struct {
	Kind     string `json:"kind"`               // chat | schedule | event | delegation | api
	By       string `json:"by"`                 // a user, a schedule's name, an event source, an agent principal
	Schedule string `json:"schedule,omitempty"` // schedules only
	Event    string `json:"event,omitempty"`    // events only: the agent's event (§14)
}

// Ref names an activity of an agent, possibly on another agent.
type Ref struct {
	Agent    string `json:"agent"`
	Activity string `json:"activity"`
}

// Activity is one invocation of the agent: one unit of work with its
// tool calls, as /v1/activities shows it (CONTRACT.md §4).
type Activity struct {
	ID       string     `json:"id"`
	Agent    string     `json:"agent"`
	Session  string     `json:"session"`
	User     string     `json:"user"`
	Origin   Origin     `json:"origin"`
	Parent   *Ref       `json:"parent,omitempty"`
	Root     Ref        `json:"root"`
	State    string     `json:"state"`
	Started  time.Time  `json:"started"`
	Ended    *time.Time `json:"ended,omitempty"`
	Summary  string     `json:"summary,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Calls    []Call     `json:"calls"`
	LLMCalls int        `json:"llm_calls"`
	// Playbook is the diagnostic playbook it runs (§14c), if any.
	Playbook string `json:"playbook,omitempty"`
	// Report is its diagnosis report (§4a), if it wrote one.
	Report *Report `json:"report,omitempty"`
	// Response is the model's last text in full (Summary is its start),
	// at most ResponseMax bytes. GET /v1/activities leaves it out.
	Response string `json:"response,omitempty"`
	// ReportNudged: the playbook run's model answered without a report,
	// and the host asked it once to file one (report_diagnosis).
	ReportNudged bool `json:"report_nudged,omitempty"`
	// ThoughtBytes is the size of the model's thinking over the
	// activity's model calls (model.reasoning_effort), never its text.
	ThoughtBytes int `json:"thought_bytes,omitempty"`
}

// SummaryMax and ResponseMax bound an activity's summary and response.
const (
	SummaryMax  = 500
	ResponseMax = 32 << 10
)

// clipBytes shortens s to at most max bytes, at a rune boundary, marking the cut.
func clipBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max] + "…"
}

// Activity states.
const (
	StateRunning   = "running"
	StateWaiting   = "waiting_approval"
	StatePaused    = "paused"
	StateFinished  = "finished"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
	StateLimit     = "limit"
)

// Start describes an activity about to start in a session: who started
// it and how to cancel it. Without one, an activity is origin "api".
type Start struct {
	Origin Origin
	// Playbook is the diagnostic playbook the activity runs (§14c), and
	// Steps the calls the host makes for it, in order, before the
	// model's first turn.
	Playbook string
	Steps    []Step
	// Tools, for a playbook, are the gateway's tools the model is shown
	// in the activity (§14c): the playbook's. Nil: every tool.
	Tools  []string
	Parent *Ref
	Root   *Ref
	Cancel context.CancelFunc
	// Started, if set, is told the activity's id when it starts.
	Started func(id string)
}

// Step is a playbook step the host runs: a tool, its arguments (nil:
// none), and why.
type Step struct {
	Tool, Why string
	Args      map[string]any
}

// ClientTool marks tools defined by an AG-UI frontend (§11).
type ClientTool interface {
	tool.Tool
	ClientTool() bool
}

type invocation struct {
	session, user string
	start         time.Time
	llmCalls      int
	toolCalls     int
	stopped       bool
	act           *Activity
	calls         []*Call
	cancel        context.CancelFunc
	cancelled     bool
	// results are the tools' outputs, for verifying a report's quotes
	// (§4a); resultBytes their size.
	results     []toolResult
	resultBytes int
	// modelStart is when the model call in progress started, for its
	// record's duration.
	modelStart time.Time
	// steps are the playbook steps the host runs, next the next one.
	steps []Step
	next  int
	// nudged: the host asked for the report once (reportNudge).
	nudged bool
	// only are the gateway's tools the model is shown (a playbook's);
	// nil: every tool.
	only map[string]bool
}

// History is how many ended activities are kept in memory; the journal
// keeps them all.
const History = 200

type Governor struct {
	cfg *config.Config
	log journal.Logger
	now func() time.Time
	// ApprovalURL makes the link to the gateway's approval page.
	ApprovalURL func(id string) string

	mu       sync.Mutex
	inv      map[string]*invocation
	inflight []*Call // in start order
	subs     map[string]map[chan Event]struct{}
	expect   map[string]Start
	ended    []*Activity // oldest first, at most History
	paused   bool
	resume   chan struct{}     // closed on Resume
	reasons  map[string]string // function call id -> rationale
	hostStep map[string]int    // function call id -> playbook step
	nudges   map[string]bool   // function call id -> the host's report nudge
	// historyPath keeps them across restarts (Keep); historyErr is the
	// last failure to write it.
	historyPath, historyErr string
}

// New returns a governor for the agent cfg, writing records to log.
func New(cfg *config.Config, log journal.Logger) *Governor {
	return &Governor{
		cfg: cfg, log: log, now: time.Now,
		ApprovalURL: func(id string) string {
			return "/cockpit/@localhost/mcp-gateway/index.html#/approvals/" + id
		},
		inv:      map[string]*invocation{},
		subs:     map[string]map[chan Event]struct{}{},
		expect:   map[string]Start{},
		reasons:  map[string]string{},
		hostStep: map[string]int{},
		nudges:   map[string]bool{},
		resume:   make(chan struct{}),
	}
}

// Plugin returns the ADK plugin.
func (g *Governor) Plugin() (*plugin.Plugin, error) {
	return plugin.New(plugin.Config{
		Name:                 "custos-governance",
		BeforeRunCallback:    g.beforeRun,
		AfterRunCallback:     g.afterRun,
		BeforeModelCallback:  g.beforeModel,
		AfterModelCallback:   g.afterModel,
		BeforeToolCallback:   g.beforeTool,
		AfterToolCallback:    g.afterTool,
		OnToolErrorCallback:  g.onToolError,
		OnModelErrorCallback: g.onModelError,
	})
}

// Subscribe returns the events of a session until cancel is called.
func (g *Governor) Subscribe(session string) (<-chan Event, func()) {
	ch := make(chan Event, 32)
	g.mu.Lock()
	if g.subs[session] == nil {
		g.subs[session] = map[chan Event]struct{}{}
	}
	g.subs[session][ch] = struct{}{}
	g.mu.Unlock()
	return ch, func() {
		g.mu.Lock()
		delete(g.subs[session], ch)
		if len(g.subs[session]) == 0 {
			delete(g.subs, session)
		}
		g.mu.Unlock()
	}
}

// publish must be called with g.mu held.
func (g *Governor) publish(e Event) {
	for ch := range g.subs[e.Session] {
		select {
		case ch <- e:
		default: // a slow stream misses an event; the journal has it
		}
	}
}

// InFlight returns the tool calls in flight.
func (g *Governor) InFlight() []Call {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]Call, len(g.inflight))
	for i, c := range g.inflight {
		out[i] = *c
	}
	return out
}

// Expect registers how the next activity in session was started.
func (g *Governor) Expect(session string, st Start) {
	g.mu.Lock()
	g.expect[session] = st
	g.mu.Unlock()
}

// Activities returns the running activities and the recently ended
// ones, newest first.
func (g *Governor) Activities() []Activity {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []Activity
	for _, in := range g.inv {
		out = append(out, g.snapshot(in))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	for i := len(g.ended) - 1; i >= 0; i-- {
		out = append(out, copyActivity(g.ended[i]))
	}
	return out
}

// Activity returns one activity.
func (g *Governor) Activity(id string) (Activity, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if in := g.inv[id]; in != nil {
		return g.snapshot(in), true
	}
	for _, a := range g.ended {
		if a.ID == id {
			return copyActivity(a), true
		}
	}
	return Activity{}, false
}

// snapshot must be called with g.mu held.
func (g *Governor) snapshot(in *invocation) Activity {
	a := copyActivity(in.act)
	a.Calls = []Call{} // an array, never null (§4), also without calls
	for _, c := range in.calls {
		a.Calls = append(a.Calls, *c)
	}
	a.State = StateRunning
	for _, c := range in.calls {
		if c.State == StateWaiting {
			a.State = StateWaiting
		}
	}
	if g.paused && a.State == StateRunning {
		a.State = StatePaused
	}
	a.LLMCalls = in.llmCalls
	return a
}

func copyActivity(a *Activity) Activity {
	c := *a
	c.Calls = append([]Call{}, a.Calls...)
	return c
}

// Failed records that an activity's run ended in an error the runner
// returned (the plugin's callbacks do not see those, e.g. a gateway that
// cannot be reached): it is failed, with the error as its reason.
func (g *Governor) Failed(id string, err error) {
	if id == "" || err == nil {
		return
	}
	g.mu.Lock()
	var a *Activity
	for _, e := range g.ended {
		if e.ID == id {
			a = e
		}
	}
	if a == nil || a.State == StateCancelled || a.State == StateLimit {
		g.mu.Unlock()
		return
	}
	a.State, a.Reason = StateFailed, err.Error()
	f := journal.Fields{"SESSION": a.Session, "INVOCATION": id, "USER": a.User, "OUTCOME": "error", "REASON": a.Reason}
	g.mu.Unlock()
	g.log.Log(journal.InvocationEnd, "Invocation "+id+" failed: "+a.Reason, f)
}

// Cancel ends a running activity (CONTRACT.md §8). It reports whether
// the activity was running.
func (g *Governor) Cancel(id, by string) bool {
	g.mu.Lock()
	in := g.inv[id]
	if in == nil || in.cancel == nil {
		g.mu.Unlock()
		return false
	}
	in.cancelled = true
	in.act.Reason = "cancelled by " + by
	cancel := in.cancel
	f := journal.Fields{"SESSION": in.session, "INVOCATION": id, "USER": by, "OUTCOME": "cancelled"}
	g.mu.Unlock()
	g.log.Log(journal.ActivityCancel, "Activity "+id+" cancelled by "+by, f)
	cancel()
	return true
}

// Pause holds every activity before its next model or tool call, and
// refuses new ones, until Resume.
func (g *Governor) Pause(by string) {
	g.mu.Lock()
	if !g.paused {
		g.paused = true
		g.resume = make(chan struct{})
	}
	g.mu.Unlock()
	g.log.Log(journal.AgentPaused, "Agent paused by "+by, journal.Fields{"USER": by})
}

// Resume lets held activities go on.
func (g *Governor) Resume(by string) {
	g.mu.Lock()
	if g.paused {
		g.paused = false
		close(g.resume)
	}
	g.mu.Unlock()
	g.log.Log(journal.AgentResumed, "Agent resumed by "+by, journal.Fields{"USER": by})
}

// Paused reports whether the agent is paused.
func (g *Governor) Paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// hold waits while the agent is paused.
func (g *Governor) hold(ctx context.Context) error {
	for {
		g.mu.Lock()
		paused, resume := g.paused, g.resume
		g.mu.Unlock()
		if !paused {
			return nil
		}
		select {
		case <-resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (g *Governor) beforeRun(ictx agent.InvocationContext) (*genai.Content, error) {
	s := ictx.Session()
	id := ictx.InvocationID()
	g.mu.Lock()
	st, ok := g.expect[s.ID()]
	delete(g.expect, s.ID())
	if !ok {
		st = Start{Origin: Origin{Kind: "api", By: s.UserID()}}
	}
	root := Ref{Agent: g.cfg.Name, Activity: id}
	if st.Root != nil {
		root = *st.Root
	}
	act := &Activity{ID: id, Agent: g.cfg.Name, Session: s.ID(), User: s.UserID(), Origin: st.Origin, Parent: st.Parent, Root: root, State: StateRunning, Started: g.now(), Playbook: st.Playbook}
	paused := g.paused
	if !paused {
		g.inv[id] = &invocation{session: s.ID(), user: s.UserID(), start: act.Started, act: act, cancel: st.Cancel, steps: st.Steps}
		if st.Tools != nil {
			g.inv[id].only = map[string]bool{}
			for _, t := range st.Tools {
				g.inv[id].only[t] = true
			}
		}
	}
	g.mu.Unlock()
	f := journal.Fields{
		"SESSION": s.ID(), "INVOCATION": id, "USER": s.UserID(),
		"ORIGIN": st.Origin.Kind, "ORIGIN_BY": st.Origin.By, "SCHEDULE": st.Origin.Schedule, "ORIGIN_EVENT": st.Origin.Event,
		"ROOT_AGENT": root.Agent, "ROOT_ACTIVITY": root.Activity, "PLAYBOOK": st.Playbook,
	}
	if st.Parent != nil {
		f["PARENT_AGENT"], f["PARENT_ACTIVITY"] = st.Parent.Agent, st.Parent.Activity
	}
	if paused {
		if st.Started != nil {
			st.Started("")
		}
		f["OUTCOME"], f["REASON"] = "denied", "agent paused"
		g.log.Log(journal.InvocationStart, "Invocation "+id+" refused: the agent is paused", f)
		return genai.NewContentFromText("This agent is paused by an administrator; nothing was done.", genai.RoleModel), nil
	}
	g.log.Log(journal.InvocationStart, fmt.Sprintf("Invocation %s started (%s by %s)", id, st.Origin.Kind, st.Origin.By), f)
	if st.Started != nil {
		st.Started(id)
	}
	return nil, nil
}

func (g *Governor) afterRun(ictx agent.InvocationContext) {
	g.mu.Lock()
	in := g.inv[ictx.InvocationID()]
	delete(g.inv, ictx.InvocationID())
	// A nudge whose call did not run (the activity ended first).
	delete(g.nudges, nudgeID(ictx.InvocationID()))
	delete(g.reasons, nudgeID(ictx.InvocationID()))
	for id := range g.hostStep { // steps never made: the activity ended first
		if strings.HasSuffix(id, "-"+ictx.InvocationID()) {
			delete(g.hostStep, id)
			delete(g.reasons, id)
		}
	}
	if in == nil {
		g.mu.Unlock()
		return
	}
	a := g.snapshot(in)
	now := g.now()
	a.Ended = &now
	switch {
	case in.cancelled:
		a.State = StateCancelled
	case in.stopped:
		a.State = StateLimit
	case ictx.Err() != nil:
		a.State = StateFailed
		if a.Reason == "" {
			a.Reason = ictx.Err().Error()
		}
	default:
		a.State = StateFinished
	}
	g.ended = append(g.ended, &a)
	if len(g.ended) > History {
		g.ended = g.ended[len(g.ended)-History:]
	}
	g.saveHistory()
	g.mu.Unlock()
	outcome := map[string]string{StateFinished: "ok", StateCancelled: "cancelled", StateLimit: "limit", StateFailed: "error"}[a.State]
	g.log.Log(journal.InvocationEnd, fmt.Sprintf("Invocation %s %s: %d model calls, %d tool calls", a.ID, a.State, in.llmCalls, in.toolCalls), journal.Fields{
		"SESSION": in.session, "INVOCATION": a.ID, "USER": in.user, "OUTCOME": outcome, "REASON": a.Reason,
		"DURATION_MS": ms(now.Sub(in.start)), "LLM_CALLS": strconv.Itoa(in.llmCalls), "TOOL_CALLS": strconv.Itoa(in.toolCalls),
		"ROOT_AGENT": a.Root.Agent, "ROOT_ACTIVITY": a.Root.Activity,
	})
}

// limit records a limit reached; g.mu must be held.
func (g *Governor) limit(in *invocation, invID, name string, value int) {
	in.stopped = true
	g.publish(Event{Kind: EvLimitExceeded, Session: in.session, Invocation: invID, Limit: name, Value: value})
	g.log.Log(journal.LimitExceeded, fmt.Sprintf("Limit %s (%d) reached", name, value), journal.Fields{
		"SESSION": in.session, "INVOCATION": invID, "USER": in.user, "OUTCOME": "limit", "REASON": name + "=" + strconv.Itoa(value),
	})
}

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

func (g *Governor) beforeTool(tctx agent.ToolContext, t tool.Tool, args map[string]any) (map[string]any, error) {
	if t.Name() == ReportTool {
		// The host's own nudge (reportNudge): answered here, the tool
		// does not run.
		g.mu.Lock()
		nudge := g.nudges[tctx.FunctionCallID()]
		delete(g.nudges, tctx.FunctionCallID())
		delete(g.reasons, tctx.FunctionCallID())
		g.mu.Unlock()
		if nudge {
			return map[string]any{"error": ReportNudgeText}, nil
		}
		return nil, nil // the host's own, recorded as diagnosis.report
	}
	f := g.toolFields(tctx, t.Name(), args)
	if ct, ok := t.(ClientTool); ok && ct.ClientTool() && !g.cfg.Interaction.ClientTools.Allowed(t.Name()) {
		f["OUTCOME"], f["REASON"] = "denied", "client tool not allowed"
		g.log.Log(journal.ToolDeniedLocal, "Client tool "+t.Name()+" is not allowed", f)
		return map[string]any{"error": "this agent does not allow the client tool " + t.Name()}, nil
	}
	if err := g.hold(tctx); err != nil {
		return nil, err
	}
	g.mu.Lock()
	in := g.inv[tctx.InvocationID()]
	if in != nil {
		in.toolCalls++
		if in.toolCalls > g.cfg.Limits.MaxToolCalls {
			g.limit(in, tctx.InvocationID(), "max_tool_calls", g.cfg.Limits.MaxToolCalls)
			g.mu.Unlock()
			return map[string]any{"error": "tool call limit reached; finish without further tool calls"}, nil
		}
		if IsDelegation(t.Name()) && args != nil {
			// The trace of §9; the callee checks the caller by SO_PEERCRED.
			args[TraceArg] = map[string]any{
				"root_agent": in.act.Root.Agent, "root_activity": in.act.Root.Activity,
				"parent_agent": g.cfg.Name, "parent_activity": in.act.ID,
			}
		}
	}
	c := &Call{Session: tctx.SessionID(), Invocation: tctx.InvocationID(), CallID: tctx.FunctionCallID(), Tool: t.Name(), ArgsSHA256: f["ARGS_SHA256"], Since: g.now(), State: StateRunning, Rationale: g.reasons[tctx.FunctionCallID()], PlaybookStep: g.hostStep[tctx.FunctionCallID()]}
	delete(g.reasons, tctx.FunctionCallID())
	delete(g.hostStep, tctx.FunctionCallID())
	f["RATIONALE"] = c.Rationale
	if c.PlaybookStep > 0 {
		f["PLAYBOOK_STEP"] = strconv.Itoa(c.PlaybookStep)
	}
	if in != nil && c.PlaybookStep == 0 {
		if c.SuspectedInjection = suspect(t.Name(), argStrings(args), in.lastResults(c.CallID)); len(c.SuspectedInjection) > 0 {
			f["SUSPECTED_INJECTION"] = suspicionText(c.SuspectedInjection)
		}
	}
	g.inflight = append(g.inflight, c)
	if in != nil {
		in.calls = append(in.calls, c)
	}
	g.mu.Unlock()
	g.log.Log(journal.ToolCall, "Tool call "+t.Name(), f)
	return nil, nil
}

func (g *Governor) afterTool(tctx agent.ToolContext, t tool.Tool, args, result map[string]any, err error) (map[string]any, error) {
	f := g.toolFields(tctx, t.Name(), args)
	g.mu.Lock()
	var call *Call
	for i, c := range g.inflight {
		if c.CallID == tctx.FunctionCallID() && c.Invocation == tctx.InvocationID() {
			call = c
			g.inflight = append(g.inflight[:i:i], g.inflight[i+1:]...)
			break
		}
	}
	if call == nil {
		g.mu.Unlock()
		return nil, nil // stopped by beforeTool: already recorded
	}
	if in := g.inv[tctx.InvocationID()]; in != nil && err == nil {
		in.keepResult(call.CallID, call.Tool, result)
	}
	now := g.now()
	f["DURATION_MS"] = ms(now.Sub(call.Since))
	errText := ""
	if err != nil {
		errText = err.Error()
	} else if e, ok := result["error"].(string); ok {
		errText = e
	}
	call.Ended, call.State, call.Reason = &now, "ok", errText
	if errText != "" {
		call.State = "error"
	}
	if call.ApprovalID != "" {
		outcome := "approved"
		if errText != "" {
			outcome = "declined"
		}
		call.Approval = outcome
		g.publish(Event{Kind: EvApprovalResolved, Session: call.Session, Invocation: call.Invocation, CallID: call.CallID, Tool: call.Tool, ApprovalID: call.ApprovalID, Outcome: outcome})
		af := copyFields(f)
		af["APPROVAL_ID"], af["OUTCOME"], af["REASON"] = call.ApprovalID, outcome, errText
		g.mu.Unlock()
		g.log.Log(journal.ApprovalResolved, fmt.Sprintf("Approval %s for %s: %s", call.ApprovalID, call.Tool, outcome), af)
		f["APPROVAL_ID"] = call.ApprovalID
	} else {
		g.mu.Unlock()
	}
	f["OUTCOME"] = "ok"
	if errText != "" {
		f["OUTCOME"], f["REASON"] = "error", errText
	}
	g.log.Log(journal.ToolResult, "Tool result "+t.Name()+": "+f["OUTCOME"], f)
	return nil, nil
}

// onToolError records a call to a tool the agent was not given: the
// gateway did not offer it (not permitted, hidden by the mode or the
// enablement state) and it is no client tool. The model asked for it all
// the same, which a prompt injection does (CONTRACT.md O14): it is
// journaled as tool.denied.local, reason "not offered", and the model is
// told so. It never reaches the gateway. Other tool errors are left to
// the runtime.
func (g *Governor) onToolError(tctx agent.ToolContext, t tool.Tool, args map[string]any, err error) (map[string]any, error) {
	if err == nil || !strings.HasPrefix(err.Error(), "tool '"+t.Name()+"' not found") {
		return nil, nil
	}
	f := g.toolFields(tctx, t.Name(), args)
	f["OUTCOME"], f["REASON"] = "denied", NotOffered
	g.mu.Lock()
	f["RATIONALE"] = g.reasons[tctx.FunctionCallID()]
	delete(g.reasons, tctx.FunctionCallID())
	g.mu.Unlock()
	g.log.Log(journal.ToolDeniedLocal, "Tool "+t.Name()+" is not offered to this agent", f)
	return map[string]any{"error": "the tool " + t.Name() + " is not offered to this agent"}, nil
}

// NotOffered is tool.denied.local's REASON for a call to a tool the
// agent was not given.
const NotOffered = "not offered"

// OnGatewayLog handles the gateway's logging messages: an approval
// notice is assigned to the call it names (its progress token,
// gateway.CallToken); from a gateway that names only the tool, to the
// oldest call of its tool in flight without one (CONTRACT.md §7).
func (g *Governor) OnGatewayLog(_ context.Context, p *mcp.LoggingMessageParams) {
	n, ok := gateway.ParseNotice(p)
	if !ok {
		return
	}
	g.mu.Lock()
	var call *Call
	for _, c := range g.inflight {
		if n.ProgressToken != "" {
			// Named: that call or, if it is not in flight, none.
			if gateway.CallToken(c.Invocation, c.CallID) == n.ProgressToken && c.Tool == n.ToolName() {
				call = c
				break
			}
		} else if c.Tool == n.ToolName() && c.ApprovalID == "" {
			call = c
			break
		}
	}
	f := journal.Fields{"TOOL": n.ToolName(), "SERVER": n.Server, "APPROVAL_ID": n.ApprovalID}
	if call != nil {
		call.ApprovalID, call.Approval, call.State = n.ApprovalID, "pending", StateWaiting
		f["SESSION"], f["INVOCATION"], f["CALL_ID"] = call.Session, call.Invocation, call.CallID
		if in := g.inv[call.Invocation]; in != nil {
			f["USER"] = in.user
			call.Preceding = preceding(in.lastResults(call.CallID), call.SuspectedInjection)
		}
		if len(call.SuspectedInjection) > 0 {
			f["SUSPECTED_INJECTION"] = suspicionText(call.SuspectedInjection)
		}
		g.publish(Event{Kind: EvApprovalPending, Session: call.Session, Invocation: call.Invocation, CallID: call.CallID, Tool: call.Tool, ApprovalID: n.ApprovalID, ApprovalURL: g.ApprovalURL(n.ApprovalID)})
	}
	g.mu.Unlock()
	g.log.Log(journal.ApprovalPending, fmt.Sprintf("Waiting for approval %s of %s", n.ApprovalID, n.ToolName()), f)
}

func (g *Governor) toolFields(tctx agent.ToolContext, name string, args map[string]any) journal.Fields {
	srv, _, _ := strings.Cut(name, "__")
	f := journal.Fields{
		"SESSION": tctx.SessionID(), "INVOCATION": tctx.InvocationID(), "USER": tctx.UserID(),
		"TOOL": name, "CALL_ID": tctx.FunctionCallID(), "ARGS_SHA256": ArgsDigest(args),
	}
	if strings.Contains(name, "__") {
		f["SERVER"] = srv
	}
	return f
}

// ArgsDigest is the SHA-256 of the arguments' canonical JSON
// (encoding/json sorts map keys).
func ArgsDigest(args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

func copyFields(f journal.Fields) journal.Fields {
	c := journal.Fields{}
	for k, v := range f {
		c[k] = v
	}
	return c
}

// TraceArg is the argument a delegation call carries its trace in.
const TraceArg = "_trace"

// IsDelegation reports whether a gateway tool delegates to another agent
// (the agents server shipped with Custos, CONTRACT.md §9).
func IsDelegation(tool string) bool { return tool == "agents__delegate" }
