package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/journal"
)

// activityRE is an activity id's form (ADK invocation ids, e-<uuid>): it
// goes into a journal match.
var activityRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,79}$`)

// Feedback is a person's feedback on a diagnosis report (CONTRACT.md §4a):
// per hypothesis confirmed, rejected or unsure; the plan useful or
// not_useful; whether they executed it; a comment.
type Feedback struct {
	Hypotheses map[string]string `json:"hypotheses,omitempty"`
	Plan       string            `json:"plan,omitempty"`
	Executed   bool              `json:"executed,omitempty"`
	Comment    string            `json:"comment,omitempty"`
}

// RecordFeedback records feedback on an activity's report as
// diagnosis.feedback, outside the agent: it is trust evidence for the
// promotion path (§6b), as the review verdicts are (§6a). It refuses an
// activity without a report, and a hypothesis the report does not have.
func (a *Admin) RecordFeedback(name, activity, by string, f Feedback) error {
	if err := host.CheckName(name); err != nil {
		return err
	}
	if !activityRE.MatchString(activity) {
		return fmt.Errorf("%q is not an activity id", activity)
	}
	for h, v := range f.Hypotheses {
		if v != "confirmed" && v != "rejected" && v != "unsure" {
			return fmt.Errorf("hypothesis %s: %q: want confirmed, rejected or unsure", h, v)
		}
	}
	if f.Plan != "" && f.Plan != "useful" && f.Plan != "not_useful" {
		return fmt.Errorf("plan: %q: want useful or not_useful", f.Plan)
	}
	if len(f.Hypotheses) == 0 && f.Plan == "" && !f.Executed && strings.TrimSpace(f.Comment) == "" {
		return errors.New("no feedback: give --hypothesis, --plan, --executed or --comment")
	}
	ids, err := a.reportHypotheses(name, activity)
	if err != nil {
		return err
	}
	for h := range f.Hypotheses {
		if !ids[h] {
			return fmt.Errorf("the report of %s has no hypothesis %q", activity, h)
		}
	}
	b, _ := json.Marshal(f)
	a.Record(journal.DiagnosisFeedback, name, fmt.Sprintf("%s gave feedback on the diagnosis report of %s", by, activity),
		journal.Fields{"INVOCATION": activity, "BY": by, "FEEDBACK": string(b)})
	fmt.Fprintf(a.Out, "agent %s: feedback on the report of %s recorded\n", name, activity)
	return nil
}

// reportHypotheses are the hypothesis ids of the activity's last
// diagnosis.report in the journal.
func (a *Admin) reportHypotheses(name, activity string) (map[string]bool, error) {
	read := a.JournalJSON
	if read == nil {
		read = func(args ...string) ([]byte, error) {
			return exec.Command("journalctl", append([]string{"-o", "json", "--no-pager"}, args...)...).Output()
		}
	}
	out, err := read("AGENT_NAME="+name, "AGENT_EVENT="+journal.DiagnosisReport, "AGENT_INVOCATION="+activity)
	if err != nil {
		return nil, fmt.Errorf("reading agent %s's journal: %w", name, err)
	}
	var ids map[string]bool
	for _, line := range bytes.Split(out, []byte("\n")) {
		var rec map[string]any
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		s, _ := rec["AGENT_REPORT"].(string)
		var r struct {
			Hypotheses []struct {
				ID string `json:"id"`
			} `json:"hypotheses"`
		}
		if json.Unmarshal([]byte(s), &r) != nil {
			continue
		}
		ids = map[string]bool{} // the last report counts
		for _, h := range r.Hypotheses {
			ids[h.ID] = true
		}
	}
	if ids == nil {
		return nil, fmt.Errorf("agent %s has no diagnosis report for activity %s", name, activity)
	}
	return ids, nil
}
