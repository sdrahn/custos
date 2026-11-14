// Package readiness measures a task category's promotion readiness
// (CONTRACT.md §6a): from the agent's journal records and the gateway's
// approval decisions, how often approvers overrode the category's
// proposals, how often approved calls went wrong, and how much was
// observed.
package readiness

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Thresholds a category must meet to be promoted without
// --despite-readiness (/etc/custos/promotion.yaml).
type Thresholds struct {
	// ObservationDays: since the category's first decided proposal.
	ObservationDays int `yaml:"observation_days" json:"observation_days"`
	// Proposals: decided approval items of the category.
	Proposals int `yaml:"proposals" json:"proposals"`
	// OverrideRate and IncidentRate are fractions (0.05 is 5 %).
	OverrideRate float64 `yaml:"override_rate" json:"override_rate"`
	IncidentRate float64 `yaml:"incident_rate" json:"incident_rate"`
	// WindowDays is how far back the records are read.
	WindowDays int `yaml:"window_days" json:"window_days"`
	// On a host that started diagnostics-only (§6b), its diagnostics
	// history is part of a category's readiness: DiagnosticsFeedback is
	// the reports with feedback at least, DiagnosticsConfirmedRate the
	// share of confirmed hypotheses (of those confirmed or rejected) at
	// least. 0 turns either off.
	DiagnosticsFeedback      int     `yaml:"diagnostics_feedback" json:"diagnostics_feedback"`
	DiagnosticsConfirmedRate float64 `yaml:"diagnostics_confirmed_rate" json:"diagnostics_confirmed_rate"`
}

// Defaults are §6a's and §6b's.
var Defaults = Thresholds{ObservationDays: 14, Proposals: 50, OverrideRate: 0.05, IncidentRate: 0.02, WindowDays: 30,
	DiagnosticsFeedback: 10, DiagnosticsConfirmedRate: 0.7}

// Record is one journal record of the agent (journalctl -o json):
// AGENT_* fields and __REALTIME_TIMESTAMP.
type Record map[string]string

// Decision is the gateway's mcp-approval record for one approval id,
// and, from a gateway that audits each call's outcome (its mcp-result,
// requirement R2), the outcome of the approved call ("" before it ended,
// or from a gateway without them).
type Decision struct {
	OK      bool
	By      string
	At      time.Time
	Outcome string
	// Tool is the call's, as the gateway names it (server__target): the
	// category comes from it, never from the agent's records (security
	// review M6).
	Tool string
}

// Result is the gateway's mcp-result record of a call it forwarded: what
// the gateway saw come back, whatever the agent records (R2).
type Result struct {
	Tool       string // server__name
	Outcome    string // ok, tool_error, error, cancelled, timeout
	Error      string // a short text, where the gateway's policy lets it log one
	ApprovalID string
	Grant      string
	DecisionID string // the gateway's decision that let the call through
	// Invocation and CallID are from the call's progress token
	// (custos:<invocation>/<call id>): the agent's, for correlation only.
	Invocation, CallID string
	At                 time.Time
}

// Failed reports whether a gateway outcome is a failure of the call: a
// tool error, an error, or a timeout (a cancelled call is no failure).
func Failed(outcome string) bool {
	return outcome == "tool_error" || outcome == "error" || outcome == "timeout"
}

