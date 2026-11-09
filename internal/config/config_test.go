package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `
version: 1
name: patch-pilot
model: {provider: openai, name: qwen3-32b, endpoint: "https://ai.example/v1"}
instruction: "Keep the host patched."
gateway: {servers: [zypp, systemd], tools: [zypp__list_updates]}
interaction: {agui: true, client_tools: {allow: [confirm_window]}}
`

func TestParse(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.Gateway.Socket != GatewaySocket || c.Limits.MaxToolCalls != 40 || c.Sessions.RetentionDays != 30 {
		t.Errorf("defaults not applied: %+v", c)
	}
	if !c.Interaction.ClientTools.Allowed("confirm_window") || c.Interaction.ClientTools.Allowed("x") {
		t.Error("client_tools allow list")
	}
	if !c.ShowsTool("zypp__list_updates") || c.ShowsTool("zypp__install") || c.ShowsTool("fs__read") {
		t.Error("ShowsTool")
	}
	// No timeout of its own by default; one when given.
	if c.Model.Timeout() != 0 {
		t.Errorf("default timeout %s", c.Model.Timeout())
	}
	c2, err := Parse([]byte(strings.Replace(sample, `endpoint: "https://ai.example/v1"}`, `endpoint: "https://ai.example/v1", timeout_seconds: 1800, max_output_tokens: 2048}`, 1)))
	if err != nil || c2.Model.Timeout().Minutes() != 30 || c2.Model.MaxOutputTokens != 2048 {
		t.Errorf("timeout_seconds, max_output_tokens: %v %v", c2, err)
	}
	if c.Model.MaxOutputTokens != 0 {
		t.Errorf("default max_output_tokens %d", c.Model.MaxOutputTokens)
	}
}

func TestParseErrors(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"version":      {"version: 2\nname: ab\nmodel: {provider: vertex, name: m}\ngateway: {servers: [a]}", "version: 2"},
		"name":         {"version: 1\nname: Bad\nmodel: {provider: vertex, name: m}\ngateway: {servers: [a]}", "name:"},
		"unknown key":  {"version: 1\nname: ab\nmodel: {provider: vertex, name: m}\ngateway: {servers: [a]}\nlimitz: {}", "limitz"},
		"no servers":   {"version: 1\nname: ab\nmodel: {provider: vertex, name: m}", "gateway.servers"},
		"tool server":  {"version: 1\nname: ab\nmodel: {provider: vertex, name: m}\ngateway: {servers: [a], tools: [b__x]}", `gateway.tools: "b__x": its server "b" is not in gateway.servers: add b to gateway.servers`},
		"gemini key":   {"version: 1\nname: ab\nmodel: {provider: gemini, name: m}\ngateway: {servers: [a]}", "API key"},
		"client tools": {"version: 1\nname: ab\nmodel: {provider: vertex, name: m}\ngateway: {servers: [a]}\ninteraction: {client_tools: allow}", "client_tools"},
		"timeout":      {"version: 1\nname: ab\nmodel: {provider: vertex, name: m, timeout_seconds: -1}\ngateway: {servers: [a]}", "model.timeout_seconds: -1"},
		"max output":   {"version: 1\nname: ab\nmodel: {provider: vertex, name: m, max_output_tokens: -1}\ngateway: {servers: [a]}", "model.max_output_tokens: -1"},
	} {
		_, err := Parse([]byte(tc.yaml))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "patch-pilot")
	os.Mkdir(dir, 0o755)
	y := strings.Replace(sample, `instruction: "Keep the host patched."`, "instruction_file: instruction.md", 1)
	os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(y), 0o644)
	os.WriteFile(filepath.Join(dir, "instruction.md"), []byte("Be careful."), 0o644)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Instruction != "Be careful." {
		t.Errorf("instruction = %q", c.Instruction)
	}
	other := filepath.Join(filepath.Dir(dir), "other")
	os.Rename(dir, other)
	if _, err := Load(other); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("name/dir mismatch: %v", err)
	}
}

