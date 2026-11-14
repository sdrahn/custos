package readiness

// Reading the records: the agent's journal, and the gateway's audit log
// (decisions, results, reviews, rate caps).

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

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
