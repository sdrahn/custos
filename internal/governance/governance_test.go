package governance

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gateway"
	"github.com/sdrahn/custos/internal/journal"
)

func TestArgsDigest(t *testing.T) {
	a := ArgsDigest(map[string]any{"b": 1, "a": []any{"x"}})
	b := ArgsDigest(map[string]any{"a": []any{"x"}, "b": 1})
	if a != b || len(a) != 64 {
		t.Errorf("digest not canonical: %s %s", a, b)
	}
	if a == ArgsDigest(map[string]any{"a": []any{"y"}, "b": 1}) {
		t.Error("different arguments, same digest")
	}
}

func TestIsDelegation(t *testing.T) {
	if !IsDelegation("agents__delegate") || IsDelegation("agents__other") || IsDelegation("x__delegate") {
		t.Error("IsDelegation")
	}
}

func newGov() (*Governor, *journal.Memory) {
	cfg, _ := config.Parse([]byte("version: 1\nname: pp\nmodel: {provider: vertex, name: m}\ngateway: {servers: [zypp]}\n"))
	log := &journal.Memory{}
	return New(cfg, log), log
}

// A notice that matches no call in flight is still journaled (the
// approval exists at the gateway), but published to nobody.
func TestNoticeWithoutCall(t *testing.T) {
	g, log := newGov()
	ch, cancel := g.Subscribe("s1")
	defer cancel()
	g.OnGatewayLog(context.Background(), &mcp.LoggingMessageParams{Logger: "mcp-gateway", Level: "notice",
		Data: "Waiting for approval of zypp/install on the mcp-gateway approvals page (id a-1)."})
	r := log.Events(journal.ApprovalPending)
	if len(r) != 1 || r[0].Fields["APPROVAL_ID"] != "a-1" || r[0].Fields["SESSION"] != "" {
		t.Errorf("records %+v", r)
	}
	select {
	case e := <-ch:
		t.Errorf("published %+v", e)
	default:
	}
	// Other logging messages are ignored.
	g.OnGatewayLog(context.Background(), &mcp.LoggingMessageParams{Logger: "zypp", Data: "hello"})
	if len(log.Records) != 1 {
		t.Errorf("records %d", len(log.Records))
	}
}

func TestPauseResumeCancel(t *testing.T) {
	g, log := newGov()
	g.Pause("alice")
	if !g.Paused() {
		t.Fatal("not paused")
	}
	done := make(chan error)
	go func() { done <- g.hold(context.Background()) }()
	select {
	case <-done:
		t.Fatal("hold returned while paused")
	default:
	}
	g.Resume("alice")
	if err := <-done; err != nil || g.Paused() {
		t.Errorf("after resume: %v %v", err, g.Paused())
	}
	ctx, c := context.WithCancel(context.Background())
	g.Pause("bob")
	c()
	if err := g.hold(ctx); err == nil {
		t.Error("hold ignores its context")
	}
	if g.Cancel("nope", "bob") {
		t.Error("cancelled an unknown activity")
	}
	if len(log.Events(journal.AgentPaused)) != 2 || len(log.Events(journal.AgentResumed)) != 1 {
		t.Error("pause/resume not journaled")
	}
}

// O2: with two calls of one tool in flight, a notice that names its call
// (the progress token in _meta) goes to that call, whatever the order; a
// notice that names only the tool goes to the oldest call without one; a
// named call that is not in flight gets nothing.
func TestNoticeNamesCall(t *testing.T) {
	g, log := newGov()
	c1 := &Call{Session: "s1", Invocation: "e-1", CallID: "c1", Tool: "zypp__install", State: StateRunning}
	c2 := &Call{Session: "s1", Invocation: "e-1", CallID: "c2", Tool: "zypp__install", State: StateRunning}
	c3 := &Call{Session: "s1", Invocation: "e-1", CallID: "c3", Tool: "zypp__install", State: StateRunning}
	g.inflight = append(g.inflight, c1, c2, c3)
	notice := func(id, token string) {
		p := &mcp.LoggingMessageParams{Logger: "mcp-gateway", Level: "notice",
			Data: "Waiting for approval of zypp/install on the mcp-gateway approvals page (id " + id + ")."}
		if token != "" {
			p.Meta = mcp.Meta{gateway.MetaApproval: map[string]any{"id": id, "progressToken": token}}
		}
		g.OnGatewayLog(context.Background(), p)
	}
	notice("a-2", gateway.CallToken("e-1", "c2"))
	notice("a-9", gateway.CallToken("e-1", "gone"))
	notice("a-1", "")
	if c1.ApprovalID != "a-1" || c2.ApprovalID != "a-2" || c3.ApprovalID != "" {
		t.Errorf("assigned: c1 %q, c2 %q, c3 %q", c1.ApprovalID, c2.ApprovalID, c3.ApprovalID)
	}
	want := map[string]string{"a-2": "c2", "a-9": "", "a-1": "c1"}
	for _, r := range log.Events(journal.ApprovalPending) {
		if id := r.Fields["APPROVAL_ID"]; r.Fields["CALL_ID"] != want[id] {
			t.Errorf("journal: %s for call %q, want %q", id, r.Fields["CALL_ID"], want[id])
		}
	}
}

func TestClipBytes(t *testing.T) {
	if got := clipBytes("short", 10); got != "short" {
		t.Errorf("%q", got)
	}
	// "ä" is two bytes: a cut inside it backs off to before it.
	if got := clipBytes("aaää", 3); got != "aa…" {
		t.Errorf("%q", got)
	}
	if got := clipBytes("aaää", 4); got != "aaä…" {
		t.Errorf("%q", got)
	}
}

// slowLog is a journal whose write takes a while, as journald's can.
type slowLog struct{ *journal.Memory }

func (s slowLog) Log(event, message string, f journal.Fields) {
	time.Sleep(50 * time.Millisecond)
	s.Memory.Log(event, message, f)
}

// Whoever sees an approval's pending event finds its journal record: the
// record is written before the event is published, however slow the
// journal (a stream that showed the event and then read the journal
// found nothing, now and then).
func TestJournalBeforeEvent(t *testing.T) {
	cfg, _ := config.Parse([]byte("version: 1\nname: pp\nmodel: {provider: vertex, name: m}\ngateway: {servers: [zypp]}\n"))
	mem := &journal.Memory{}
	g := New(cfg, slowLog{mem})
	g.inflight = append(g.inflight, &Call{Session: "s1", Invocation: "e-1", CallID: "c1", Tool: "zypp__install", State: StateRunning})
	ch, cancel := g.Subscribe("s1")
	defer cancel()
	go g.OnGatewayLog(context.Background(), &mcp.LoggingMessageParams{Logger: "mcp-gateway", Level: "notice",
		Data: "Waiting for approval of zypp/install on the mcp-gateway approvals page (id a-1)."})
	select {
	case e := <-ch:
		if e.Kind != EvApprovalPending {
			t.Fatalf("event %+v", e)
		}
		if r := mem.Events(journal.ApprovalPending); len(r) != 1 || r[0].Fields["APPROVAL_ID"] != "a-1" {
			t.Errorf("the event came before its journal record: records %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
}
