package admin

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/sdrahn/custos/internal/readiness"
)

// DemoteBy is the demotion evaluator's name in the records (§10,
// AGENT_BY) and in demoted_by.
const DemoteBy = "custos-demote"

// OverrideWindow: the evaluator measures a bounded category's override
// rate over the last 7 days since its promotion (§6a).
const OverrideWindow = 7 * 24 * time.Hour

// RateCapDemotion: a bounded category whose rate the gateway capped this
// often within RateCapWindow since its promotion is demoted (§6a, O12).
const (
	RateCapDemotion = 3
	RateCapWindow   = 24 * time.Hour
)

// HealthCheckTimeout bounds a rollback health check.
const HealthCheckTimeout = 60 * time.Second

// Demotion is one category the evaluator demoted, and why; Notice says
// how its approvers were told (R3).
type Demotion struct {
	Agent, Category, Reason string
	Notice                  string
}

// noticeOf tells the approvers of the agent's calls of an automatic
// demotion through the gateway's notices (its requirement R3), and says
// how that went. A gateway without notices is no error: Cockpit's
// Approvals tab shows the demotion all the same; neither is a refused
// notice an error of the demotion, which stands.
func (a *Admin) noticeOf(name, c, reason string, d *GatewayData) string {
	if a.Notify == nil {
		return ""
	}
	id, err := a.Notify(demotionNotice(name, c, reason, d))
	switch {
	case errors.Is(err, errNoNotices):
		return "no notice: this mcp-gateway takes none"
	case err != nil:
		return "no notice: " + err.Error()
	}
	return "approvers notified (notice " + id + ")"
}

