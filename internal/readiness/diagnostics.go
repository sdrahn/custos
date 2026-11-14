package readiness

// The diagnostics history: what an agent's diagnosis reports and the
// feedback on them show (CONTRACT.md §4a, §6b).

import (
	"encoding/json"
	"fmt"
	"time"
)

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
