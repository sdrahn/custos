package admin

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/readiness"
)

// GatewayEnablement is the enablement state (CONTRACT.md §6b), gateway
// data next to the emergency stop: data.mcp.agents._enablement.
const GatewayEnablement = "/etc/mcp-gateway/policy/agents/_enablement.yaml"

// Diagnostics is the category a diagnostics-only host may use.
const Diagnostics = "diagnostics"

// Enablement states (§6b).
const (
	DiagnosticsOnly = "diagnostics-only"
	SomeCategories  = "categories"
)

// Enablement is the enablement state. Absent (nil): every category is
// enabled, as on every host before §6b.
type Enablement struct {
	State     string   `yaml:"state" json:"state"`
	Enabled   []string `yaml:"enabled" json:"enabled"`
	ChangedBy string   `yaml:"changed_by,omitempty" json:"changed_by,omitempty"`
	Record    string   `yaml:"record,omitempty" json:"record,omitempty"`
	// Signature is agent-admin's over state, enabled and record (the
	// seal, #109): the gateway counts a sealed host's state only with it.
	Signature string `yaml:"signature,omitempty" json:"-"`
	// Sealed: the host has a seal (show).
	Sealed bool `yaml:"-" json:"sealed"`
	// Tampered: show's check against the last enablement record and the
	// seal; "" when both match or the journal cannot be read.
	Tampered string `yaml:"-" json:"tampered,omitempty"`
}

// Usable reports whether a permission of the category (""; none) may be
// used: the gateway's rule (agents.rego, category_usable).
func (e *Enablement) Usable(cat string) bool {
	switch {
	case e == nil:
		return true
	case e.State == DiagnosticsOnly:
		return cat == Diagnostics
	case e.State == SomeCategories:
		if cat == "" || cat == Diagnostics {
			return true
		}
		for _, c := range e.Enabled {
			if c == cat {
				return true
			}
		}
	}
	return false
}

