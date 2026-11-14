package readiness

// The review of a promoted category's autonomous actions (CONTRACT.md
// §6a): which ran without approval, the week's sample, the verdicts.

import (
	"sort"
	"strings"
	"time"
)

// Review verdicts (category.review, §6a, §10).
const (
	VerdictOK       = "ok"
	VerdictIncident = "incident"
)

// SampleSize: of a week's autonomous actions, all are offered for review
// up to this many, then this many (§6a).
const SampleSize = 20

// Action is an autonomous action: a call of a bounded category that ran
// without an approval.
type Action struct {
	Activity string    `json:"activity"`
	Call     string    `json:"call"`
	Tool     string    `json:"tool"`
	Outcome  string    `json:"outcome"`
	At       time.Time `json:"-"`
	Time     string    `json:"at"`
	Args     string    `json:"args_sha256,omitempty"`
	// Decision is the gateway's decision id of the call, when the gateway
	// recorded it (mcp-result): the gateway's review of it has the same.
	Decision string `json:"decision_id,omitempty"`
}

// key is the action's key in a reviews map (ReviewMap): its activity
// and call, or the gateway's decision id when the call named no activity.
func (a Action) key() string {
	if a.Activity != "" {
		return a.Activity + " " + a.Call
	}
	return "decision " + a.Decision
}

// ReviewMap is the reviews by action: root's by activity and call
// (Reviews), and the gateway's approvers' by the decision id of the call
// they reviewed (R4).
func ReviewMap(category string, perms History, recs []Record, gateway []Review) map[string]Review {
	out := Reviews(category, recs)
	for _, rv := range gateway {
		if rv.Decision != "" && perms.InCategory(category, rv.Tool, rv.At) {
			out["decision "+rv.Decision] = rv
		}
	}
	return out
}

// Reviewed reports whether the action has a verdict in the map: root's,
// or the gateway's approvers' on the same call.
func Reviewed(a Action, reviews map[string]Review) bool {
	if _, ok := reviews[a.key()]; ok {
		return true
	}
	_, ok := reviews["decision "+a.Decision]
	return ok && a.Decision != ""
}