// Measures are one category's readiness (§6a).
type Measures struct {
	// Proposals: approval items of the category the gateway's approvers
	// decided (approved or declined); expired ones have no decision.
	Proposals int `json:"proposals"`
	Approved  int `json:"approved"`
	Declined  int `json:"declined"`
	// Cancelled: approved, then the activity was cancelled before the
	// call ran.
	Cancelled int `json:"cancelled"`
	// Reviewed: autonomous actions of the category an approver reviewed
	// afterwards (category.review); ReviewOverrides: of those, the ones
	// that should not have run.
	Reviewed        int `json:"reviewed"`
	ReviewOverrides int `json:"review_overrides"`
	// Overrides = Declined + Cancelled + ReviewOverrides; OverrideRate =
	// Overrides / (Proposals + Reviewed).
	Overrides    int     `json:"overrides"`
	OverrideRate float64 `json:"override_rate"`
	// Incidents among the approved calls: the tool failed, a limit was
	// reached in its activity, or the agent was paused during it.
	// IncidentRate = Incidents / Approved.
	Incidents    int     `json:"incidents"`
	IncidentRate float64 `json:"incident_rate"`
	// Diversity: distinct tools, and distinct calls (tool and arguments'
	// digest), among the proposals.
	Tools int `json:"tools"`
	Calls int `json:"distinct_calls"`
	// ObservedDays: from the first decided proposal to now.
	ObservedDays float64   `json:"observed_days"`
	First        time.Time `json:"-"`
	// FirstProposal is First as RFC 3339, when there is one.
	FirstProposal string `json:"first_proposal,omitempty"`
	// Ready: every threshold met; Unmet says which are not.
	Ready bool     `json:"ready"`
	Unmet []string `json:"unmet,omitempty"`
}

// Measure computes a category's readiness. perms are the agent's
// permissions (the gateway data) and their changes; a call belongs to the
// category when a permission of the category that is not read-only
// matches its tool, among the permissions in force at the call's time.
func Measure(category string, perms History, recs []Record, gw Audit, th Thresholds, now time.Time) Measures {
	decisions := gw.Decisions
	type key struct{ inv, call string }
	calls := map[key]Record{}
	results := map[key]Record{}
	pending := map[string]Record{}      // approval id → the agent's approval.pending
	cancels := map[string][]time.Time{} // invocation → cancel times
	limits := map[string]bool{}         // invocations with a limit reached
	ends := map[string]time.Time{}      // invocation → its end
	var pauses, stops []time.Time
	for _, r := range recs {
		k := key{r["AGENT_INVOCATION"], r["AGENT_CALL_ID"]}
		switch r["AGENT_EVENT"] {
		case "tool.call":
			calls[k] = r
		case "tool.result":
			results[k] = r
		case "approval.pending":
			if r["AGENT_APPROVAL_ID"] != "" {
				pending[r["AGENT_APPROVAL_ID"]] = r
			}
		case "activity.cancel":
			cancels[r["AGENT_INVOCATION"]] = append(cancels[r["AGENT_INVOCATION"]], At(r))
		case "limit.exceeded":
			limits[r["AGENT_INVOCATION"]] = true
		case "invocation.end":
			ends[r["AGENT_INVOCATION"]] = At(r)
		case "agent.paused":
			pauses = append(pauses, At(r))
		case AgentsStopped:
			if ByRoot(r) {
				stops = append(stops, At(r))
			}
		}
	}
	// The proposals are the gateway's decisions, its records alone: their
	// tool (server and target in mcp-approval) puts them in a category,
	// and a decline counts whatever the agent recorded (security review
	// M6). The agent's own records add what only it knows, matched by the
	// approval id: the call's arguments, its result, a cancel, a limit, a
	// pause. An undecided proposal (expired, still pending) is none.
	var m Measures
	tools, distinct := map[string]bool{}, map[string]bool{}
	ids := make([]string, 0, len(decisions))
	for id := range decisions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		d := decisions[id]
		p, known := pending[id]
		tool, at := d.Tool, d.At
		if known {
			at = At(p)
		}
		if tool == "" && known { // a gateway that names no tool
			tool = p["AGENT_TOOL"]
		}
		if tool == "" || !perms.InCategory(category, tool, at) {
			continue
		}
		k := key{p["AGENT_INVOCATION"], p["AGENT_CALL_ID"]}
		m.Proposals++
		if m.First.IsZero() || at.Before(m.First) {
			m.First = at
		}
		tools[tool] = true
		distinct[tool+" "+calls[k]["AGENT_ARGS_SHA256"]] = true
		if !d.OK {
			m.Declined++
			continue
		}
		m.Approved++
		if !known {
			// Only what the gateway saw: a failed call is an incident.
			if Failed(d.Outcome) {
				m.Incidents++
			} else if d.Outcome == "cancelled" {
				m.Cancelled++
			}
			continue
		}
		res, ran := results[k]
		if d.Outcome == "cancelled" || cancelledBefore(cancels[k.inv], d.At, res, ran) {
			m.Cancelled++
			continue
		}
		end, ended := ends[k.inv]
		if !ended {
			end = now
		}
		switch {
		case Failed(d.Outcome): // what the gateway saw: the agent cannot hide it
			m.Incidents++
		case ran && res["AGENT_OUTCOME"] == "error":
			m.Incidents++
		case limits[k.inv]:
			m.Incidents++
		case pausedBetween(pauses, at, end):
			m.Incidents++
		case pausedBetween(stops, at, end): // the emergency stop (O11)
			m.Incidents++
		}
	}
	for _, rv := range AllReviews(category, perms, recs, gw.Reviews) {
		m.Reviewed++
		if rv.Verdict == VerdictIncident {
			m.ReviewOverrides++
		}
	}
	m.Overrides = m.Declined + m.Cancelled + m.ReviewOverrides
	if m.Proposals+m.Reviewed > 0 {
		m.OverrideRate = float64(m.Overrides) / float64(m.Proposals+m.Reviewed)
	}
	if m.Approved-m.Cancelled > 0 {
		m.IncidentRate = float64(m.Incidents) / float64(m.Approved-m.Cancelled)
	}
	m.Tools, m.Calls = len(tools), len(distinct)
	if !m.First.IsZero() {
		m.ObservedDays = now.Sub(m.First).Hours() / 24
		m.FirstProposal = m.First.UTC().Format(time.RFC3339)
	}
	if m.ObservedDays < float64(th.ObservationDays) {
		m.Unmet = append(m.Unmet, fmt.Sprintf("observed %.1f days of %d", m.ObservedDays, th.ObservationDays))
	}
	if m.Proposals < th.Proposals {
		m.Unmet = append(m.Unmet, fmt.Sprintf("%d proposals of %d", m.Proposals, th.Proposals))
	}
	if m.OverrideRate > th.OverrideRate {
		m.Unmet = append(m.Unmet, fmt.Sprintf("override rate %s above %s", pct(m.OverrideRate), pct(th.OverrideRate)))
	}
	if m.IncidentRate > th.IncidentRate {
		m.Unmet = append(m.Unmet, fmt.Sprintf("incident rate %s above %s", pct(m.IncidentRate), pct(th.IncidentRate)))
	}
	m.Ready = len(m.Unmet) == 0
	return m
}

