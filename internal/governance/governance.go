// Package governance is the ADK plugin every agent loads (CONTRACT.md
// §8): local limits, audit records (§9), and the gateway's approval
// notices (§7) passed on to the journal and to AG-UI streams (§11).
//
// It runs inside the agent's process. Its limits keep an honest agent
// within budget; they are not a security boundary (§1).
package governance

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/plugin"
	"google.golang.org/adk/tool"

	"github.com/sdrahn/custos/internal/config"
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
