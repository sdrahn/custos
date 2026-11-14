// Package category is the task categories' data (CONTRACT.md §6a): the
// catalog of categories a host has, a category's state in an agent's
// gateway data, and the envelope a promoted category acts within.
// Promoting, demoting and measuring are agent-admin's.
package category

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/config"
)

// The category catalog: the packages' entries, then the administrator's.
// A file in /etc replaces the one of the same name in /usr; an empty
// one disables it.
const (
	Vendor = "/usr/share/custos/categories.d"
	Site   = "/etc/custos/categories.d"
)

// Category is one entry of the catalog.
type Category struct {
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// State is a category's entry in the agent's gateway data
// (data.mcp.agents.<name>.categories.<category>), as promote and demote
// write it.
type State struct {
	Mode       string    `yaml:"mode" json:"mode"`
	Envelope   *Envelope `yaml:"envelope,omitempty" json:"envelope,omitempty"`
	PromotedBy string    `yaml:"promoted_by,omitempty" json:"promoted_by,omitempty"`
	DemotedBy  string    `yaml:"demoted_by,omitempty" json:"demoted_by,omitempty"`
	Promotion  string    `yaml:"promotion,omitempty" json:"promotion,omitempty"`
}

// Envelope is where, when and how much a promoted category acts without
// an approval. This version enforces windows (in the gateway's policy);
// the other parts of §6a are refused until something enforces them.
type Envelope struct {
	Windows []Window `yaml:"windows" json:"windows"`
	// Rate caps the category's calls without an approval, per tool (the
	// gateway's rate_limit obligation, O12).
	Rate *Rate `yaml:"rate,omitempty" json:"rate,omitempty"`
	// Rollback: conditions on which the demotion evaluator demotes the
	// category at once (agent-admin evaluate, custos-demote.timer).
	Rollback *Rollback `yaml:"rollback,omitempty" json:"rollback,omitempty"`
}

// Rollback conditions (§6a): tool_error, limit_exceeded (of the
// category's calls without approval since the promotion) and
// health_check_failed (HealthCheck, run as root by sh -c, exits non-zero).
type Rollback struct {
	On          []string `yaml:"on" json:"on"`
	HealthCheck string   `yaml:"health_check,omitempty" json:"health_check,omitempty"`
}

// Rate is the envelope's rate: at most Calls calls of each of the
// category's tools without an approval per Per (1s, 1m or 1h), counted
// by the gateway, which denies a call over it.
type Rate struct {
	Calls int    `yaml:"calls" json:"calls"`
	Per   string `yaml:"per" json:"per"`
}

// RatePers are the windows the gateway counts in (its rate_limit
// obligation: N/s, N/m, N/h).
var RatePers = []string{"1s", "1m", "1h"}

// RollbackConditions are the conditions the evaluator knows.
var RollbackConditions = []string{"tool_error", "limit_exceeded", "health_check_failed"}

// Window is a weekly time window.
type Window struct {
	Days []string `yaml:"days,omitempty" json:"days,omitempty"`
	From string   `yaml:"from" json:"from"`
	To   string   `yaml:"to" json:"to"`
	TZ   string   `yaml:"tz,omitempty" json:"tz,omitempty"`
}

// Load reads the catalog of the system below root.
func Load(root string) (map[string]Category, error) {
	files := map[string]string{} // base name → path; /etc replaces /usr
	for _, dir := range []string{Vendor, Site} {
		m, err := filepath.Glob(filepath.Join(root, dir, "*.yaml"))
		if err != nil {
			return nil, err
		}
		for _, f := range m {
			files[filepath.Base(f)] = f
		}
	}
	out := map[string]Category{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var doc map[string]Category
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for name, c := range doc {
			if !config.ValidName(name) {
				return nil, fmt.Errorf("%s: %q is not a valid category name", f, name)
			}
			if name == "uncategorized" {
				return nil, fmt.Errorf("%s: uncategorized is not a category", f)
			}
			out[name] = c
		}
	}
	return out, nil
}

// Check refuses a category the catalog does not have.
func Check(root, cat string) error {
	cats, err := Load(root)
	if err != nil {
		return err
	}
	if _, ok := cats[cat]; !ok {
		names := make([]string, 0, len(cats))
		for n := range cats {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Errorf("no task category %q (the catalog has: %s)", cat, strings.Join(names, ", "))
	}
	return nil
}

var (
	hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	days = map[string]bool{"mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true, "sun": true}
)

// ParseEnvelope reads and checks an envelope. Parts of §6a that nothing
// enforces yet are refused, never ignored.
func ParseEnvelope(b []byte) (*Envelope, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	for k := range raw {
		switch k {
		case "windows":
		case "rate":
		case "rollback":
		case "hosts", "blast_radius":
			return nil, fmt.Errorf("envelope: %s needs the fleet, which is parked (CONTRACT.md O13)", k)
		default:
			return nil, fmt.Errorf("envelope: unknown key %q", k)
		}
	}
	var e Envelope
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	if len(e.Windows) == 0 {
		return nil, errors.New("envelope: no windows: a promoted category acts without approval only within its windows")
	}
	for i, w := range e.Windows {
		if !hhmm.MatchString(w.From) || !hhmm.MatchString(w.To) {
			return nil, fmt.Errorf("envelope: windows[%d]: from and to are HH:MM", i)
		}
		if w.From >= w.To {
			return nil, fmt.Errorf("envelope: windows[%d]: from %s is not before to %s (a window across midnight is two windows)", i, w.From, w.To)
		}
		for _, d := range w.Days {
			if !days[d] {
				return nil, fmt.Errorf("envelope: windows[%d]: day %q: want mon, tue, wed, thu, fri, sat or sun", i, d)
			}
		}
		if w.TZ != "" {
			if _, err := time.LoadLocation(w.TZ); err != nil {
				return nil, fmt.Errorf("envelope: windows[%d]: tz %q: %v", i, w.TZ, err)
			}
		}
	}
	if r := e.Rate; r != nil {
		if r.Calls < 1 {
			return nil, fmt.Errorf("envelope: rate: calls %d: want 1 or more", r.Calls)
		}
		if !slices.Contains(RatePers, r.Per) {
			return nil, fmt.Errorf("envelope: rate: per %q: want 1s, 1m or 1h (the gateway counts per second, minute or hour)", r.Per)
		}
	}
	if r := e.Rollback; r != nil {
		if len(r.On) == 0 {
			return nil, errors.New("envelope: rollback: no conditions (on: tool_error, limit_exceeded, health_check_failed)")
		}
		health := false
		for _, c := range r.On {
			if !slices.Contains(RollbackConditions, c) {
				return nil, fmt.Errorf("envelope: rollback: condition %q: want tool_error, limit_exceeded or health_check_failed", c)
			}
			health = health || c == "health_check_failed"
		}
		if health != (strings.TrimSpace(r.HealthCheck) != "") {
			return nil, errors.New("envelope: rollback: health_check_failed and health_check (a command) go together")
		}
	}
	return &e, nil
}

// EnvelopeHash is the SHA-256 of the envelope's JSON with sorted keys
// (§10, AGENT_ENVELOPE_SHA256).
func EnvelopeHash(e *Envelope) string {
	if e == nil {
		return ""
	}
	b, _ := json.Marshal(e)
	var v any
	json.Unmarshal(b, &v)
	b, _ = json.Marshal(v) // maps marshal with sorted keys
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
