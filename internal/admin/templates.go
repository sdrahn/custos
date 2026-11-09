package admin

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/playbook"
)

// Agent templates (CONTRACT.md §14a): starting points for agents, each a
// directory with template.yaml, agent.yaml, permissions.yaml and
// optionally envelopes/<category>.yaml. The package's, then the
// administrator's: a directory in /etc replaces the one of the same name
// in /usr; one without template.yaml there hides it.
const (
	TemplatesVendor = "/usr/share/custos/templates"
	TemplatesSite   = "/etc/custos/templates"
)

// Template is one template, as agent-admin templates lists it.
type Template struct {
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`
	// System: classic, transactional or any (which systems it is for).
	System string `yaml:"system" json:"system"`
	// Mode: the mode an agent made from it starts in (advisory or
	// supervised; never bounded).
	Mode  string   `yaml:"mode" json:"mode"`
	Needs []string `yaml:"needs,omitempty" json:"needs,omitempty"`

	// From the template's files:
	Dir        string   `yaml:"-" json:"dir"`
	Servers    []string `yaml:"-" json:"servers"`
	Categories []string `yaml:"-" json:"categories,omitempty"`
	Envelopes  []string `yaml:"-" json:"envelopes,omitempty"`
	// Error: why the template cannot be used here (a category it names
	// is not in the catalog, say); then the rest may be missing.
	Error string `yaml:"-" json:"error,omitempty"`
}

// Templates is agent-admin templates' answer.
type Templates struct {
	// Transactional: this system's root file system is read-only
	// (transactional-update).
	Transactional bool                `json:"transactional"`
	Templates     map[string]Template `json:"templates"`
}

// ListTemplates reads the templates.
func (a *Admin) ListTemplates() (*Templates, error) {
	dirs := map[string]string{} // name → directory; /etc replaces /usr
	for _, root := range []string{TemplatesVendor, TemplatesSite} {
		es, err := os.ReadDir(a.p(root))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range es {
			if e.IsDir() {
				dirs[e.Name()] = filepath.Join(a.p(root), e.Name())
			}
		}
	}
	out := &Templates{Transactional: a.transactional(), Templates: map[string]Template{}}
	for name, dir := range dirs {
		if _, err := os.Stat(filepath.Join(dir, "template.yaml")); errors.Is(err, os.ErrNotExist) {
			continue // hidden by the administrator
		}
		t, err := a.loadTemplate(name, dir)
		if err != nil {
			out.Templates[name] = Template{Title: name, Dir: dir, Error: err.Error()}
			continue
		}
		out.Templates[name] = *t
	}
	return out, nil
}

// template finds one template.
func (a *Admin) template(name string) (*Template, error) {
	if !config.ValidName(name) {
		return nil, fmt.Errorf("%q is not a valid template name", name)
	}
	for _, root := range []string{TemplatesSite, TemplatesVendor} {
		dir := filepath.Join(a.p(root), name)
		if _, err := os.Stat(dir); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "template.yaml")); err != nil {
				break // hidden
			}
			return a.loadTemplate(name, dir)
		}
	}
	return nil, fmt.Errorf("no template %q (agent-admin templates lists them)", name)
}

// loadTemplate reads and checks a template's directory.
func (a *Admin) loadTemplate(name, dir string) (*Template, error) {
	bad := func(f string, err error) error { return fmt.Errorf("template %s: %s: %w", name, f, err) }
	if !config.ValidName(name) {
		return nil, fmt.Errorf("template %q: not a valid name", name)
	}
	var t Template
	b, err := os.ReadFile(filepath.Join(dir, "template.yaml"))
	if err != nil {
		return nil, bad("template.yaml", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, bad("template.yaml", err)
	}
	if strings.TrimSpace(t.Title) == "" {
		return nil, bad("template.yaml", errors.New("title missing"))
	}
	switch t.System {
	case "classic", "transactional", "any":
	default:
		return nil, bad("template.yaml", fmt.Errorf("system %q: want classic, transactional or any", t.System))
	}
	switch t.Mode {
	case "advisory", "supervised":
	default:
		return nil, bad("template.yaml", fmt.Errorf("mode %q: want advisory or supervised (an agent is never created bounded)", t.Mode))
	}
	t.Dir = dir
	cb, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		return nil, bad("agent.yaml", err)
	}
	cfg, err := config.Parse(cb)
	if err != nil {
		return nil, bad("agent.yaml", err)
	}
	if cfg.Name != name {
		return nil, bad("agent.yaml", fmt.Errorf("name %q: want the template's name", cfg.Name))
	}
	t.Servers = cfg.Gateway.Servers
	perms, err := a.templatePermissions(dir)
	if err != nil {
		return nil, bad("permissions.yaml", err)
	}
	cats := map[string]bool{}
	for _, p := range perms {
		if c, ok := p["category"].(string); ok {
			cats[c] = true
		}
	}
	for c := range cats {
		t.Categories = append(t.Categories, c)
	}
	sort.Strings(t.Categories)
	envs, _ := filepath.Glob(filepath.Join(dir, "envelopes", "*.yaml"))
	for _, f := range envs {
		c := strings.TrimSuffix(filepath.Base(f), ".yaml")
		if !cats[c] {
			return nil, bad("envelopes/"+filepath.Base(f), fmt.Errorf("no permission has category %s", c))
		}
		eb, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if _, err := ParseEnvelope(eb); err != nil {
			return nil, bad("envelopes/"+filepath.Base(f), err)
		}
		t.Envelopes = append(t.Envelopes, c)
	}
	return &t, nil
}

// templatePermissions reads and checks a template's permissions.yaml.
func (a *Admin) templatePermissions(dir string) ([]map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(dir, "permissions.yaml"))
	if err != nil {
		return nil, err
	}
	return a.parsePermissions(b)
}

// transactional reports whether the root file system is mounted
// read-only, as on a transactional system.
func (a *Admin) transactional() bool {
	f, err := os.Open(a.p("/proc/self/mounts"))
	if err != nil {
		return false
	}
	defer f.Close()
	ro := false
	s := bufio.NewScanner(f)
	for s.Scan() {
		x := strings.Fields(s.Text())
		if len(x) >= 4 && x[1] == "/" {
			ro = false
			for _, o := range strings.Split(x[3], ",") {
				if o == "ro" {
					ro = true
				}
			}
		}
	}
	return ro
}

var nameLine = regexp.MustCompile(`(?m)^name:[ \t]*\S+[ \t]*$`)

// WithName is a template's agent.yaml for an agent of another name.
func WithName(cfg []byte, name string) []byte {
	return nameLine.ReplaceAllLiteral(cfg, []byte("name: "+name))
}

// CreateFromTemplate installs a new agent from a template: its
// configuration from FILE when given (as edited in a control plane),
// otherwise the template's for NAME with the host's model defaults
// (Draft); then the template's permissions and
// mode. Categories stay supervised: promoting one is a separate,
// deliberate step (§6a).
func (a *Admin) CreateFromTemplate(name, tmpl, from string) error {
	if err := checkName(name); err != nil {
		return err
	}
	t, err := a.template(tmpl)
	if err != nil {
		return err
	}
	var cfg []byte
	if from != "" {
		if cfg, err = a.readFrom(from); err != nil {
			return err
		}
	} else if cfg, err = a.Draft(name, tmpl); err != nil {
		return err
	}
	perms, err := a.templatePermissions(t.Dir)
	if err != nil {
		return fmt.Errorf("template %s: permissions.yaml: %w", tmpl, err)
	}
	if err := a.checkEnabled(name, "create --template "+tmpl, perms); err != nil {
		return fmt.Errorf("template %s: %w", tmpl, err)
	}
	if err := a.create(name, cfg); err != nil {
		return err
	}
	d, err := a.readGateway(name)
	if err != nil {
		return err
	}
	d.Mode, d.Permissions = t.Mode, perms
	if err := a.writeGateway(name, d, false); err != nil {
		return fmt.Errorf("agent %s was created, but setting the template's permissions failed (agent-admin permissions %s < %s): %w",
			name, name, filepath.Join(t.Dir, "permissions.yaml"), err)
	}
	fmt.Fprintf(a.Out, "agent %s created from template %s: %s, %d permissions\n", name, tmpl, t.Mode, len(perms))
	return nil
}

// Playbooks are the diagnostic playbooks (§14c): the package's, replaced
// or hidden by the administrator's; a file that does not parse is listed
// with its error.
func (a *Admin) Playbooks() (map[string]*playbook.Playbook, error) { return playbook.Load(a.Root) }
