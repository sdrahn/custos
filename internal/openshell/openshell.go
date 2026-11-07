// Package openshell turns an agent's configuration into what NVIDIA
// OpenShell needs to run it (CONTRACT.md §3a, §16a): the sandbox policy,
// the provider profile for mcp-gateway's token, and the arguments of
// `openshell sandbox create`.
//
// The agent may reach two places: mcp-gateway's HTTPS listener (with the
// token the supervisor adds) and its model endpoint. Everything else is
// denied by OpenShell's default. Services on the host are reached as
// host.openshell.internal.
package openshell

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/config"
)

// Paths inside the sandbox image (packaging/openshell/Containerfile).
const (
	Launcher  = "/usr/libexec/custos/agent-launcher"
	Workdir   = "/sandbox"
	StateDir  = "/sandbox/state"
	TokenEnv  = "AGENT_GATEWAY_TOKEN"
	SecretEnv = "CUSTOS_RELAY_SECRET"
	ConfigEnv = "CUSTOS_CONFIG"
)

// Port is the agent's control API port: inside the sandbox (loopback)
// and on the host, where `openshell forward` makes it reachable to the
// relay. Stable per name, in 47000–47999.
func Port(agent string) int {
	h := uint32(2166136261)
	for i := 0; i < len(agent); i++ {
		h = (h ^ uint32(agent[i])) * 16777619
	}
	return 47000 + int(h%1000)
}

// HostAlias is the name a sandbox reaches the host by.
const HostAlias = "host.openshell.internal"

// Names of an agent's OpenShell objects.
func SandboxName(agent string) string  { return "agent-" + agent }
func ProviderName(agent string) string { return "agent-" + agent + "-gateway" }
func ProfileID(agent string) string    { return "custos-gateway-" + agent }

type endpoint struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Path        string `yaml:"path,omitempty"`
	Protocol    string `yaml:"protocol"`
	Enforcement string `yaml:"enforcement"`
	Access      string `yaml:"access,omitempty"`
	// MCP: OpenShell inspects the traffic as MCP; which tools are allowed
	// is the gateway's decision, so every known MCP method may pass.
	MCP        map[string]any `yaml:"mcp,omitempty"`
	Credential *struct {
		Provider string `yaml:"provider"`
	} `yaml:"credential_binding,omitempty"`
}

type rule struct {
	Name      string     `yaml:"name"`
	Endpoints []endpoint `yaml:"endpoints"`
	Binaries  []binary   `yaml:"binaries"`
}

type binary struct {
	Path string `yaml:"path"`
}

// Policy is the agent's sandbox policy (OpenShell policy schema v1).
// The agent runs as OpenShell's unprivileged sandbox user, which owns the
// sandbox's working directory (where its state lives).
func Policy(cfg *config.Config) ([]byte, error) {
	if _, err := hostPort(cfg.Gateway.URL); err != nil {
		return nil, fmt.Errorf("gateway.url: %w", err)
	}
	// The gateway's rule comes with the provider (Profile): OpenShell adds
	// the profile's endpoint, with the credential bound, when the provider
	// is attached, and refuses a second rule for the same endpoint.
	rules := map[string]rule{}
	if cfg.Model.Provider == "openai" {
		m, err := hostPort(cfg.Model.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("model.endpoint: %w", err)
		}
		rules["model"] = rule{Name: "model", Endpoints: []endpoint{{Host: m.host, Port: m.port, Protocol: "rest", Enforcement: "enforce", Access: "full"}}, Binaries: []binary{{Launcher}}}
	} else {
		rules["model"] = rule{Name: "model", Endpoints: []endpoint{
			{Host: "generativelanguage.googleapis.com", Port: 443, Protocol: "rest", Enforcement: "enforce", Access: "full"},
			{Host: "*-aiplatform.googleapis.com", Port: 443, Protocol: "rest", Enforcement: "enforce", Access: "full"},
		}, Binaries: []binary{{Launcher}}}
	}
	p := map[string]any{
		"version": 1,
		"filesystem_policy": map[string]any{
			"include_workdir": true,
			"read_only":       []string{"/usr", "/lib", "/lib64", "/etc"},
		},
		"landlock":         map[string]any{"compatibility": "hard_requirement"},
		"process":          map[string]any{"run_as_user": "sandbox", "run_as_group": "sandbox"},
		"network_policies": rules,
	}
	return yaml.Marshal(p)
}

// Profile is the provider profile that binds the gateway token to the
// gateway's endpoint only. Attaching the provider adds this endpoint to
// the sandbox's network policy, for agent-launcher only.
func Profile(cfg *config.Config) ([]byte, error) {
	gw, err := hostPort(cfg.Gateway.URL)
	if err != nil {
		return nil, fmt.Errorf("gateway.url: %w", err)
	}
	cred := map[string]any{
		"name": "gateway_token", "description": "OAuth access token whose subject is agent-" + cfg.Name,
		"env_vars": []string{TokenEnv}, "required": true, "auth_style": "bearer", "header_name": "authorization",
	}
	if r := Refresh(cfg); r != nil {
		cred["refresh"] = r
	}
	return yaml.Marshal(map[string]any{
		"id":           ProfileID(cfg.Name),
		"display_name": "mcp-gateway for agent " + cfg.Name,
		"description":  "Bearer token of agent-" + cfg.Name + " for mcp-gateway (custos)",
		"category":     "data",
		"credentials":  []map[string]any{cred},
		"endpoints":    []map[string]any{{"host": gw.host, "port": gw.port, "protocol": "mcp", "enforcement": "enforce", "mcp": allMCP}},
		"binaries":     []string{Launcher},
	})
}

