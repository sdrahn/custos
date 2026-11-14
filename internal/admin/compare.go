package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"
	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/host"
	"gopkg.in/yaml.v3"
)

// TemplateFile is the file in an agent's configuration directory that
// names the template it was created from (CONTRACT.md §14a): written by
// create --template and by apply-template.
const TemplateFile = "template"

// agentKeys are the configuration keys an agent keeps from its own
// configuration when the template's is applied (§14a): who it is, its
// model and credentials, when it runs and where. The rest (description,
// instruction, gateway servers and tools, limits, interaction) is the
// template's. In gateway, everything but servers and tools is the
// agent's: how it reaches the gateway and its credential.
var agentKeys = []string{"name", "model", "schedules", "events", "sessions", "sandbox"}

var templateGatewayKeys = []string{"servers", "tools"}

// Comparison is an agent against its template (agent-admin compare).
type Comparison struct {
	Agent string `json:"agent"`
	// Template is the template compared with; "" when none is recorded
	// and none was named.
	Template string `json:"template"`
	// Recorded: the template is the one recorded for the agent.
	Recorded      bool            `json:"recorded"`
	Configuration *ConfigDiff     `json:"configuration,omitempty"`
	Permissions   *PermissionDiff `json:"permissions,omitempty"`
	Error         string          `json:"error,omitempty"`
}

// ConfigDiff: Diff is a line diff ("-" the agent's, "+" the template's)
// of both configurations with the agent's own keys kept, keys sorted and
// comments left out; "" when they are the same.
type ConfigDiff struct {
	Same bool   `json:"same"`
	Diff string `json:"diff,omitempty"`
}

// PermissionDiff: the permissions only the agent has (Removed by applying
// the template's) and those only the template has (Added), as JSON
// objects; their order does not count.
type PermissionDiff struct {
	Same    bool             `json:"same"`
	Added   []map[string]any `json:"added"`
	Removed []map[string]any `json:"removed"`
}

// RecordedTemplate is the template recorded for the agent, "" for none.
func (a *Admin) RecordedTemplate(name string) string {
	b, err := os.ReadFile(filepath.Join(a.agentDir(name), TemplateFile))
	if err != nil {
		return ""
	}
	t := strings.TrimSpace(string(b))
	if host.CheckName(t) != nil {
		return ""
	}
	return t
}

func (a *Admin) recordTemplate(name, tmpl string) error {
	return host.WriteFile(filepath.Join(a.agentDir(name), TemplateFile), []byte(tmpl+"\n"), 0o644)
}

// Compare compares agents with their templates: the recorded one, or
// tmpl (only with one agent). Without names, every agent.
func (a *Admin) Compare(names []string, tmpl string) ([]Comparison, error) {
	if tmpl != "" && len(names) != 1 {
		return nil, errors.New("compare: --template needs exactly one agent")
	}
	if len(names) == 0 {
		var err error
		if names, err = a.List(); err != nil {
			return nil, err
		}
	}
	out := []Comparison{}
	for _, n := range names {
		if err := host.CheckName(n); err != nil {
			return nil, err
		}
		c := Comparison{Agent: n, Template: tmpl}
		rec := a.RecordedTemplate(n)
		if c.Template == "" {
			c.Template = rec
		}
		c.Recorded = c.Template != "" && c.Template == rec
		if c.Template == "" {
			c.Error = "no template recorded for agent " + n + ": name one with --template"
			out = append(out, c)
			continue
		}
		if err := a.compare(&c); err != nil {
			c.Error = err.Error()
		}
		out = append(out, c)
	}
	return out, nil
}

func (a *Admin) compare(c *Comparison) error {
	t, err := a.template(c.Template)
	if err != nil {
		return err
	}
	cur, err := os.ReadFile(filepath.Join(a.agentDir(c.Agent), "agent.yaml"))
	if err != nil {
		return err
	}
	want, err := a.templateConfig(c.Agent, t.Dir, cur)
	if err != nil {
		return err
	}
	d, err := lineDiff(cur, want)
	if err != nil {
		return err
	}
	c.Configuration = &ConfigDiff{Same: d == "", Diff: d}
	g, err := a.readGateway(c.Agent)
	if err != nil {
		return err
	}
	perms, err := a.templatePermissions(t.Dir)
	if err != nil {
		return fmt.Errorf("template %s: permissions.yaml: %w", c.Template, err)
	}
	c.Permissions = permDiff(g.Permissions, perms)
	return nil
}

// ApplyTemplate gives the agent its template's configuration (with the
// agent's own keys kept, agentKeys) and/or its permissions; its mode,
// promoted categories, owners and credentials stay. tmpl "" is the
// recorded template; a template named is recorded.
func (a *Admin) ApplyTemplate(name, tmpl string, configuration, permissions bool) error {
	if err := host.CheckName(name); err != nil {
		return err
	}
	if tmpl == "" {
		if tmpl = a.RecordedTemplate(name); tmpl == "" {
			return fmt.Errorf("no template recorded for agent %s: name one with --template", name)
		}
	}
	if !configuration && !permissions {
		configuration, permissions = true, true
	}
	t, err := a.template(tmpl)
	if err != nil {
		return err
	}
	var perms []map[string]any
	if permissions {
		if perms, err = a.templatePermissions(t.Dir); err != nil {
			return fmt.Errorf("template %s: permissions.yaml: %w", tmpl, err)
		}
		if err := a.checkEnabled(name, "apply-template "+tmpl, perms); err != nil {
			return fmt.Errorf("template %s: %w", tmpl, err)
		}
	}
	var did []string
	if configuration {
		cur, err := os.ReadFile(filepath.Join(a.agentDir(name), "agent.yaml"))
		if err != nil {
			return err
		}
		want, err := a.templateConfig(name, t.Dir, cur)
		if err != nil {
			return err
		}
		if err := a.edit(name, want); err != nil {
			return err
		}
		did = append(did, "configuration")
	}
	if permissions {
		d, err := a.readGateway(name)
		if err != nil {
			return err
		}
		d.Permissions = perms
		if err := a.writeGateway(name, d, false); err != nil {
			return err
		}
		did = append(did, fmt.Sprintf("%d permissions", len(perms)))
	}
	if err := a.recordTemplate(name, tmpl); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "agent %s: %s from template %s\n", name, strings.Join(did, " and "), tmpl)
	return nil
}

