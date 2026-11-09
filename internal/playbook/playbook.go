// Package playbook reads the diagnostic playbooks (CONTRACT.md §14c):
// published, defined diagnostic procedures, each a list of read-only
// tools to call and what to look for. A playbook grants nothing: each
// step is a gateway call, which the gateway decides like any other.
package playbook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The playbooks' directories: the package's, and the administrator's,
// where a file of the same name replaces the package's and an empty one
// hides it.
const (
	Vendor = "/usr/share/custos/playbooks"
	Site   = "/etc/custos/playbooks"
)

// Playbook is one playbook.
type Playbook struct {
	Name        string   `yaml:"-" json:"-"`
	Title       string   `yaml:"title" json:"title"`
	Description string   `yaml:"description" json:"description"`
	Needs       []string `yaml:"needs,omitempty" json:"needs,omitempty"`
	Steps       []Step   `yaml:"steps" json:"steps"`
	// Tools are further tools the model is offered in a run of the
	// playbook, besides its steps' (#155): a run offers only those, so
	// that a small model's prompt stays small.
	Tools   []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	LookFor string   `yaml:"look_for" json:"look_for"`
	// File is where it was read from (agent-admin playbooks).
	File string `yaml:"-" json:"file"`
	// Error says why a playbook file cannot be used (agent-admin
	// playbooks lists it with the error instead of failing).
	Error string `yaml:"-" json:"error,omitempty"`
}

// Step is a tool to call, server__tool as the gateway names it, with
// its arguments. A string in Args may name {unit}, the unit the activity
// is about; such a step runs only when the activity names a unit.
type Step struct {
	Tool     string         `yaml:"tool" json:"tool"`
	Why      string         `yaml:"why" json:"why"`
	Args     map[string]any `yaml:"args,omitempty" json:"args,omitempty"`
	Optional bool           `yaml:"optional,omitempty" json:"optional,omitempty"`
}

// Unit is the placeholder in a step's arguments for the unit the
// activity is about.
const Unit = "{unit}"

// UnitRE is the unit as a regular expression that matches it alone (its
// dots escaped), for an argument that is a pattern (security review
// 0.6, N5).
const UnitRE = "{unit_re}"

// NeedsUnit reports whether the step's arguments name {unit} or
// {unit_re}.
func (s Step) NeedsUnit() bool { return mentions(s.Args, Unit) || mentions(s.Args, UnitRE) }

// ArgsFor are the step's arguments with {unit} replaced by unit, and
// {unit_re} by it escaped for a regular expression; a fresh copy, never
// nil.
func (s Step) ArgsFor(unit string) map[string]any {
	out, _ := replace(s.Args, unit).(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func mentions(v any, s string) bool {
	switch v := v.(type) {
	case string:
		return strings.Contains(v, s)
	case map[string]any:
		for _, x := range v {
			if mentions(x, s) {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if mentions(x, s) {
				return true
			}
		}
	}
	return false
}

func replace(v any, unit string) any {
	switch v := v.(type) {
	case string:
		return strings.ReplaceAll(strings.ReplaceAll(v, UnitRE, regexp.QuoteMeta(unit)), Unit, unit)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = replace(x, unit)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = replace(x, unit)
		}
		return out
	}
	return v
}

// unitRE is a unit an activity may be about: a plain unit name, as an
// event watches (config's eventUnitRE), or a template instance.
var unitRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]{0,190}\.(service|socket|mount|swap|timer|path|target|scope|slice|device|automount)$`)

// ValidUnit reports whether s is a unit name an activity may name.
func ValidUnit(s string) bool { return unitRE.MatchString(s) }

var (
	nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	toolRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*__[A-Za-z0-9_.-]+$`)
)

// ValidName reports whether s is a valid playbook name (an agent name's
// form).
func ValidName(s string) bool { return nameRE.MatchString(s) }

// Parse reads one playbook file: one or more playbooks by name. Unknown
// keys are errors.
func Parse(b []byte) (map[string]*Playbook, error) {
	var doc map[string]*Playbook
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	for name, p := range doc {
		if !ValidName(name) {
			return nil, fmt.Errorf("%q is not a valid playbook name", name)
		}
		if p == nil {
			return nil, fmt.Errorf("%s: empty", name)
		}
		p.Name = name
		if err := p.check(); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return doc, nil
}

func (p *Playbook) check() error {
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("title: missing")
	}
	if strings.TrimSpace(p.LookFor) == "" {
		return errors.New("look_for: missing")
	}
	if len(p.Steps) == 0 {
		return errors.New("steps: at least one")
	}
	for i, t := range p.Tools {
		if !toolRE.MatchString(t) {
			return fmt.Errorf("tools[%d]: %q: want server__tool", i, t)
		}
	}
	required := 0
	for i, s := range p.Steps {
		if !toolRE.MatchString(s.Tool) {
			return fmt.Errorf("steps[%d].tool: %q: want server__tool", i, s.Tool)
		}
		if strings.TrimSpace(s.Why) == "" {
			return fmt.Errorf("steps[%d].why: missing", i)
		}
		if _, err := json.Marshal(s.Args); err != nil {
			return fmt.Errorf("steps[%d].args: %w", i, err)
		}
		if !s.Optional && !s.NeedsUnit() {
			required++
		}
	}
	if required == 0 {
		return errors.New("steps: at least one that is not optional and does not need a unit")
	}
	return nil
}