// Refresh is the gateway token's refresh metadata when the agent has an
// OAuth client (gateway.oauth): OpenShell's gateway mints its access
// tokens with the client credentials grant and renews them before they
// expire, and the sandbox's placeholder resolves to the current one, so
// the agent keeps running across renewals (CONTRACT.md §16a). The token
// endpoint is the profile's; the client id and secret are the provider's
// refresh material (agent-admin set-credential NAME gateway-client-secret).
func Refresh(cfg *config.Config) map[string]any {
	o := cfg.Gateway.OAuth
	if o == nil {
		return nil
	}
	r := map[string]any{
		"strategy":  "oauth2_client_credentials",
		"token_url": o.TokenURL,
		"material": []map[string]any{
			{"name": "client_id", "description": "OAuth client id of agent-" + cfg.Name, "required": true, "secret": false},
			{"name": "client_secret", "description": "OAuth client secret of agent-" + cfg.Name, "required": true, "secret": true},
		},
	}
	if len(o.Scopes) > 0 {
		r["scopes"] = o.Scopes
	}
	if o.RefreshBefore > 0 {
		r["refresh_before"] = fmt.Sprintf("%ds", o.RefreshBefore)
	}
	return r
}

// SandboxConfig is the agent's configuration as the sandbox gets it: the
// instruction inline, the gateway by URL with the token variable.
func SandboxConfig(cfg *config.Config) ([]byte, error) {
	c := *cfg
	c.InstructionFile = ""
	c.Gateway.Socket = ""
	c.Gateway.TokenEnv = TokenEnv
	// The token's issuer is OpenShell's business, not the agent's.
	c.Gateway.OAuth = nil
	// The host's loopback is the sandbox's host.openshell.internal.
	c.Gateway.URL = hostURL(c.Gateway.URL)
	if c.Model.Endpoint != "" {
		c.Model.Endpoint = hostURL(c.Model.Endpoint)
	}
	return yaml.Marshal(&c)
}

// CreateArgs are the arguments of `openshell` that create the sandbox,
// and the environment to run it with: the relay secret and the
// configuration go into the sandbox with --env-from, from the CLI's
// environment, never on its command line, which any local user can read
// in /proc (security review H2).
func CreateArgs(cfg *config.Config, image, policyFile, secret string, port int) (args, env []string, err error) {
	sc, err := SandboxConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	return []string{
			"sandbox", "create", "--name", SandboxName(cfg.Name), "--from", image,
			"--policy", policyFile, "--provider", ProviderName(cfg.Name),
			"--env-from", SecretEnv,
			"--env-from", ConfigEnv,
			"--env", "STATE_DIRECTORY=" + StateDir,
			"--detach", "--",
			Launcher, "serve", "--config-env", ConfigEnv, "--listen-tcp", fmt.Sprintf("127.0.0.1:%d", port), "--idle-exit", "0",
		}, []string{
			SecretEnv + "=" + secret,
			ConfigEnv + "=" + base64.StdEncoding.EncodeToString(sc),
		}, nil
}

// hostURL replaces a loopback host by the sandbox's name for the host.
func hostURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	h := u.Hostname()
	if h == "localhost" || (net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback()) {
		if p := u.Port(); p != "" {
			u.Host = net.JoinHostPort(HostAlias, p)
		} else {
			u.Host = HostAlias
		}
	}
	return u.String()
}

// allMCP lets every known MCP method through OpenShell's MCP inspection
// (OpenShell requires rules or this for protocol mcp); the tools are
// mcp-gateway's to decide. The protocol revisions are the agent's (MCP
// go-sdk v1.4 negotiates 2025-06-18): OpenShell allows only 2025-11-25
// unless they are listed, and answers a request with another
// Mcp-Protocol-Version 403 (notifications/initialized, the first).
var allMCP = map[string]any{"allow_all_known_mcp_methods": true, "versions": MCPVersions}

// MCPVersions are the MCP protocol revisions the agent may speak through
// OpenShell.
var MCPVersions = []string{"2025-06-18", "2025-11-25"}

type hp struct {
	host, path string
	port       int
}

// hostPort reads a URL as OpenShell endpoint fields; a loopback host is
// the sandbox host's (host.openshell.internal).
func hostPort(raw string) (hp, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return hp{}, fmt.Errorf("%q is not a URL", raw)
	}
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			return hp{}, err
		}
	}
	h := u.Hostname()
	if h == "localhost" || (net.ParseIP(h) != nil && net.ParseIP(h).IsLoopback()) {
		h = HostAlias
	}
	path := u.EscapedPath()
	if path == "/" {
		path = ""
	}
	return hp{host: h, port: port, path: path}, nil
}