func TestSandbox(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if l := c.Lifecycle(); l["kind"] != "systemd" || l["service"] != "agent@patch-pilot.service" || l["socket"] != "agent@patch-pilot.socket" {
		t.Errorf("default lifecycle %v", l)
	}
	osh := strings.Replace(sample, "gateway: {servers: [zypp, systemd], tools: [zypp__list_updates]}", "gateway: {url: \"https://host.openshell.internal:8443/mcp\", token_env: AGENT_GATEWAY_TOKEN, servers: [zypp]}", 1)
	c, err = Parse([]byte(osh + "sandbox: {kind: openshell}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l := c.Lifecycle(); l["kind"] != "openshell" || l["sandbox"] != "agent-patch-pilot" || l["gateway"] != "openshell" || l["service"] != "agent-openshell@patch-pilot.service" {
		t.Errorf("openshell lifecycle %v", l)
	}
	for _, bad := range []string{"sandbox: {kind: docker}\n", "sandbox: {kind: systemd, openshell_gateway: x}\n"} {
		if _, err := Parse([]byte(sample + bad)); err == nil || !strings.Contains(err.Error(), "sandbox.") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestGatewayURL(t *testing.T) {
	base := "version: 1\nname: ab\nmodel: {provider: vertex, name: m}\n"
	for gw, want := range map[string]string{
		"gateway: {servers: [a], url: \"https://gw:8443/mcp\"}":                            "",
		"gateway: {servers: [a], url: \"http://127.0.0.1:8443/mcp\", token_env: GW_TOKEN}": "",
		"gateway: {servers: [a], url: \"http://gw:8443/mcp\"}":                             "want an https URL",
		"gateway: {servers: [a], url: \"https://gw/mcp\", socket: /run/x.sock}":            "not both",
		"gateway: {servers: [a], url: \"https://gw/mcp\", token_env: \"bad name\"}":        "environment variable",
		"gateway: {servers: [a], token_env: GW_TOKEN}":                                     "only with gateway.url",
		"gateway: {servers: [a]}\nsandbox: {kind: openshell}":                              "reaches the gateway over HTTPS",
	} {
		c, err := Parse([]byte(base + gw + "\n"))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", gw, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: %v, want %q", gw, err, want)
		case want == "" && c.Gateway.Socket != "":
			t.Errorf("%s: socket defaulted along with url", gw)
		}
	}
}

func TestGatewayOAuth(t *testing.T) {
	base := "version: 1\nname: ab\nmodel: {provider: vertex, name: m}\n"
	osh := "\nsandbox: {kind: openshell}\n"
	for gw, want := range map[string]string{
		"gateway: {servers: [a], url: \"https://gw/mcp\", oauth: {token_url: \"https://idp/token\", client_id: agent-ab, scopes: [mcp]}}" + osh:                     "",
		"gateway: {servers: [a], url: \"https://gw/mcp\", oauth: {token_url: \"http://127.0.0.1:1/token\", client_id: agent-ab, refresh_before_seconds: 60}}" + osh: "",
		"gateway: {servers: [a], url: \"https://gw/mcp\", oauth: {token_url: \"http://idp/token\", client_id: agent-ab}}" + osh:                                     "want an https URL",
		"gateway: {servers: [a], url: \"https://gw/mcp\", oauth: {token_url: \"https://idp/token\"}}" + osh:                                                         "client_id",
		"gateway: {servers: [a], url: \"https://gw/mcp\", oauth: {token_url: \"https://idp/token\", client_id: agent-ab, scopes: [\"a b\"]}}" + osh:                 "scope names",
		"gateway: {servers: [a], url: \"https://gw/mcp\", oauth: {token_url: \"https://idp/token\", client_id: agent-ab, refresh_before_seconds: -1}}" + osh:        "0 or more",
		"gateway: {servers: [a], url: \"https://gw/mcp\", oauth: {token_url: \"https://idp/token\", client_id: agent-ab, secret: x}}" + osh:                         "secret",
		"gateway: {servers: [a], url: \"https://gw/mcp\", oauth: {token_url: \"https://idp/token\", client_id: agent-ab}}\n":                                        "only with sandbox kind openshell",
	} {
		c, err := Parse([]byte(base + gw))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", gw, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: %v, want %q", gw, err, want)
		case want == "" && (c.Gateway.OAuth == nil || c.Gateway.OAuth.ClientID != "agent-ab"):
			t.Errorf("%s: oauth %+v", gw, c.Gateway.OAuth)
		}
	}
}

// An agent's events (§14): units whose failure starts an activity, never
// Custos's own, and no template instances; a prompt or a playbook.
func TestEvents(t *testing.T) {
	c, err := Parse([]byte(sample + `events:
  - {name: web-down, on_unit_failure: [nginx.service, php-fpm.service], prompt: "{unit} failed."}
  - {name: db, on_unit_failure: [postgresql.service], playbook: service-health, min_interval_seconds: 60}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Events) != 2 || c.Events[0].MinInterval().Seconds() != DefaultEventInterval || c.Events[1].MinInterval().Seconds() != 60 {
		t.Errorf("events %+v", c.Events)
	}
	for name, tc := range map[string]struct{ events, want string }{
		"no units":     {"[{name: e, prompt: x}]", "events[0].on_unit_failure: want 1 to 32"},
		"own unit":     {"[{name: e, prompt: x, on_unit_failure: [agent@x.service]}]", "Custos's own units"},
		"own unit 2":   {"[{name: e, prompt: x, on_unit_failure: [custos-demote.service]}]", "Custos's own units"},
		"template":     {"[{name: e, prompt: x, on_unit_failure: [getty@tty1.service]}]", "no template instance"},
		"not a unit":   {"[{name: e, prompt: x, on_unit_failure: [nginx]}]", "want a unit name"},
		"twice":        {"[{name: e, prompt: x, on_unit_failure: [a.service, a.service]}]", "twice"},
		"no prompt":    {"[{name: e, on_unit_failure: [a.service]}]", "events[0].prompt: missing"},
		"bad name":     {"[{name: E_1, prompt: x, on_unit_failure: [a.service]}]", "events[0].name"},
		"same name":    {"[{name: e, prompt: x, on_unit_failure: [a.service]}, {name: e, prompt: x, on_unit_failure: [b.service]}]", "events[1].name"},
		"interval":     {"[{name: e, prompt: x, on_unit_failure: [a.service], min_interval_seconds: -5}]", "min_interval_seconds"},
		"bad playbook": {"[{name: e, playbook: ../x, on_unit_failure: [a.service]}]", "not a playbook name"},
	} {
		_, err := Parse([]byte(sample + "events: " + tc.events + "\n"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}
