package openshell

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/config"
)

func cfg(t *testing.T) *config.Config {
	c, err := config.Parse([]byte(`version: 1
name: pp
model: {provider: openai, name: m, endpoint: "http://127.0.0.1:8008/v1"}
instruction: Be careful.
gateway: {url: "https://gw.example.com:8443/mcp", servers: [zypp]}
sandbox: {kind: openshell}
`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPolicy(t *testing.T) {
	b, err := Policy(cfg(t))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Version          int
		Process          map[string]string
		Landlock         map[string]string
		Network_policies map[string]struct {
			Endpoints []map[string]any
			Binaries  []map[string]string
		} `yaml:"network_policies"`
	}
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.Version != 1 || p.Process["run_as_user"] != "sandbox" || p.Landlock["compatibility"] != "hard_requirement" {
		t.Errorf("policy %s", b)
	}
	if _, ok := p.Network_policies["mcp_gateway"]; ok {
		t.Error("the gateway's rule belongs to the provider profile; OpenShell refuses it twice")
	}
	m := p.Network_policies["model"].Endpoints[0]
	if m["host"] != HostAlias || m["port"] != 8008 {
		t.Errorf("model on loopback must be the host's: %v", m)
	}
	if len(p.Network_policies) != 1 {
		t.Errorf("only the model (the gateway comes with the provider): %v", p.Network_policies)
	}
	if p.Network_policies["model"].Binaries[0]["path"] != Launcher {
		t.Error("binaries")
	}
}

func TestProfile(t *testing.T) {
	b, err := Profile(cfg(t))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"id: custos-gateway-pp", "AGENT_GATEWAY_TOKEN", "auth_style: bearer", "host: gw.example.com", "port: 8443", "allow_all_known_mcp_methods: true", "- \"2025-06-18\"", "protocol: mcp", "- " + Launcher} {
		if !strings.Contains(s, want) {
			t.Errorf("profile lacks %q:\n%s", want, s)
		}
	}
}

func TestCreateArgs(t *testing.T) {
	const secret = "s3cret-0123456789abcdef0123456789"
	args, env, err := CreateArgs(cfg(t), "registry.example/agent:1", "/etc/p.yaml", secret, 47123)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(args, " ")
	for _, want := range []string{"sandbox create --name agent-pp --from registry.example/agent:1", "--policy /etc/p.yaml", "--provider agent-pp-gateway",
		"--env-from CUSTOS_RELAY_SECRET --env-from CUSTOS_CONFIG", "--detach -- " + Launcher + " serve --config-env CUSTOS_CONFIG --listen-tcp 127.0.0.1:47123"} {
		if !strings.Contains(s, want) {
			t.Errorf("args lack %q: %s", want, s)
		}
	}
	// The secret and the configuration never on the command line, which
	// any local user reads in /proc (security review H2): in the
	// environment the CLI passes on with --env-from.
	if strings.Contains(s, secret) || strings.Contains(s, ConfigEnv+"=") || strings.Contains(s, SecretEnv+"=") {
		t.Errorf("a secret or the configuration on the command line: %s", s)
	}
	if !slices.Contains(env, SecretEnv+"="+secret) {
		t.Errorf("env %v", env)
	}
	var enc string
	for _, a := range env {
		if v, ok := strings.CutPrefix(a, ConfigEnv+"="); ok {
			enc = v
		}
	}
	b, _ := base64.StdEncoding.DecodeString(enc)
	c, err := config.Parse(b)
	if err != nil {
		t.Fatalf("the sandbox's config does not parse: %v\n%s", err, b)
	}
	if c.Model.Endpoint != "http://host.openshell.internal:8008/v1" {
		t.Errorf("model endpoint in the sandbox: %s", c.Model.Endpoint)
	}
	if c.Gateway.TokenEnv != TokenEnv || c.Gateway.Socket != "" || c.Instruction != "Be careful." || c.Sandbox.Kind != "openshell" {
		t.Errorf("sandbox config %+v", c)
	}
}

func TestPort(t *testing.T) {
	if Port("pp") != Port("pp") || Port("pp") < 47000 || Port("pp") > 47999 || Port("pp") == Port("pq") {
		t.Errorf("ports %d %d", Port("pp"), Port("pq"))
	}
}

func oauthCfg(t *testing.T) *config.Config {
	c, err := config.Parse([]byte(`version: 1
name: pp
model: {provider: openai, name: m, endpoint: "http://127.0.0.1:8008/v1"}
instruction: Be careful.
gateway:
  url: "https://gw.example.com:8443/mcp"
  servers: [zypp]
  oauth: {token_url: "https://idp.example.com/token", client_id: agent-pp, scopes: [mcp], refresh_before_seconds: 60}
sandbox: {kind: openshell}
`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// With an OAuth client, OpenShell's gateway mints and renews the token.
func TestProfileRefresh(t *testing.T) {
	b, err := Profile(oauthCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Credentials []struct {
			EnvVars []string `yaml:"env_vars"`
			Refresh struct {
				Strategy      string
				TokenURL      string `yaml:"token_url"`
				Scopes        []string
				RefreshBefore string `yaml:"refresh_before"`
				Material      []struct {
					Name     string
					Required bool
					Secret   bool
				}
			}
		}
	}
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	r := p.Credentials[0].Refresh
	if p.Credentials[0].EnvVars[0] != TokenEnv || r.Strategy != "oauth2_client_credentials" || r.TokenURL != "https://idp.example.com/token" ||
		len(r.Scopes) != 1 || r.Scopes[0] != "mcp" || r.RefreshBefore != "60s" {
		t.Errorf("refresh %+v\n%s", r, b)
	}
	secret := map[string]bool{}
	for _, m := range r.Material {
		if !m.Required {
			t.Errorf("material %s not required", m.Name)
		}
		secret[m.Name] = m.Secret
	}
	if len(secret) != 2 || secret["client_id"] || !secret["client_secret"] {
		t.Errorf("material: %v", secret)
	}
	// A static token has no refresh.
	b, _ = Profile(cfg(t))
	if strings.Contains(string(b), "refresh") {
		t.Errorf("static token profile with refresh:\n%s", b)
	}
}

// The sandbox does not learn the agent's OAuth client.
func TestSandboxConfigWithoutOAuth(t *testing.T) {
	b, err := SandboxConfig(oauthCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "oauth") || strings.Contains(string(b), "idp.example.com") {
		t.Errorf("sandbox config has the OAuth client:\n%s", b)
	}
}