// cancelledBefore: the activity was cancelled after the approval and
// before the call had a result.
func cancelledBefore(cancels []time.Time, approved time.Time, res Record, ran bool) bool {
	for _, c := range cancels {
		if !c.Before(approved) && (!ran || c.Before(At(res))) {
			return true
		}
	}
	return false
}

func pausedBetween(pauses []time.Time, from, to time.Time) bool {
	for _, p := range pauses {
		if !p.Before(from) && !p.After(to) {
			return true
		}
	}
	return false
}

func pct(f float64) string { return strconv.FormatFloat(f*100, 'f', 1, 64) + " %" }

// InCategory: a permission of the category that changes the system
// matches the tool (server__name; globs as the gateway's).
func InCategory(category, tool string, perms []map[string]any) bool {
	srv, name, ok := strings.Cut(tool, "__")
	if !ok {
		return false
	}
	for _, p := range perms {
		if p["category"] != category || p["read_only"] == true {
			continue
		}
		ps, _ := p["server"].(string)
		pt, _ := p["tool"].(string)
		if pt == "" {
			pt = "*"
		}
		if okS, _ := path.Match(ps, srv); !okS {
			continue
		}
		if okT, _ := path.Match(pt, name); okT {
			return true
		}
	}
	return false
}

// Categories are the categories named by perms, sorted.
func Categories(perms []map[string]any) []string {
	set := map[string]bool{}
	for _, p := range perms {
		if c, ok := p["category"].(string); ok {
			set[c] = true
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// At is a record's time (__REALTIME_TIMESTAMP, microseconds).
func At(r Record) time.Time {
	us, err := strconv.ParseInt(r["__REALTIME_TIMESTAMP"], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMicro(us)
}