// AutonomousActions are the category's calls since then that ran without
// an approval or a grant, oldest first: as the gateway recorded them
// (mcp-result, R2), each with its decision id, the activity and call
// from the progress token the agent sent; then those only the agent
// recorded (Autonomous). An agent can add actions to review this way,
// never hide one the gateway let through.
func AutonomousActions(category string, perms History, recs []Record, results []Result, since time.Time) []Action {
	args := map[string]string{}
	for _, r := range recs {
		if r["AGENT_EVENT"] == "tool.call" {
			args[r["AGENT_INVOCATION"]+" "+r["AGENT_CALL_ID"]] = r["AGENT_ARGS_SHA256"]
		}
	}
	var out []Action
	seen := map[string]bool{}
	for _, res := range results {
		if res.ApprovalID != "" || res.Grant != "" || res.At.Before(since) || !perms.InCategory(category, res.Tool, res.At) {
			continue
		}
		outcome := "ok"
		switch {
		case Failed(res.Outcome):
			outcome = "error"
		case res.Outcome == "cancelled":
			outcome = "cancelled"
		}
		a := Action{Activity: res.Invocation, Call: res.CallID, Tool: res.Tool, Outcome: outcome, At: res.At,
			Time: res.At.UTC().Format(time.RFC3339), Decision: res.DecisionID}
		if a.Activity != "" {
			a.Args = args[a.Activity+" "+a.Call]
			seen[a.Activity+" "+a.Call] = true
		}
		out = append(out, a)
	}
	for _, a := range Autonomous(category, perms, recs, since) {
		if !seen[a.Activity+" "+a.Call] {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Review is an approver's verdict on an autonomous action: root's
// (agent-admin review, by activity and call) or one of the gateway's
// approvers' (its review items, R4: by item, with the tool).
type Review struct {
	Activity, Call, Verdict, By, Reason string
	At                                  time.Time
	// Item, Tool and Decision: a review the gateway recorded (mcp-review),
	// with the decision id of the reviewed call.
	Item, Tool, Decision string
}

// AllReviews are the category's reviews: root's (Reviews) and the
// gateway's approvers' of calls of the category.
//
// A call reviewed by both (root's review names the gateway's decision id
// of the call) is one review: the later verdict.
func AllReviews(category string, perms History, recs []Record, gateway []Review) []Review {
	var out []Review
	byDecision := map[string]int{} // decision id → index in out
	add := func(rv Review) {
		if rv.Decision != "" {
			if i, ok := byDecision[rv.Decision]; ok {
				if rv.At.After(out[i].At) {
					out[i] = rv
				}
				return
			}
			byDecision[rv.Decision] = len(out)
		}
		out = append(out, rv)
	}
	for _, rv := range Reviews(category, recs) {
		add(rv)
	}
	for _, rv := range gateway {
		if perms.InCategory(category, rv.Tool, rv.At) {
			add(rv)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Autonomous are the category's calls since then that ran without an
// approval, oldest first.
func Autonomous(category string, perms History, recs []Record, since time.Time) []Action {
	asked := map[string]bool{}
	args := map[string]string{}
	for _, r := range recs {
		k := r["AGENT_INVOCATION"] + " " + r["AGENT_CALL_ID"]
		switch r["AGENT_EVENT"] {
		case "approval.pending":
			asked[k] = true
		case "tool.call":
			args[k] = r["AGENT_ARGS_SHA256"]
		}
	}
	var out []Action
	for _, r := range recs {
		k := r["AGENT_INVOCATION"] + " " + r["AGENT_CALL_ID"]
		if r["AGENT_EVENT"] != "tool.result" || r["AGENT_APPROVAL_ID"] != "" || asked[k] ||
			At(r).Before(since) || !perms.InCategory(category, r["AGENT_TOOL"], At(r)) {
			continue
		}
		out = append(out, Action{Activity: r["AGENT_INVOCATION"], Call: r["AGENT_CALL_ID"], Tool: r["AGENT_TOOL"],
			Outcome: r["AGENT_OUTCOME"], At: At(r), Time: At(r).UTC().Format(time.RFC3339), Args: args[k]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// ByRoot reports whether journald says root wrote the record (its
// trusted field _UID), and not the OpenShell relay: agent-admin's records
// (SYSLOG_IDENTIFIER custos-promotion; custos-demote runs agent-admin
// evaluate) are root's. An agent writes its own records to the journal
// and may set any other field, an identifier and AGENT_EVENT included,
// but never a trusted one. An OpenShell agent's records reach the
// journal through its relay, which runs as root (security review H1):
// the relay passes only the host's own events (relay.Bridge), and its
// records count as root's nowhere, by its trusted _COMM or _SYSTEMD_UNIT.
// journald reads those from /proc when a record arrives, so a short-lived
// agent-admin may have neither; the relay, which runs on, always has them.
func ByRoot(r Record) bool {
	return r["_UID"] == "0" && !strings.HasPrefix(r["_COMM"], RelayComm) && !strings.HasPrefix(r["_SYSTEMD_UNIT"], "agent-openshell@")
}

// RelayComm is the OpenShell relay's _COMM (its name, cut to 15
// characters by the kernel).
const RelayComm = "agent-openshell"

// Reviews are the category's review records, by activity and call; a
// later verdict on the same action replaces an earlier one. Only root's
// records count (ByRoot): a review an agent wrote itself is none.
func Reviews(category string, recs []Record) map[string]Review {
	out := map[string]Review{}
	for _, r := range recs {
		if r["AGENT_EVENT"] != "category.review" || r["AGENT_CATEGORY"] != category || !ByRoot(r) {
			continue
		}
		k := r["AGENT_INVOCATION"] + " " + r["AGENT_CALL_ID"]
		if prev, ok := out[k]; ok && At(r).Before(prev.At) {
			continue
		}
		out[k] = Review{Activity: r["AGENT_INVOCATION"], Call: r["AGENT_CALL_ID"], Verdict: r["AGENT_VERDICT"],
			By: r["AGENT_BY"], Reason: r["AGENT_REASON"], At: At(r), Decision: r["AGENT_DECISION_ID"]}
	}
	return out
}

// Pending is the sample still to review among the actions of the last
// week: all of them while the week has at most SampleSize, else the
// newest unreviewed ones up to SampleSize reviews in the week.
func Pending(actions []Action, reviews map[string]Review, now time.Time) []Action {
	week := now.Add(-7 * 24 * time.Hour)
	var recent, open []Action
	done := 0
	for _, a := range actions {
		if a.At.Before(week) {
			continue
		}
		recent = append(recent, a)
		if Reviewed(a, reviews) {
			done++
		} else {
			open = append(open, a)
		}
	}
	if len(recent) <= SampleSize {
		return open
	}
	n := SampleSize - done
	if n <= 0 {
		return nil
	}
	if n > len(open) {
		n = len(open)
	}
	return open[len(open)-n:]
}
