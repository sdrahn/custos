// Package readiness measures a task category's promotion readiness
// (CONTRACT.md §6a): from the agent's journal records and the gateway's
// approval decisions, how often approvers overrode the category's
// proposals, how often approved calls went wrong, and how much was
// observed.
package readiness

import (
	"encoding/json"
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

// ParseRecords reads journalctl -o json lines.
func ParseRecords(b []byte) []Record {
	var out []Record
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		r := Record{}
		for k, v := range raw {
			if s, ok := v.(string); ok {
				r[k] = s
			}
		}
		out = append(out, r)
	}
	return out
}

// gatewayRecord is a line of the gateway's audit log (the MESSAGE of its
// journal records).
type gatewayRecord struct {
	Audit         bool   `json:"audit"`
	Event         string `json:"event"`
	ID            string `json:"id"`
	OK            bool   `json:"ok"`
	By            string `json:"by"`
	Principal     string `json:"principal"`
	Sub           string `json:"sub"`
	Server        string `json:"server"`
	Name          string `json:"name"`
	Target        string `json:"target"`
	Outcome       string `json:"outcome"`
	Error         string `json:"error"`
	ApprovalID    string `json:"approval_id"`
	Grant         string `json:"grant"`
	DecisionID    string `json:"decision_id"`
	ProgressToken string `json:"progress_token"`
	Action        string `json:"action"`
	Effect        string `json:"effect"`
	Reason        string `json:"reason"`
	OverRate      string `json:"over_rate"`
}

// ParseDecisions reads the gateway's journal (journalctl -o json -u
// mcp-gateway.service) for one principal's mcp-approval records: its
// MESSAGE is a JSON line of the gateway's log, with event, id, ok, by.
// The outcome of an approved call comes from its mcp-result.
func ParseDecisions(b []byte, principal string) map[string]Decision {
	out := map[string]Decision{}
	for _, r := range ParseRecords(b) {
		var msg gatewayRecord
		if json.Unmarshal([]byte(r["MESSAGE"]), &msg) != nil || msg.Event != "mcp-approval" || msg.ID == "" || msg.Principal != principal {
			continue
		}
		d := Decision{OK: msg.OK, By: msg.By, At: At(r)}
		if msg.Server != "" && msg.Target != "" {
			d.Tool = msg.Server + "__" + msg.Target
		}
		out[msg.ID] = d
	}
	for _, res := range ParseResults(b, principal) {
		if d, ok := out[res.ApprovalID]; ok && res.ApprovalID != "" {
			d.Outcome = res.Outcome
			out[res.ApprovalID] = d
		}
	}
	return out
}

