package admin

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/config"
)

// Defaults for new agents (CONTRACT.md §14b): the host's model, which a
// new agent's starting configuration (agent-admin draft, create
// --template) takes over. An agent keeps the model in its own agent.yaml:
// changing the defaults changes no existing agent.
const DefaultsFile = "/etc/custos/defaults.yaml"

// Defaults is /etc/custos/defaults.yaml.
type Defaults struct {
	Model *ModelDefaults `yaml:"model,omitempty" json:"model,omitempty"`
}

// ModelDefaults is the model block of agent.yaml (§14).
type ModelDefaults struct {
	Provider    string   `yaml:"provider" json:"provider"`
	Name        string   `yaml:"name" json:"name"`
	Endpoint    string   `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Credential  string   `yaml:"credential,omitempty" json:"credential,omitempty"`
	Temperature *float32 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	// TimeoutSeconds: model.timeout_seconds (0: none).
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	// MaxOutputTokens: model.max_output_tokens (0: none).
	MaxOutputTokens int `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	// ReasoningEffort: model.reasoning_effort ("": the server's default).
	ReasoningEffort string `yaml:"reasoning_effort,omitempty" json:"reasoning_effort,omitempty"`
}

func (m *ModelDefaults) config() config.Model {
	return config.Model{Provider: m.Provider, Name: m.Name, Endpoint: m.Endpoint, Credential: m.Credential, Temperature: m.Temperature,
		TimeoutSeconds: m.TimeoutSeconds, MaxOutputTokens: m.MaxOutputTokens, ReasoningEffort: m.ReasoningEffort}
}

// ParseDefaults reads and checks a defaults file.
func ParseDefaults(b []byte) (*Defaults, error) {
	var d Defaults
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("defaults: %w", err)
	}
	if d.Model != nil {
		if err := d.Model.config().Check(); err != nil {
			return nil, fmt.Errorf("defaults: %w", err)
		}
	}
	return &d, nil
}

// GetDefaults reads the host's defaults; none: empty.
func (a *Admin) GetDefaults() (*Defaults, error) {
	b, err := os.ReadFile(a.p(DefaultsFile))
	if errors.Is(err, os.ErrNotExist) {
		return &Defaults{}, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := ParseDefaults(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", DefaultsFile, err)
	}
	return d, nil
}

// SetDefaults replaces the host's defaults; an empty one removes them.
func (a *Admin) SetDefaults(b []byte) error {
	d, err := ParseDefaults(b)
	if err != nil {
		return err
	}
	if d.Model != nil && config.LooksLikeSecret(d.Model.Credential) {
		// The file is everyone's to read (agent-admin draft as anyone).
		return fmt.Errorf("defaults: %s", config.SecretAsName)
	}
	if d.Model == nil {
		if err := os.Remove(a.p(DefaultsFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Fprintln(a.Out, "no model defaults: new agents get the template's model")
		return nil
	}
	var out bytes.Buffer
	out.WriteString("# Defaults for new agents (CONTRACT.md §14b), written by agent-admin defaults.\n" +
		"# A new agent's configuration takes them over; existing agents keep theirs.\n")
	out.Write(renderModel(d.Model, ""))
	if err := writeFile(a.p(DefaultsFile), out.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "model defaults: %s %s\n", d.Model.Provider, d.Model.Name)
	return nil
}

// renderModel writes a model block, with a comment after "model:".
func renderModel(m *ModelDefaults, comment string) []byte {
	var b strings.Builder
	b.WriteString("model:")
	if comment != "" {
		b.WriteString("  # " + comment)
	}
	b.WriteString("\n")
	kv := func(k, v string) {
		if v != "" {
			b.WriteString("  " + k + ": " + scalar(v) + "\n")
		}
	}
	kv("provider", m.Provider)
	kv("name", m.Name)
	kv("endpoint", m.Endpoint)
	kv("credential", m.Credential)
	if m.Temperature != nil {
		b.WriteString("  temperature: " + strconv.FormatFloat(float64(*m.Temperature), 'f', -1, 32) + "\n")
	}
	if m.TimeoutSeconds > 0 {
		b.WriteString("  timeout_seconds: " + strconv.Itoa(m.TimeoutSeconds) + "\n")
	}
	if m.MaxOutputTokens > 0 {
		b.WriteString("  max_output_tokens: " + strconv.Itoa(m.MaxOutputTokens) + "\n")
	}
	kv("reasoning_effort", m.ReasoningEffort)
	return []byte(b.String())
}

// scalar is v as a YAML scalar, quoted when it needs to be.
func scalar(v string) string {
	out, err := yaml.Marshal(v)
	if err != nil {
		return strconv.Quote(v)
	}
	return strings.TrimSuffix(string(out), "\n")
}

// modelBlock finds the top-level "model:" entry: its line and the
// indented lines after it.
var modelBlock = regexp.MustCompile(`(?m)^model:.*\n(?:[ \t]+.*\n)*`)

// WithModel is a configuration with its model block replaced by m.
func WithModel(cfg []byte, m *ModelDefaults) []byte {
	block := renderModel(m, "the host's defaults ("+DefaultsFile+")")
	if loc := modelBlock.FindIndex(cfg); loc != nil {
		return append(append(append([]byte{}, cfg[:loc[0]]...), block...), cfg[loc[1]:]...)
	}
	// None: after name (and description).
	at := nameLine.FindIndex(cfg)
	if at == nil {
		return append(append([]byte{}, block...), cfg...)
	}
	end := at[1] + 1
	if d := regexp.MustCompile(`(?m)^description:.*\n`).FindIndex(cfg[end:]); d != nil && d[0] == 0 {
		end += d[1]
	}
	if end > len(cfg) {
		end = len(cfg)
	}
	return append(append(append([]byte{}, cfg[:end]...), block...), cfg[end:]...)
}

// blank is a new agent's starting configuration without a template.
const blank = `version: 1
name: NAME
description: What this agent is for.
model:
  provider: openai              # an on-premises OpenAI-compatible server
  name: qwen3-32b
  endpoint: http://127.0.0.1:8000/v1
instruction: |
  Say what the agent does, and that it looks before it changes anything.
gateway:
  servers: [zypp]
limits:
  max_llm_calls: 25
  max_tool_calls: 40
  max_invocation_seconds: 1800
interaction:
  agui: true
  client_tools: deny
`

// Draft is a new agent's starting configuration: a template's, or a
// blank one, named name, with the host's model defaults.
func (a *Admin) Draft(name, tmpl string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	cfg := []byte(blank)
	if tmpl != "" {
		t, err := a.template(tmpl)
		if err != nil {
			return nil, err
		}
		if cfg, err = os.ReadFile(filepath.Join(t.Dir, "agent.yaml")); err != nil {
			return nil, err
		}
	}
	cfg = WithName(cfg, name)
	d, err := a.GetDefaults()
	if err != nil {
		return nil, err
	}
	if d.Model != nil {
		cfg = WithModel(cfg, d.Model)
	}
	if _, err := config.Parse(cfg); err != nil {
		return nil, fmt.Errorf("the starting configuration is not valid: %w", err)
	}
	return cfg, nil
}
