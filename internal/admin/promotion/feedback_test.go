package promotion

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/admin/admintest"
	"github.com/sdrahn/custos/internal/journal"
)

// Feedback on a report is recorded outside the agent with the person,
// and only for a report that exists and its hypotheses.
func TestFeedback(t *testing.T) {
	a, _ := admintest.New(t)
	mem := &journal.Memory{}
	a.Journal = func(string) journal.Logger { return mem }
	report := `{"kind":"diagnosis","hypotheses":[{"id":"h1"},{"id":"h2"}]}`
	a.JournalJSON = func(args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "AGENT_NAME=hc AGENT_EVENT=diagnosis.report AGENT_INVOCATION=e-1" {
			b, _ := json.Marshal(map[string]string{"AGENT_REPORT": report})
			return b, nil
		}
		return nil, nil
	}
	f := Feedback{Hypotheses: map[string]string{"h1": "confirmed", "h2": "rejected"}, Plan: "useful", Executed: true, Comment: "right"}
	if err := RecordFeedback(a, "hc", "e-1", "alice", f); err != nil {
		t.Fatal(err)
	}
	rec := mem.Events(journal.DiagnosisFeedback)
	if len(rec) != 1 || rec[0].Fields["BY"] != "alice" || rec[0].Fields["INVOCATION"] != "e-1" {
		t.Fatalf("records %+v", rec)
	}
	var got Feedback
	json.Unmarshal([]byte(rec[0].Fields["FEEDBACK"]), &got)
	if got.Hypotheses["h1"] != "confirmed" || got.Plan != "useful" || !got.Executed || got.Comment != "right" {
		t.Errorf("feedback %+v", got)
	}
	for _, c := range []struct {
		name, act string
		f         Feedback
		want      string
	}{
		{"hc", "e-2", f, "no diagnosis report"},
		{"hc", "e-1", Feedback{Hypotheses: map[string]string{"h9": "confirmed"}}, `no hypothesis "h9"`},
		{"hc", "e-1", Feedback{Hypotheses: map[string]string{"h1": "true"}}, "want confirmed"},
		{"hc", "e-1", Feedback{Plan: "great"}, "want useful"},
		{"hc", "e-1", Feedback{}, "no feedback"},
		{"hc", "e-1 OR x", f, "not an activity id"},
		{"../x", "e-1", f, ""},
	} {
		if err := RecordFeedback(a, c.name, c.act, "alice", c.f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s %+v: %v", c.name, c.act, c.f, err)
		}
	}
	if len(mem.Events(journal.DiagnosisFeedback)) != 1 {
		t.Error("a refused feedback was recorded")
	}
}