// Digest is the SHA-256 of state and enabled, JSON with sorted keys
// (AGENT_ENABLEMENT_SHA256).
func (e *Enablement) Digest() string {
	en := append([]string{}, e.Enabled...)
	if en == nil {
		en = []string{}
	}
	b, _ := json.Marshal(map[string]any{"enabled": en, "state": e.State})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (e *Enablement) summary() string {
	b, _ := json.Marshal(map[string]any{"state": e.State, "enabled": e.Enabled})
	return string(b)
}

// GetEnablement reads the enablement state; nil when there is none.
func (a *Admin) GetEnablement() (*Enablement, error) {
	b, err := os.ReadFile(a.Path(GatewayEnablement))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		E *Enablement `yaml:"_enablement"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", GatewayEnablement, err)
	}
	if doc.E == nil {
		return nil, fmt.Errorf("%s: no _enablement", GatewayEnablement)
	}
	return doc.E, nil
}

// writeEnablement writes the state with a new record id, records the
// change (categories.enabled, or event: categories.disabled) and waits for
// the gateway's OPA.
func (a *Admin) writeEnablement(from, to *Enablement, by, msg string, event ...string) error {
	ev := journal.CategoriesEnabled
	if len(event) > 0 {
		ev = event[0]
	}
	id := "n-" + a.Secret()
	if len(id) > 18 {
		id = id[:18]
	}
	to.ChangedBy = by + " " + a.Now().UTC().Format(time.RFC3339Nano)
	to.Record = id
	key, err := a.enablementKey()
	if err != nil {
		return err
	}
	if to.Signature, err = sign(key, to); err != nil {
		return err
	}
	if err := a.writeEnablementFile(to); err != nil {
		return err
	}
	// The seal after the state: in between the gateway sees a signed
	// state without a seal, which counts as it is.
	if err := a.writeSeal(key); err != nil {
		return err
	}
	fromJSON := `{"state":"","enabled":"all"}` // the broader install's
	if from != nil {
		fromJSON = from.summary()
	}
	a.Record(ev, "", msg, journal.Fields{
		"BY": by, "ENABLEMENT": id, "ENABLEMENT_FROM": fromJSON, "ENABLEMENT_TO": to.summary(),
		"ENABLEMENT_SHA256": to.Digest(),
		// The host is sealed from here on: a later show that finds no
		// seal knows it was removed (security review M5).
		"SEALED": "1",
	})
	return a.waitPolicy("mcp/agents/_enablement/record", func(v any) bool { return v == id })
}

// writeEnablementFile writes the state as it is (signed or not).
func (a *Admin) writeEnablementFile(to *Enablement) error {
	var buf bytes.Buffer
	buf.WriteString("# The task categories this host may use (custos CONTRACT.md §6b), written\n" +
		"# by agent-admin. Change it only with agent-admin enable or disable: a change\n" +
		"# without its record is reported as tampered, and one its signature does not\n" +
		"# cover counts as diagnostics-only (the seal, _seal.yaml).\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]*Enablement{"_enablement": to}); err != nil {
		return err
	}
	return host.WriteFile(a.Path(GatewayEnablement), buf.Bytes(), 0o644)
}

// enablementKey is the key a change of the state is signed with: the
// seal's, when the host has one (a missing credential is an error, never
// a new key: security review 0.6, N2), else a new one, which seals the
// host.
func (a *Admin) enablementKey() (*ecdsa.PrivateKey, error) {
	pub, _, err := a.sealInfo()
	if err != nil {
		return nil, err
	}
	if pub == "" {
		return a.sealKey()
	}
	k, err := a.existingSealKey()
	if err != nil {
		return nil, fmt.Errorf("%w (a lost key: agent-admin seal --rekey)", err)
	}
	if !matchesSeal(k, pub) {
		return nil, fmt.Errorf("the seal's key (%s) does not match the seal (%s): agent-admin seal --rekey", filepath.Join(CredstoreDir, SealCredential), GatewaySeal)
	}
	return k, nil
}

// InitDiagnosticsOnly makes a fresh host diagnostics-only (the bundled
// install, custos-setup, §17a). It is not a way back: it refuses a host
// that has enablement data, or an agent that uses another category or a
// permission without one.
func (a *Admin) InitDiagnosticsOnly(by string) error {
	e, err := a.CurrentEnablement()
	if err != nil {
		return err
	}
	if e != nil {
		return fmt.Errorf("this host's enablement is set already (%s, by %s): going back to diagnostics-only is not offered", e.State, e.ChangedBy)
	}
	p, err := a.Show()
	if err != nil {
		return err
	}
	for name, d := range p.Agents {
		for _, perm := range d.Permissions {
			if c, _ := perm["category"].(string); c != Diagnostics {
				return fmt.Errorf("agent %s has permissions outside diagnostics: diagnostics-only is for a fresh install", name)
			}
		}
	}
	to := &Enablement{State: DiagnosticsOnly, Enabled: []string{Diagnostics}}
	if err := a.writeEnablement(nil, to, by, by+" made this host diagnostics-only"); err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "this host is diagnostics-only: other task categories are disabled until agent-admin enable")
	return nil
}

// Enable enables task categories (§6b): the one administrator action
// from diagnostics-only (or a partly enabled host) towards the broader
// capability. Enabled categories start supervised: nothing else changes.
func (a *Admin) Enable(by string, cats []string, all bool) error {
	catalog, err := a.Categories()
	if err != nil {
		return err
	}
	if all {
		cats = cats[:0]
		for c := range catalog {
			cats = append(cats, c)
		}
	}
	if len(cats) == 0 {
		return errors.New("enable: name the categories, or --all")
	}
	for _, c := range cats {
		if err := a.checkCategory(c); err != nil {
			return err
		}
	}
	from, err := a.CurrentEnablement()
	if err != nil {
		return err
	}
	if from == nil {
		return errors.New("every task category is enabled on this host already (no enablement state)")
	}
	set := map[string]bool{Diagnostics: true}
	if from.State == SomeCategories {
		for _, c := range from.Enabled {
			set[c] = true
		}
	}
	var added []string
	for _, c := range cats {
		if !set[c] {
			added = append(added, c)
		}
		set[c] = true
	}
	if len(added) == 0 && from.State == SomeCategories {
		return fmt.Errorf("enabled already: %s", strings.Join(cats, ", "))
	}
	to := &Enablement{State: SomeCategories}
	for c := range set {
		to.Enabled = append(to.Enabled, c)
	}
	sort.Strings(to.Enabled)
	sort.Strings(added)
	if err := a.writeEnablement(from, to, by, fmt.Sprintf("%s enabled the task categories %s", by, strings.Join(added, ", "))); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "enabled: %s; they start supervised (agent-admin readiness, promote)\n", strings.Join(added, ", "))
	return nil
}

// Disable disables task categories again (§6b, #110): the way back from
// enable. all returns the host to diagnostics-only. It refuses, naming
// what is in the way and recording the refusal (category.refused), while
// an agent has a permission in a category it disables (for all: any
// permission not in diagnostics) or has one promoted (bounded): remove
// the permissions with agent-admin permissions, demote with agent-admin
// demote, then disable. On a host without an enablement state (every
// category enabled) it creates one: every category of the catalog but
// those disabled. The change is recorded (categories.disabled).
func (a *Admin) Disable(by string, cats []string, all bool) error {
	catalog, err := a.Categories()
	if err != nil {
		return err
	}
	if !all && len(cats) == 0 {
		return errors.New("disable: name the categories, or --all for diagnostics-only")
	}
	for _, c := range cats {
		if err := a.checkCategory(c); err != nil {
			return err
		}
		if c == Diagnostics {
			return errors.New("disable: diagnostics is always enabled; --all leaves only it")
		}
	}
	from, err := a.CurrentEnablement()
	if err != nil {
		return err
	}
	// What is enabled now.
	set := map[string]bool{Diagnostics: true}
	switch {
	case from == nil:
		for c := range catalog {
			set[c] = true
		}
	case from.State == DiagnosticsOnly:
		return errors.New("this host is diagnostics-only already: nothing to disable")
	default:
		for _, c := range from.Enabled {
			set[c] = true
		}
	}
	off := map[string]bool{}
	if all {
		for c := range set {
			if c != Diagnostics {
				off[c] = true
			}
		}
	} else {
		for _, c := range cats {
			if !set[c] {
				return fmt.Errorf("disable: task category %s is not enabled on this host", c)
			}
			off[c] = true
		}
	}
	// What is in the way.
	p, err := a.Show()
	if err != nil {
		return err
	}
	var blockers []string
	names := make([]string, 0, len(p.Agents))
	for n := range p.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		d := p.Agents[name]
		perms := map[string]bool{}
		for _, perm := range d.Permissions {
			c, _ := perm["category"].(string)
			if off[c] || (all && c != Diagnostics) {
				perms[c] = true
			}
		}
		for c := range perms {
			shown := c
			if c == "" {
				shown = "(none)"
			}
			blockers = append(blockers, fmt.Sprintf("agent %s has permissions in %s (agent-admin permissions %s)", name, shown, name))
		}
		for c, st := range d.Categories {
			if off[c] && st != nil && st.Mode == "bounded" {
				blockers = append(blockers, fmt.Sprintf("agent %s has %s promoted (agent-admin demote %s %s)", name, c, name, c))
			}
		}
	}
	if len(blockers) > 0 {
		sort.Strings(blockers)
		what := "disable"
		if all {
			what = "disable --all"
		}
		for c := range off {
			a.Record(journal.CategoryRefused, "", fmt.Sprintf("%s: category %s is still in use", what, c),
				journal.Fields{"CATEGORY": c, "REASON": what, "BY": by})
		}
		return fmt.Errorf("%s: still in use: %s", what, strings.Join(blockers, "; "))
	}
	var gone []string
	for c := range off {
		gone = append(gone, c)
	}
	sort.Strings(gone)
	if all {
		to := &Enablement{State: DiagnosticsOnly, Enabled: []string{Diagnostics}}
		if err := a.writeEnablement(from, to, by, by+" returned this host to diagnostics-only", journal.CategoriesDisabled); err != nil {
			return err
		}
		fmt.Fprintln(a.Out, "this host is diagnostics-only again: other task categories are disabled until agent-admin enable")
		return nil
	}
	to := &Enablement{State: SomeCategories}
	for c := range set {
		if !off[c] {
			to.Enabled = append(to.Enabled, c)
		}
	}
	sort.Strings(to.Enabled)
	if err := a.writeEnablement(from, to, by, fmt.Sprintf("%s disabled the task categories %s", by, strings.Join(gone, ", ")), journal.CategoriesDisabled); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "disabled: %s; the gateway denies them and hides their tools (agent-admin enable to undo)\n", strings.Join(gone, ", "))
	return nil
}

// CheckEnabled refuses permissions of a disabled category, recording the
// refusal (category.refused) for the person, once per disabled category.
// what names the action. The error names everything that is disabled.
func (a *Admin) CheckEnabled(name, what string, perms []map[string]any) error {
	e, err := a.CurrentEnablement()
	if err != nil || e == nil {
		return err
	}
	var cats []string
	seen := map[string]bool{}
	for _, p := range perms {
		c, _ := p["category"].(string)
		if e.Usable(c) || seen[c] {
			continue
		}
		seen[c] = true
		shown := c
		if c == "" {
			shown = "(none)"
		} else {
			cats = append(cats, c)
		}
		a.Record(journal.CategoryRefused, name, fmt.Sprintf("%s: category %s is not enabled on this host (%s)", what, shown, e.State),
			journal.Fields{"CATEGORY": shown, "REASON": what, "BY": a.By})
	}
	if len(seen) == 0 {
		return nil
	}
	sort.Strings(cats)
	var parts []string
	if len(cats) > 0 {
		parts = append(parts, "task categories "+strings.Join(cats, ", "))
	}
	if seen[""] {
		parts = append(parts, "permissions without a category")
	}
	msg := fmt.Sprintf("%s are not enabled on this host (%s)", strings.Join(parts, " and "), e.State)
	if len(cats) > 0 {
		msg += ": agent-admin enable " + strings.Join(cats, " ")
	} else {
		msg += ": every permission needs category: diagnostics, or agent-admin enable --all"
	}
	return errors.New(msg)
}

// checkCategoryEnabled refuses a disabled category for promote.
func (a *Admin) checkCategoryEnabled(name, cat string) error {
	e, err := a.CurrentEnablement()
	if err != nil || e.Usable(cat) {
		return err
	}
	a.Record(journal.CategoryRefused, name, fmt.Sprintf("promote: category %s is not enabled on this host (%s)", cat, e.State),
		journal.Fields{"CATEGORY": cat, "REASON": "promote", "BY": a.By})
	return fmt.Errorf("task category %s is not enabled on this host (%s): agent-admin enable %s", cat, e.State, cat)
}

// enablementTampered compares the state in force with its record in the
// journal; "" when they match, or when the journal cannot be read.
func (a *Admin) enablementTampered(e *Enablement) string {
	read := a.JournalJSON
	if read == nil {
		read = func(args ...string) ([]byte, error) {
			return exec.Command("journalctl", append([]string{"-o", "json", "--no-pager"}, args...)...).Output()
		}
	}
	if e.Record == "" {
		return "the state has no record: it was not written by agent-admin"
	}
	recs, err := a.enablementRecords(read)
	if err != nil {
		return ""
	}
	digest, latest := "", ""
	for _, r := range recs {
		if r["AGENT_ENABLEMENT"] == e.Record {
			if d, ok := r["AGENT_ENABLEMENT_SHA256"].(string); ok {
				digest = d
			}
		}
		if id, ok := r["AGENT_ENABLEMENT"].(string); ok && id != "" {
			latest = id
		}
	}
	switch {
	case digest == "":
		return "no enablement record " + e.Record + " in the journal"
	case digest != e.Digest():
		return "the state differs from its record " + e.Record
	case latest != e.Record:
		// A state signed earlier verifies: the seal has no freshness, the
		// journal does (security review M5).
		return "the state on file is record " + e.Record + "'s, but the latest change is record " + latest +
			": an earlier state was put back; agent-admin enable or disable writes the current one"
	}
	return ""
}

// enablementRecords are agent-admin's enablement records as root, oldest
// first (journalctl's order). journalctl ORs matches of the same field.
func (a *Admin) enablementRecords(read func(...string) ([]byte, error)) ([]map[string]any, error) {
	out, err := read("_UID=0", "SYSLOG_IDENTIFIER=custos-promotion", "AGENT_EVENT="+journal.CategoriesEnabled,
		"AGENT_EVENT="+journal.CategoriesDisabled)
	if err != nil {
		return nil, err
	}
	var recs []map[string]any
	for _, line := range bytes.Split(out, []byte("\n")) {
		var r map[string]any
		// agent-admin's records as root only: an agent could write one
		// with any other field (readiness.ByRoot).
		if json.Unmarshal(line, &r) == nil && readiness.ByRoot(stringsOf(r)) {
			recs = append(recs, r)
		}
	}
	return recs, nil
}

// stringsOf is a journal record's string fields.
func stringsOf(r map[string]any) readiness.Record {
	out := readiness.Record{}
	for k, v := range r {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
