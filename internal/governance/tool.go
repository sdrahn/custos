package governance

// The tools' calls: the records of a call and its result, what is not
// offered to this agent, and the gateway's approval notices (CONTRACT.md
// §7, §9).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/tool"

	"github.com/sdrahn/custos/internal/gateway"
	"github.com/sdrahn/custos/internal/journal"
)

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
	if in := g.inv[tctx.InvocationID()]; in != nil {
		if err == nil {
			in.keepResult(call.CallID, call.Tool, result)
		} else {
			// An output that came back as an error was read by the model
			// all the same: the next call is checked against it (P4).
			in.keepResult(call.CallID, call.Tool, map[string]any{"error": err.Error()})
		}
	}
	now := g.now()
	f["DURATION_MS"] = ms(now.Sub(call.Since))
	errText := ""
	if err != nil {
		errText = err.Error()
	} else if e, ok := result["error"].(string); ok {
		errText = e
	}
	call.Ended, call.State, call.Reason = &now, "ok", reasonOf(errText)
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
		af["APPROVAL_ID"], af["OUTCOME"], af["REASON"] = call.ApprovalID, outcome, reasonOf(errText)
		g.mu.Unlock()
		g.log.Log(journal.ApprovalResolved, fmt.Sprintf("Approval %s for %s: %s", call.ApprovalID, call.Tool, outcome), af)
		f["APPROVAL_ID"] = call.ApprovalID
	} else {
		g.mu.Unlock()
	}
	f["OUTCOME"] = "ok"
	if errText != "" {
		f["OUTCOME"], f["REASON"] = "error", reasonOf(errText)
	}
	// An output that reads like instructions reaches the model with the
	// host's notice next to it (#155), also one that came back as an
	// error (P4); the journal names the signs.
	var shown map[string]any
	if t.Name() != ReportTool {
		given := result
		if err != nil {
			given = map[string]any{"error": err.Error()}
		}
		var signs []string
		if shown, signs = WithNotice(t.Name(), given); len(signs) > 0 {
			f["INJECTION_SIGNS"] = strings.Join(signs, "; ")
		}
	}
	g.log.Log(journal.ToolResult, "Tool result "+t.Name()+": "+f["OUTCOME"], f)
	return shown, nil
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