// templateConfig is the template's agent.yaml for the agent: the
// template's document, its comments kept, with the agent's own keys
// (agentKeys, and gateway's but servers and tools) taken from cur.
func (a *Admin) templateConfig(name, dir string, cur []byte) ([]byte, error) {
	tb, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		return nil, err
	}
	var tdoc, cdoc yaml.Node
	if err := yaml.Unmarshal(tb, &tdoc); err != nil || len(tdoc.Content) == 0 || tdoc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the template's agent.yaml is not a mapping: %v", err)
	}
	if err := yaml.Unmarshal(cur, &cdoc); err != nil || len(cdoc.Content) == 0 || cdoc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("agent %s's agent.yaml is not a mapping: %v", name, err)
	}
	tm, cm := tdoc.Content[0], cdoc.Content[0]
	for _, k := range agentKeys {
		setKey(tm, k, getKey(cm, k))
	}
	tg, cg := getKey(tm, "gateway"), getKey(cm, "gateway")
	switch {
	case tg == nil && cg != nil:
		// The template names no servers or tools: the agent's
		// connection without them.
		g := &yaml.Node{Kind: yaml.MappingNode}
		for i := 0; i+1 < len(cg.Content); i += 2 {
			if !slices.Contains(templateGatewayKeys, cg.Content[i].Value) {
				g.Content = append(g.Content, cg.Content[i], cg.Content[i+1])
			}
		}
		setKey(tm, "gateway", g)
	case tg != nil && tg.Kind == yaml.MappingNode:
		var keep []*yaml.Node
		for i := 0; i+1 < len(tg.Content); i += 2 {
			if slices.Contains(templateGatewayKeys, tg.Content[i].Value) {
				keep = append(keep, tg.Content[i], tg.Content[i+1])
			}
		}
		if cg != nil && cg.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(cg.Content); i += 2 {
				if !slices.Contains(templateGatewayKeys, cg.Content[i].Value) {
					keep = append(keep, cg.Content[i], cg.Content[i+1])
				}
			}
		}
		tg.Content = keep
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&tdoc); err != nil {
		return nil, err
	}
	out := WithName(buf.Bytes(), name)
	if _, err := config.Parse(out); err != nil {
		return nil, fmt.Errorf("the template's configuration for agent %s is not valid: %w", name, err)
	}
	return out, nil
}

func getKey(m *yaml.Node, k string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == k {
			return m.Content[i+1]
		}
	}
	return nil
}

// setKey sets k to v in m (v nil: removes k), in place or at the end.
func setKey(m *yaml.Node, k string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == k {
			if v == nil {
				m.Content = append(m.Content[:i], m.Content[i+2:]...)
			} else {
				m.Content[i+1] = v
			}
			return
		}
	}
	if v != nil {
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, v)
	}
}

// canonical is a YAML document without its comments, keys sorted.
func canonical(b []byte) (string, error) {
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n") + "\n", nil
}

// lineDiff is the agent's against the template's configuration, both
// canonical: "- " lines only the agent's has, "+ " only the template's,
// "  " both, diffContext of them around each change and "…" between the
// parts; "" when they are the same.
func lineDiff(cur, want []byte) (string, error) {
	x, err := canonical(cur)
	if err != nil {
		return "", err
	}
	y, err := canonical(want)
	if err != nil {
		return "", err
	}
	u, err := udiff.ToUnifiedDiff("", "", x, udiff.Lines(x, y), diffContext)
	if err != nil {
		return "", err
	}
	prefix := map[udiff.OpKind]string{udiff.Delete: "- ", udiff.Insert: "+ ", udiff.Equal: "  "}
	var b strings.Builder
	for i, h := range u.Hunks {
		if i > 0 {
			b.WriteString("…\n")
		}
		for _, l := range h.Lines {
			b.WriteString(prefix[l.Kind] + strings.TrimRight(l.Content, "\n") + "\n")
		}
	}
	return b.String(), nil
}

const diffContext = 2

func permKey(p map[string]any) string {
	b, _ := json.Marshal(p) // map keys sorted
	return string(b)
}

func permDiff(have, want []map[string]any) *PermissionDiff {
	count := func(ps []map[string]any) map[string]int {
		c := map[string]int{}
		for _, p := range ps {
			c[permKey(p)]++
		}
		return c
	}
	h, w := count(have), count(want)
	d := &PermissionDiff{Added: []map[string]any{}, Removed: []map[string]any{}}
	for _, p := range want {
		if k := permKey(p); h[k] > 0 {
			h[k]--
		} else {
			d.Added = append(d.Added, p)
		}
	}
	for _, p := range have {
		if k := permKey(p); w[k] > 0 {
			w[k]--
		} else {
			d.Removed = append(d.Removed, p)
		}
	}
	d.Same = len(d.Added) == 0 && len(d.Removed) == 0
	return d
}
