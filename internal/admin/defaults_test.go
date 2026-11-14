package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/config"
)

func TestDefaults(t *testing.T) {
	a, _ := withTemplates(t)
	d, err := a.GetDefaults()
	if err != nil || d.Model != nil {
		t.Fatalf("no file: %+v %v", d, err)
	}
	for _, c := range []struct{ in, want string }{
		{"model: {provider: openai, name: m}\n", "model.endpoint"},
		{"model: {provider: gemini, name: g}\n", "provider gemini needs an API key credential"},
		{"model: {provider: openai, name: m, endpoint: 'http://x/v1', key: 1}\n", "field key not found"},
		{"modle: {}\n", "field modle not found"},
	} {
		if err := a.SetDefaults([]byte(c.in)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err %v, want %q", c.in, err, c.want)
		}
	}
	in := "model:\n  provider: openai\n  name: granite-4\n  endpoint: 'http://ai.example.com:8000/v1'\n  credential: model-key\n  temperature: 0.2\n  timeout_seconds: 1800\n  max_output_tokens: 2048\n  reasoning_effort: none\n"
	if err := a.SetDefaults([]byte(in)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.Path(DefaultsFile))
	if !strings.HasPrefix(string(b), "# Defaults for new agents") || !strings.Contains(string(b), "  temperature: 0.2\n") ||
		!strings.Contains(string(b), "  timeout_seconds: 1800\n") || !strings.Contains(string(b), "  max_output_tokens: 2048\n") || !strings.Contains(string(b), "  reasoning_effort: none\n") {
		t.Errorf("file:\n%s", b)
	}
	d, err = a.GetDefaults()
	if err != nil || d.Model.Name != "granite-4" || d.Model.Credential != "model-key" || *d.Model.Temperature != 0.2 || d.Model.TimeoutSeconds != 1800 || d.Model.MaxOutputTokens != 2048 || d.Model.ReasoningEffort != "none" {
		t.Fatalf("%+v %v", d.Model, err)
	}

	// Every template's draft gets the defaults, and stays valid.
	l, _ := a.ListTemplates()
	for name := range l.Templates {
		cfg, err := a.Draft("x-"+name[:3], name)
		if err != nil {
			t.Fatalf("draft %s: %v", name, err)
		}
		c, err := config.Parse(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if c.Name != "x-"+name[:3] || c.Model.Name != "granite-4" || c.Model.Endpoint != "http://ai.example.com:8000/v1" ||
			c.Model.Credential != "model-key" || c.Instruction == "" || len(c.Gateway.Tools) == 0 {
			t.Errorf("draft %s: %+v", name, c)
		}
		if strings.Contains(string(cfg), "qwen3") || !strings.Contains(string(cfg), "# Agent template "+name) {
			t.Errorf("draft %s:\n%s", name, cfg)
		}
	}
	blankCfg, err := a.Draft("mine", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blankCfg), "\nname: mine\n") || !strings.Contains(string(blankCfg), "name: granite-4") {
		t.Errorf("blank:\n%s", blankCfg)
	}

	// create --template without --from takes the defaults; an existing
	// agent keeps its model when they change.
	if err := a.CreateFromTemplate("doc", "service-doctor", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDefaults([]byte("model: {provider: gemini, name: gemini-2.5-flash, credential: gkey}\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(a.Path("/etc/custos/agents/doc/agent.yaml")); !strings.Contains(string(b), "granite-4") {
		t.Errorf("agent.yaml:\n%s", b)
	}
	if cfg, _ := a.Draft("g1", "triage"); !strings.Contains(string(cfg), "provider: gemini") || strings.Contains(string(cfg), "endpoint") {
		t.Errorf("gemini draft:\n%s", cfg)
	}

	// Cleared: the template's own model.
	if err := a.SetDefaults(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path(DefaultsFile)); !os.IsNotExist(err) {
		t.Error("defaults file not removed")
	}
	if cfg, _ := a.Draft("q1", "triage"); !strings.Contains(string(cfg), "qwen3-32b") {
		t.Errorf("draft:\n%s", cfg)
	}
	if _, err := a.Draft("q1", "no-such"); err == nil {
		t.Error("draft of no template")
	}
}

func TestWithModel(t *testing.T) {
	m := &ModelDefaults{Provider: "openai", Name: "m: 1", Endpoint: "http://h/v1"}
	for _, c := range []struct{ in, want string }{
		// A block, kept in place; the key after it untouched.
		{"version: 1\nname: a\nmodel:\n  provider: gemini   # x\n  name: g\n\ninstruction: |\n  hi\n",
			"version: 1\nname: a\nmodel:  # the host's defaults (/etc/custos/defaults.yaml)\n  provider: openai\n  name: 'm: 1'\n  endpoint: http://h/v1\n\ninstruction: |\n  hi\n"},
		// Flow style.
		{"name: a\nmodel: {provider: gemini, name: g}\ngateway: {servers: [x]}\n",
			"name: a\nmodel:  # the host's defaults (/etc/custos/defaults.yaml)\n  provider: openai\n  name: 'm: 1'\n  endpoint: http://h/v1\ngateway: {servers: [x]}\n"},
		// None: after name and description.
		{"version: 1\nname: a\ndescription: d\ngateway: {}\n",
			"version: 1\nname: a\ndescription: d\nmodel:  # the host's defaults (/etc/custos/defaults.yaml)\n  provider: openai\n  name: 'm: 1'\n  endpoint: http://h/v1\ngateway: {}\n"},
	} {
		if got := string(WithModel([]byte(c.in), m)); got != c.want {
			t.Errorf("in\n%s\ngot\n%s\nwant\n%s", c.in, got, c.want)
		}
	}
}

func TestDefaultsFileIsWorldReadable(t *testing.T) {
	a, _ := newAdmin(t)
	if err := a.SetDefaults([]byte("model: {provider: openai, name: m, endpoint: 'http://127.0.0.1:1/v1'}\n")); err != nil {
		t.Fatal(err)
	}
	// draft and the page read it without root: it names no secret.
	st, err := os.Stat(a.Path(DefaultsFile))
	if err != nil || st.Mode().Perm() != 0o644 || filepath.Base(DefaultsFile) != "defaults.yaml" {
		t.Errorf("%v %v", st.Mode(), err)
	}
}