// Load reads the playbooks under root ("" on a host): the package's,
// replaced by the administrator's of the same file name, an empty file
// hiding it. A file that does not parse is kept as one playbook named
// after the file, with its Error.
func Load(root string) (map[string]*Playbook, error) {
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
	out := map[string]*Playbook{}
	names := make([]string, 0, len(files))
	for b := range files {
		names = append(names, b)
	}
	sort.Strings(names)
	for _, base := range names {
		f := files[base]
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(b)) == 0 {
			continue // hidden by the administrator
		}
		doc, err := Parse(b)
		if err != nil {
			n := strings.TrimSuffix(base, ".yaml")
			out[n] = &Playbook{Name: n, File: strings.TrimPrefix(f, root), Error: err.Error()}
			continue
		}
		for n, p := range doc {
			p.File = strings.TrimPrefix(f, root)
			out[n] = p
		}
	}
	return out, nil
}

// Missing are the playbook's steps that are not optional and whose tool
// is not among those offered.
func (p *Playbook) Missing(offered map[string]bool) []string {
	var out []string
	for _, s := range p.Steps {
		if !s.Optional && !offered[s.Tool] {
			out = append(out, s.Tool)
		}
	}
	return out
}

// Offer is the tools a run of the playbook offers the model, of those
// the gateway offers: its steps' and its tools (#155). The model is
// shown no other tool of the gateway's in the run.
func (p *Playbook) Offer(offered map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if offered[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, s := range p.Steps {
		add(s.Tool)
	}
	for _, t := range p.Tools {
		add(t)
	}
	return out
}

// Run are the steps the host runs, in order: all of them but the
// optional ones not offered, and without a unit those that need one;
// their arguments with the unit in place of {unit}.
func (p *Playbook) Run(offered map[string]bool, unit string) []Step {
	var out []Step
	for _, s := range p.Steps {
		if (s.Optional && !offered[s.Tool]) || (unit == "" && s.NeedsUnit()) {
			continue
		}
		s.Args = s.ArgsFor(unit)
		out = append(out, s)
	}
	return out
}

// Prompt is what the model is given to run the playbook: its steps (the
// optional ones only when offered, those that need a unit only with
// one), the unit, what to look for, and the person's own words, if any.
// The host makes the steps' calls itself before the model's first turn
// (CONTRACT.md §14c), so the model reads their outputs rather than
// having to issue the calls.
func (p *Playbook) Prompt(offered map[string]bool, unit, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run the diagnostic playbook %q (%s).\n", p.Name, p.Title)
	if p.Description != "" {
		b.WriteString(p.Description + "\n")
	}
	if unit != "" {
		b.WriteString("The unit in question: " + unit + "\n")
	}
	b.WriteString("\nThe host calls these tools for you, in this order, and you get their outputs; they only read:\n")
	for n, s := range p.Run(offered, unit) {
		fmt.Fprintf(&b, "%d. %s", n+1, s.Tool)
		if len(s.Args) > 0 {
			a, _ := json.Marshal(s.Args)
			fmt.Fprintf(&b, " %s", a)
		}
		fmt.Fprintf(&b, ": %s\n", s.Why)
	}
	b.WriteString("\nDo not repeat these calls. If the outputs leave a question open, call a tool you are offered " +
		"with arguments that answer it (for example the journal of one unit).\n")
	b.WriteString("\nLook for: " + strings.TrimSpace(p.LookFor) + "\n")
	b.WriteString("\nThen report with the tool report_diagnosis: what you found, your root-cause hypotheses with how " +
		"confident you are, the tool output each one rests on (quote it exactly), and a written remediation plan for " +
		"the administrator. Change nothing yourself.\n")
	if strings.TrimSpace(extra) != "" {
		b.WriteString("\nThe administrator adds: " + strings.TrimSpace(extra) + "\n")
	}
	return b.String()
}
