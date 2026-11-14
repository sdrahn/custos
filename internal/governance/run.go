package governance

// An invocation from its start to its end: the records of both, and the
// local limits (CONTRACT.md §8, §9).

import (
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/adk/agent"
	"google.golang.org/genai"

	"github.com/sdrahn/custos/internal/journal"
)

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
