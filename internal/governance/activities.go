package governance

// The activities a governor knows: the running ones and the last ones
// finished, cancelling one, and holding the agent (CONTRACT.md §8, §11).

import (
	"context"
	"sort"

	"github.com/sdrahn/custos/internal/journal"
)

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
