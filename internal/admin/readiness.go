package admin

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/readiness"
)

// PromotionConfig holds the site's readiness thresholds (§6a); without it,
// readiness.Defaults.
const PromotionConfig = "/etc/custos/promotion.yaml"

// Thresholds reads PromotionConfig over the defaults.
func (a *Admin) Thresholds() (readiness.Thresholds, error) {
	th := readiness.Defaults
	b, err := os.ReadFile(a.Path(PromotionConfig))
	if errors.Is(err, os.ErrNotExist) {
		return th, nil
	}
	if err != nil {
		return th, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&th); err != nil && err.Error() != "EOF" {
		return th, fmt.Errorf("%s: %w", PromotionConfig, err)
	}
	switch {
	case th.ObservationDays < 0, th.Proposals < 0, th.WindowDays < 1:
		return th, fmt.Errorf("%s: observation_days and proposals are 0 or more, window_days 1 or more", PromotionConfig)
	case th.OverrideRate < 0 || th.OverrideRate > 1, th.IncidentRate < 0 || th.IncidentRate > 1,
		th.DiagnosticsConfirmedRate < 0 || th.DiagnosticsConfirmedRate > 1:
		return th, fmt.Errorf("%s: override_rate, incident_rate and diagnostics_confirmed_rate are fractions from 0 to 1 (0.05 is 5 %%)", PromotionConfig)
	case th.DiagnosticsFeedback < 0:
		return th, fmt.Errorf("%s: diagnostics_feedback is 0 or more", PromotionConfig)
	}
	return th, nil
}

// Report is agent-admin readiness's answer (§17).
type Report struct {
	Agent      string                        `json:"agent"`
	Thresholds readiness.Thresholds          `json:"thresholds"`
	Categories map[string]readiness.Measures `json:"categories"`
	// Diagnostics is the agent's diagnostics history over the window
	// (§6b). DiagnosticsApply: the host started diagnostics-only, so it
	// is part of every category's readiness (diagnostics' own aside).
	Diagnostics      readiness.Diagnostics `json:"diagnostics"`
	DiagnosticsApply bool                  `json:"diagnostics_apply"`
}

// Readiness measures an agent's categories (or one), from the agent's
// journal and the gateway's approval decisions over the thresholds'
// window.
func (a *Admin) Readiness(name string, only ...string) (*Report, error) {
	if err := host.CheckName(name); err != nil {
		return nil, err
	}
	d, err := a.readGateway(name)
	if err != nil {
		return nil, err
	}
	th, err := a.Thresholds()
	if err != nil {
		return nil, err
	}
	cats := readiness.Categories(d.Permissions)
	if len(only) > 0 {
		cats = only
	}
	now := a.Now()
	recs, gw, err := a.journalRecords(name, now.AddDate(0, 0, -th.WindowDays))
	if err != nil {
		return nil, err
	}
	r := &Report{Agent: name, Thresholds: th, Categories: map[string]readiness.Measures{}, Diagnostics: readiness.DiagnosticsOf(recs)}
	// A host has an enablement state only when it started diagnostics-only
	// (§6b): there, what the diagnostics agent did is part of readiness.
	e, err := a.currentEnablement()
	if err != nil {
		return nil, err
	}
	r.DiagnosticsApply = e != nil
	perms := readiness.NewHistory(d.Permissions, recs)
	for _, c := range cats {
		m := readiness.Measure(c, perms, recs, gw, th, now)
		if r.DiagnosticsApply && c != Diagnostics {
			m.Unmet = append(m.Unmet, r.Diagnostics.Unmet(th)...)
			m.Ready = len(m.Unmet) == 0
		}
		r.Categories[c] = m
	}
	return r, nil
}

// journalRecords reads the agent's records, and what the gateway's audit
// says about it (decisions, call results R2, reviews R4), since then.
func (a *Admin) journalRecords(name string, since time.Time) ([]readiness.Record, readiness.Audit, error) {
	at := "@" + strconv.FormatInt(since.Unix(), 10)
	read := a.JournalJSON
	if read == nil {
		read = func(args ...string) ([]byte, error) {
			return exec.Command("journalctl", append([]string{"-o", "json", "--no-pager", "--since", at}, args...)...).Output()
		}
	}
	agentLog, err := read("AGENT_NAME="+name, "AGENT_CONTRACT=1")
	if err != nil {
		return nil, readiness.Audit{}, fmt.Errorf("reading agent %s's journal: %w", name, err)
	}
	// The emergency stops (every agent's, so no AGENT_NAME): an approved
	// call whose activity one stopped is an incident (O11).
	stops, err := read("_UID=0", "SYSLOG_IDENTIFIER=custos-promotion", "AGENT_EVENT="+readiness.AgentsStopped)
	if err != nil {
		return nil, readiness.Audit{}, fmt.Errorf("reading the emergency stops' records: %w", err)
	}
	// The gateway's own records (journald's trusted _SYSTEMD_UNIT): -u
	// would also return root's records about the unit, which a root
	// process could write with OBJECT_SYSTEMD_UNIT (security review H1).
	gwLog, err := read("_SYSTEMD_UNIT=mcp-gateway.service")
	if err != nil {
		return nil, readiness.Audit{}, fmt.Errorf("reading mcp-gateway's journal: %w", err)
	}
	recs := readiness.ParseRecords(agentLog)
	for _, s := range readiness.ParseRecords(stops) {
		if s["AGENT_EVENT"] == readiness.AgentsStopped && readiness.ByRoot(s) {
			recs = append(recs, s)
		}
	}
	return recs, readiness.ParseAudit(gwLog, "agent-"+name), nil
}