// ParseResults reads the gateway's journal for one principal's
// mcp-result records (R2), oldest first. Only the gateway's audit
// records count ("audit": true, from mcp-gateway.service).
func ParseResults(b []byte, principal string) []Result {
	var out []Result
	for _, r := range ParseRecords(b) {
		var msg gatewayRecord
		if json.Unmarshal([]byte(r["MESSAGE"]), &msg) != nil || !msg.Audit || msg.Event != "mcp-result" || msg.Sub != principal || msg.Name == "" {
			continue
		}
		res := Result{Tool: msg.Server + "__" + msg.Name, Outcome: msg.Outcome, Error: msg.Error,
			ApprovalID: msg.ApprovalID, Grant: msg.Grant, DecisionID: msg.DecisionID, At: At(r)}
		if tok, ok := strings.CutPrefix(msg.ProgressToken, "custos:"); ok {
			res.Invocation, res.CallID, _ = strings.Cut(tok, "/")
		}
		out = append(out, res)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

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

// Audit is what the gateway's audit says about one agent: its approval
// decisions, the outcomes of its calls (R2) and its approvers' reviews
// of the calls that ran (R4).
type Audit struct {
	Decisions map[string]Decision
	Results   []Result
	Reviews   []Review
	RateCaps  []RateCap
}

// ParseAudit reads the gateway's journal for one principal.
func ParseAudit(b []byte, principal string) Audit {
	return Audit{Decisions: ParseDecisions(b, principal), Results: ParseResults(b, principal), Reviews: ParseReviews(b, principal),
		RateCaps: ParseRateCaps(b, principal)}
}

// RateLimitExceeded is the gateway's reason for a call it denied over a
// rate_limit obligation.
const RateLimitExceeded = "rate limit exceeded"

// RateCap is a tool call of the principal that reached a rate: over an
// approval_over_rate obligation (an envelope's rate, O12; the gateway's
// R6) it asked for an approval, and the decision record names the rate
// (over_rate); over a rate_limit it was denied.
type RateCap struct {
	Tool string // server__name
	At   time.Time
	// Rate is the rate reached (over_rate); "" for a rate_limit denial.
	Rate string
}

// ParseRateCaps reads the gateway's decision records of one principal's
// tool calls that reached a rate, oldest first. Only the gateway's audit
// records count.
func ParseRateCaps(b []byte, principal string) []RateCap {
	var out []RateCap
	for _, r := range ParseRecords(b) {
		var msg gatewayRecord
		if json.Unmarshal([]byte(r["MESSAGE"]), &msg) != nil || !msg.Audit || msg.Event != "" || msg.Sub != principal ||
			msg.Action != "tools.call" || msg.Name == "" {
			continue
		}
		if msg.OverRate == "" && (msg.Effect != "deny" || msg.Reason != RateLimitExceeded) {
			continue
		}
		out = append(out, RateCap{Tool: msg.Server + "__" + msg.Name, At: At(r), Rate: msg.OverRate})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// ParseReviews reads the gateway's mcp-review records of one principal's
// calls: the verdicts of its approvers (R4), a later one on the same item
// replacing an earlier one, oldest first.
func ParseReviews(b []byte, principal string) []Review {
	latest := map[string]Review{}
	for _, r := range ParseRecords(b) {
		var msg struct {
			Audit    bool   `json:"audit"`
			Event    string `json:"event"`
			ID       string `json:"id"`
			Sub      string `json:"sub"`
			Server   string `json:"server"`
			Name     string `json:"name"`
			Verdict  string `json:"verdict"`
			Reason   string `json:"reason"`
			By       string `json:"by"`
			Decision string `json:"decision_id"`
		}
		if json.Unmarshal([]byte(r["MESSAGE"]), &msg) != nil || !msg.Audit || msg.Event != "mcp-review" || msg.Sub != principal || msg.ID == "" {
			continue
		}
		rv := Review{Item: msg.ID, Tool: msg.Server + "__" + msg.Name, Decision: msg.Decision, Verdict: msg.Verdict, Reason: msg.Reason, By: msg.By, At: At(r)}
		if prev, ok := latest[msg.ID]; ok && rv.At.Before(prev.At) {
			continue
		}
		latest[msg.ID] = rv
	}
	out := make([]Review, 0, len(latest))
	for _, rv := range latest {
		out = append(out, rv)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
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

// ByRoot reports whether journald says agent-admin wrote the record as
// root (its trusted fields _UID and _COMM): agent-admin's records
// (SYSLOG_IDENTIFIER custos-promotion; custos-demote runs agent-admin
// evaluate) are root's. An agent writes its own records to the journal
// and may set any other field, an identifier and AGENT_EVENT included,
// but never a trusted one. _UID alone is not enough: an OpenShell
// agent's records reach the journal through its relay, which runs as
// root (security review H1).
func ByRoot(r Record) bool { return r["_UID"] == "0" && r["_COMM"] == RootWriter }

// RootWriter is the program whose root records count (_COMM).
const RootWriter = "agent-admin"

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

// Diagnostics is what an agent's diagnosis reports and the feedback on
// them show over the window (§4a, §6b): the trust evidence the
// diagnostics history gives a category enabled later. Shown with the
// readiness, not a threshold.
type Diagnostics struct {
	// Reports: activities with a diagnosis report (the last of each).
	Reports    int `json:"reports"`
	Hypotheses int `json:"hypotheses"`
	// Quotes and VerifiedQuotes: the reports' evidence, and how much of
	// it the host found in the tools' output.
	Quotes         int `json:"quotes"`
	VerifiedQuotes int `json:"verified_quotes"`
	// WithFeedback: reports a person gave feedback on (the last
	// feedback of each counts); the verdicts on their hypotheses, and on
	// their plans.
	WithFeedback   int `json:"with_feedback"`
	Confirmed      int `json:"confirmed"`
	Rejected       int `json:"rejected"`
	Unsure         int `json:"unsure"`
	PlansUseful    int `json:"plans_useful"`
	PlansNotUseful int `json:"plans_not_useful"`
	PlansExecuted  int `json:"plans_executed"`
	// ConfirmedRate = Confirmed / (Confirmed + Rejected).
	ConfirmedRate float64 `json:"confirmed_rate"`
}

// DiagnosticsOf measures the reports and feedback among the records.
// Feedback counts only from root's records (agent-admin feedback, _UID
// 0): an agent writes its own records to the journal and could write any
// identifier, never journald's trusted _UID.
func DiagnosticsOf(recs []Record) Diagnostics {
	type report struct {
		at                 time.Time
		hyps, quotes, good int
	}
	type feedback struct {
		at time.Time
		f  struct {
			Hypotheses map[string]string `json:"hypotheses"`
			Plan       string            `json:"plan"`
			Executed   bool              `json:"executed"`
		}
	}
	reports := map[string]report{}
	feedbacks := map[string]feedback{}
	for _, r := range recs {
		inv := r["AGENT_INVOCATION"]
		switch r["AGENT_EVENT"] {
		case "diagnosis.report":
			var rep struct {
				Hypotheses []json.RawMessage `json:"hypotheses"`
				Evidence   []struct {
					Verified bool `json:"verified"`
				} `json:"evidence"`
			}
			if inv == "" || json.Unmarshal([]byte(r["AGENT_REPORT"]), &rep) != nil {
				continue
			}
			if prev, ok := reports[inv]; ok && At(r).Before(prev.at) {
				continue
			}
			x := report{at: At(r), hyps: len(rep.Hypotheses), quotes: len(rep.Evidence)}
			for _, e := range rep.Evidence {
				if e.Verified {
					x.good++
				}
			}
			reports[inv] = x
		case "diagnosis.feedback":
			if inv == "" || !ByRoot(r) {
				continue
			}
			var fb feedback
			if json.Unmarshal([]byte(r["AGENT_FEEDBACK"]), &fb.f) != nil {
				continue
			}
			fb.at = At(r)
			if prev, ok := feedbacks[inv]; ok && fb.at.Before(prev.at) {
				continue
			}
			feedbacks[inv] = fb
		}
	}
	var d Diagnostics
	for inv, x := range reports {
		d.Reports++
		d.Hypotheses += x.hyps
		d.Quotes += x.quotes
		d.VerifiedQuotes += x.good
		fb, ok := feedbacks[inv]
		if !ok {
			continue
		}
		d.WithFeedback++
		for _, v := range fb.f.Hypotheses {
			switch v {
			case "confirmed":
				d.Confirmed++
			case "rejected":
				d.Rejected++
			case "unsure":
				d.Unsure++
			}
		}
		switch fb.f.Plan {
		case "useful":
			d.PlansUseful++
		case "not_useful":
			d.PlansNotUseful++
		}
		if fb.f.Executed {
			d.PlansExecuted++
		}
	}
	if n := d.Confirmed + d.Rejected; n > 0 {
		d.ConfirmedRate = float64(d.Confirmed) / float64(n)
	}
	return d
}

// Unmet says which of the diagnostics thresholds the history does not
// meet (§6b), in the words of Measures.Unmet.
func (d Diagnostics) Unmet(th Thresholds) []string {
	var out []string
	if d.WithFeedback < th.DiagnosticsFeedback {
		out = append(out, fmt.Sprintf("diagnosis reports with feedback %d of %d", d.WithFeedback, th.DiagnosticsFeedback))
	}
	if th.DiagnosticsConfirmedRate > 0 {
		switch {
		case d.Confirmed+d.Rejected == 0:
			out = append(out, "no hypothesis confirmed or rejected yet")
		case d.ConfirmedRate < th.DiagnosticsConfirmedRate:
			out = append(out, fmt.Sprintf("confirmed hypotheses %s below %s", pct(d.ConfirmedRate), pct(th.DiagnosticsConfirmedRate)))
		}
	}
	return out
}

// AgentsStopped is root's record of the emergency stop (agent-admin
// stop-all; §8, §10): an approved call whose activity it stopped is an
// incident. AgentsReleased is its end.
const (
	AgentsStopped  = "agents.stopped"
	AgentsReleased = "agents.released"
)

// PermissionsChanged is root's record of a change of an agent's
// permissions (agent-admin; §10), with the permissions before and after
// as JSON.
const PermissionsChanged = "permissions.changed"

// History is an agent's permissions now and their changes, oldest first:
// which permissions were in force at a time (O11).
type History struct {
	Current []map[string]any
	Changes []Change
}

// Change is one change of the permissions.
type Change struct {
	At       time.Time
	From, To []map[string]any
}

// Now is a history without changes: the current permissions at any time.
func Now(perms []map[string]any) History { return History{Current: perms} }

// NewHistory is the current permissions and the changes root recorded
// among recs (permissions.changed with _UID 0; ByRoot). An agent's own
// record of a change is none.
func NewHistory(current []map[string]any, recs []Record) History {
	h := History{Current: current}
	for _, r := range recs {
		if r["AGENT_EVENT"] != PermissionsChanged || !ByRoot(r) {
			continue
		}
		var c Change
		if json.Unmarshal([]byte(r["AGENT_PERMISSIONS_FROM"]), &c.From) != nil ||
			json.Unmarshal([]byte(r["AGENT_PERMISSIONS_TO"]), &c.To) != nil {
			continue
		}
		c.At = At(r)
		h.Changes = append(h.Changes, c)
	}
	sort.SliceStable(h.Changes, func(i, j int) bool { return h.Changes[i].At.Before(h.Changes[j].At) })
	return h
}

// At is the permissions in force at t: those after the last change
// before t; before the first change read, those it changed from; without
// changes, the current ones.
func (h History) At(t time.Time) []map[string]any {
	if len(h.Changes) == 0 {
		return h.Current
	}
	if t.Before(h.Changes[0].At) {
		return h.Changes[0].From
	}
	in := h.Changes[0].To
	for _, c := range h.Changes[1:] {
		if t.Before(c.At) {
			break
		}
		in = c.To
	}
	return in
}

// InCategory is InCategory with the permissions in force at t.
func (h History) InCategory(category, tool string, t time.Time) bool {
	return InCategory(category, tool, h.At(t))
}
