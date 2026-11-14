package promotion

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/category"
	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/readiness"
)

// ReviewWindow: the review looks at the last week's autonomous actions
// (§6a).
const ReviewWindow = 7 * 24 * time.Hour

// Queue is agent-admin review's answer (§17): per promoted category, the
// sample of its autonomous actions still to review.
type Queue struct {
	Agent      string                   `json:"agent"`
	Categories map[string]CategoryQueue `json:"categories"`
}

// CategoryQueue is one category's review queue.
type CategoryQueue struct {
	Mode string `json:"mode"`
	// Actions: autonomous actions in the last week since the promotion;
	// Reviewed: of those, the ones with a verdict.
	Actions  int                `json:"actions"`
	Reviewed int                `json:"reviewed"`
	Pending  []readiness.Action `json:"pending"`
}

// reviewData reads what a review needs for an agent: its gateway data,
// its records and the gateway's audit of it.
func reviewData(a *admin.Admin, name string) (*admin.GatewayData, []readiness.Record, readiness.Audit, time.Time, error) {
	if err := host.CheckName(name); err != nil {
		return nil, nil, readiness.Audit{}, time.Time{}, err
	}
	d, err := a.ReadGateway(name)
	if err != nil {
		return nil, nil, readiness.Audit{}, time.Time{}, err
	}
	now := a.Now()
	recs, gw, err := journalRecords(a, name, now.Add(-ReviewWindow))
	return d, recs, gw, now, err
}

// ReviewQueue lists the sample to review of the agent's promoted
// categories (bounded, or demoted since a promotion), or of one.
func ReviewQueue(a *admin.Admin, name string, only ...string) (*Queue, error) {
	d, recs, gw, now, err := reviewData(a, name)
	if err != nil {
		return nil, err
	}
	q := &Queue{Agent: name, Categories: map[string]CategoryQueue{}}
	for c, st := range d.Categories {
		if st == nil || st.PromotedBy == "" || (len(only) > 0 && !contains(only, c)) {
			continue
		}
		perms := readiness.NewHistory(d.Permissions, recs)
		acts := readiness.AutonomousActions(c, perms, recs, gw.Results, promotedAt(st))
		reviews := readiness.ReviewMap(c, perms, recs, gw.Reviews)
		cq := CategoryQueue{Mode: st.Mode, Pending: readiness.Pending(acts, reviews, now)}
		if cq.Pending == nil {
			cq.Pending = []readiness.Action{}
		}
		for _, x := range acts {
			if x.At.Before(now.Add(-ReviewWindow)) {
				continue
			}
			cq.Actions++
			if readiness.Reviewed(x, reviews) {
				cq.Reviewed++
			}
		}
		q.Categories[c] = cq
	}
	return q, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Review records an approver's verdict on an autonomous action of a
// category (§6a): ok, or incident (it should not have run), which needs
// a reason and demotes a bounded category at once.
func Review(a *admin.Admin, name, cat, activity, call, verdict, by, reason string) error {
	switch verdict {
	case readiness.VerdictOK:
	case readiness.VerdictIncident:
		if strings.TrimSpace(reason) == "" {
			return errors.New("an incident needs --reason TEXT: why it should not have run")
		}
	default:
		return fmt.Errorf("verdict %q: want ok or incident", verdict)
	}
	d, recs, gw, _, err := reviewData(a, name)
	if err != nil {
		return err
	}
	st := d.Categories[cat]
	if st == nil || st.PromotedBy == "" {
		return fmt.Errorf("agent %s: category %s has not been promoted: its calls all had approvals", name, cat)
	}
	var act *readiness.Action
	for _, x := range readiness.AutonomousActions(cat, readiness.NewHistory(d.Permissions, recs), recs, gw.Results, promotedAt(st)) {
		if activity != "" && x.Activity == activity && x.Call == call {
			x := x
			act = &x
		}
	}
	if act == nil {
		return fmt.Errorf("agent %s: no action %s of activity %s in category %s ran without an approval in the last week (agent-admin review %s %s lists them; "+
			"one listed without an activity is reviewed on the gateway: Cockpit's Approvals tab, mcp-gateway-admin reviews)",
			name, call, activity, cat, name, cat)
	}
	a.Record(journal.CategoryReview, name, fmt.Sprintf("%s reviewed %s of agent %s (%s in %s): %s", by, cat, name, act.Tool, activity, verdict), journal.Fields{
		"CATEGORY": cat, "PROMOTION": st.Promotion, "INVOCATION": activity, "CALL_ID": call, "TOOL": act.Tool,
		"VERDICT": verdict, "BY": by, "REASON": reason, "DECISION_ID": act.Decision,
	})
	fmt.Fprintf(a.Out, "agent %s: %s in activity %s reviewed: %s\n", name, act.Tool, activity, verdict)
	if verdict == readiness.VerdictIncident && st.Mode == "bounded" {
		return Demote(a, name, cat, by, fmt.Sprintf("review: %s in activity %s should not have run: %s", act.Tool, activity, reason))
	}
	return nil
}

// reviewIncident is a review since the promotion that found an action
// should not have run, for the evaluator; "" if none.
func reviewIncident(cat string, st *category.State, perms readiness.History, recs []readiness.Record, gateway []readiness.Review) string {
	var found []readiness.Review
	for _, rv := range readiness.AllReviews(cat, perms, recs, gateway) {
		if rv.Verdict == readiness.VerdictIncident && !rv.At.Before(promotedAt(st)) {
			found = append(found, rv)
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool { return found[i].At.Before(found[j].At) })
	rv := found[0]
	if rv.Item != "" {
		return fmt.Sprintf("review by %s on the gateway: %s (review %s) should not have run: %s", rv.By, rv.Tool, rv.Item, rv.Reason)
	}
	return fmt.Sprintf("review by %s: the action in activity %s should not have run: %s", rv.By, rv.Activity, rv.Reason)
}