// Evaluate is the demotion evaluator (agent-admin evaluate, run by
// custos-demote.timer as root, outside any agent; §6a). For each bounded
// category of each agent it demotes when, since the promotion:
//   - the override rate over the last 7 days exceeds the threshold;
//   - the gateway capped the envelope's rate RateCapDemotion times within
//     a day;
//   - a rollback condition of the envelope fires: tool_error or
//     limit_exceeded for a call of the category made without an
//     approval, or health_check_failed.
//
// An agent whose records cannot be read is reported and the others are
// still evaluated.
func (a *Admin) Evaluate() ([]Demotion, error) {
	p, err := a.Show()
	if err != nil {
		return nil, err
	}
	th, err := a.Thresholds()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(p.Agents))
	for n := range p.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Demotion
	var errs []string
	for _, name := range names {
		d := p.Agents[name]
		var cats []string
		for c, st := range d.Categories {
			if st != nil && st.Mode == "bounded" {
				cats = append(cats, c)
			}
		}
		if len(cats) == 0 {
			continue
		}
		sort.Strings(cats)
		now := a.Now()
		recs, gw, err := a.journalRecords(name, now.Add(-OverrideWindow))
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, c := range cats {
			reason := a.demotionReason(d, c, recs, gw, th, now)
			if reason == "" {
				continue
			}
			if err := a.Demote(name, c, DemoteBy, reason); err != nil {
				errs = append(errs, fmt.Sprintf("agent %s: demoting %s: %v", name, c, err))
				continue
			}
			out = append(out, Demotion{name, c, reason, a.noticeOf(name, c, reason, d)})
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return out, nil
}

// demotionReason is why category c of agent d is to be demoted, or "".
func (a *Admin) demotionReason(d *GatewayData, c string, recs []readiness.Record, gw readiness.Audit, th readiness.Thresholds, now time.Time) string {
	st := d.Categories[c]
	since := promotedAt(st)
	if from := now.Add(-OverrideWindow); since.Before(from) {
		since = from
	}
	var after []readiness.Record
	for _, r := range recs {
		if !readiness.At(r).Before(since) {
			after = append(after, r)
		}
	}
	// The gateway's records since then too.
	var reviews []readiness.Review
	for _, rv := range gw.Reviews {
		if !rv.At.Before(since) {
			reviews = append(reviews, rv)
		}
	}
	gw.Reviews = reviews
	// The decisions since then: they are the proposals now (review M6).
	decisions := map[string]readiness.Decision{}
	for id, dc := range gw.Decisions {
		if !dc.At.Before(since) {
			decisions[id] = dc
		}
	}
	gw.Decisions = decisions
	// The permissions in force at each call's time (O11), from root's
	// records of their changes.
	perms := readiness.NewHistory(d.Permissions, recs)
	if why := reviewIncident(c, st, perms, after, gw.Reviews); why != "" {
		return why
	}
	// Outside its windows a bounded category is supervised, so it still
	// has proposals to decline.
	m := readiness.Measure(c, perms, after, gw, th, now)
	if m.Proposals > 0 && m.OverrideRate > th.OverrideRate {
		return fmt.Sprintf("override rate %.1f %% above %.1f %% since %s (%d of %d proposals)",
			m.OverrideRate*100, th.OverrideRate*100, since.UTC().Format(time.RFC3339), m.Overrides, m.Proposals)
	}
	if why := rateCapped(c, st, perms, gw.RateCaps, promotedAt(st), now); why != "" {
		return why
	}
	if st.Envelope == nil || st.Envelope.Rollback == nil {
		return ""
	}
	rb := st.Envelope.Rollback
	on := func(cond string) bool {
		for _, x := range rb.On {
			if x == cond {
				return true
			}
		}
		return false
	}
	// The category's calls since the promotion that ran without an
	// approval: the ones the promotion let through.
	approved := map[string]bool{}
	for _, r := range after {
		if r["AGENT_EVENT"] == "approval.pending" {
			approved[r["AGENT_INVOCATION"]+" "+r["AGENT_CALL_ID"]] = true
		}
	}
	autonomous := map[string]bool{} // invocations
	// What the gateway saw come back (R2): a call of the category it let
	// through without an approval or a grant. The agent cannot hide its
	// failure by leaving out its own record.
	for _, res := range gw.Results {
		if res.At.Before(since) || res.ApprovalID != "" || res.Grant != "" || !perms.InCategory(c, res.Tool, res.At) {
			continue
		}
		if res.Invocation != "" {
			autonomous[res.Invocation] = true
		}
		if on("tool_error") && readiness.Failed(res.Outcome) {
			why := res.Outcome
			if res.Error != "" {
				why += ": " + res.Error
			}
			return fmt.Sprintf("rollback tool_error: %s failed at %s (the gateway: %s)", res.Tool, res.At.UTC().Format(time.RFC3339), why)
		}
	}
	for _, r := range after {
		if r["AGENT_EVENT"] != "tool.result" || !perms.InCategory(c, r["AGENT_TOOL"], readiness.At(r)) ||
			r["AGENT_APPROVAL_ID"] != "" || approved[r["AGENT_INVOCATION"]+" "+r["AGENT_CALL_ID"]] {
			continue
		}
		autonomous[r["AGENT_INVOCATION"]] = true
		if on("tool_error") && r["AGENT_OUTCOME"] == "error" {
			return fmt.Sprintf("rollback tool_error: %s failed at %s (%s)", r["AGENT_TOOL"], readiness.At(r).UTC().Format(time.RFC3339), r["AGENT_REASON"])
		}
	}
	if on("limit_exceeded") {
		for _, r := range after {
			if r["AGENT_EVENT"] == "limit.exceeded" && autonomous[r["AGENT_INVOCATION"]] {
				return fmt.Sprintf("rollback limit_exceeded: %s in activity %s at %s", r["AGENT_REASON"], r["AGENT_INVOCATION"], readiness.At(r).UTC().Format(time.RFC3339))
			}
		}
	}
	// The health check runs as root: only from data the seal covers
	// (security review 0.6, N1). Data that does not match it is denied
	// everything by the gateway already.
	if on("health_check_failed") && (d.Tampered == "" || strings.HasPrefix(d.Tampered, "an earlier version")) {
		check := a.HealthCheck
		if check == nil {
			check = runHealthCheck
		}
		if err := check(rb.HealthCheck); err != nil {
			return fmt.Sprintf("rollback health_check_failed: %q: %v", rb.HealthCheck, err)
		}
	}
	return ""
}

// rateCapped says why category c is demoted for its rate: calls of its
// tools reached the envelope's rate RateCapDemotion times within the last
// RateCapWindow since its promotion (each asked for an approval, or was
// denied by a rate_limit).
func rateCapped(c string, st *CategoryState, perms readiness.History, caps []readiness.RateCap, since, now time.Time) string {
	if st.Envelope == nil || st.Envelope.Rate == nil {
		return ""
	}
	if from := now.Add(-RateCapWindow); since.Before(from) {
		since = from
	}
	var hit []string
	for _, rc := range caps {
		if rc.At.Before(since) || !perms.InCategory(c, rc.Tool, rc.At) {
			continue
		}
		hit = append(hit, rc.Tool+" at "+rc.At.UTC().Format(time.RFC3339))
		if len(hit) == RateCapDemotion {
			r := st.Envelope.Rate
			return fmt.Sprintf("rate (%d calls per %s per tool) reached %d times within a day: %s",
				r.Calls, r.Per, RateCapDemotion, strings.Join(hit, ", "))
		}
	}
	return ""
}

// promotedAt is the time in promoted_by ("<who> <RFC 3339>").
func promotedAt(st *CategoryState) time.Time {
	f := strings.Fields(st.PromotedBy)
	if len(f) == 0 {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, f[len(f)-1])
	return t
}

func runHealthCheck(cmd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), HealthCheckTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).CombinedOutput()
	if err != nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return fmt.Errorf("%w: %s", err, lastLine(s))
		}
		return err
	}
	return nil
}

func lastLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
