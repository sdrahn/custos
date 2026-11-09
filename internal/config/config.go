// Package config reads and checks an agent's agent.yaml (CONTRACT.md §4).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/playbook"
)

// ContractVersion is the contract version this code implements.
const ContractVersion = 1

// Default paths (CONTRACT.md §3, §6).
const (
	ConfigRoot    = "/etc/custos/agents"
	GatewaySocket = "/run/mcp-gateway/mcp.sock"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

var envRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func isLoopback(h string) bool {
	// host.openshell.internal is what the host's loopback becomes in an
	// OpenShell sandbox (the configuration the sandbox gets).
	if h == "localhost" || h == "host.openshell.internal" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var credRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidCredential reports whether s is a valid credential name.
func ValidCredential(s string) bool { return credRE.MatchString(s) }

// secretPrefixes start the API keys of common model services and
// proxies (OpenAI and LiteLLM sk-, Hugging Face, Groq, xAI, Perplexity,
// NVIDIA, Replicate, GitHub, GitLab, Slack), in the lower case a
// credential name allows.
var secretPrefixes = []string{"sk-", "sk_", "pk-", "rk-", "hf_", "gsk_", "xai-", "pplx-", "nvapi-", "r8_",
	"ghp_", "gho_", "github_pat_", "glpat-", "xoxb-", "xoxp-"}

// LooksLikeSecret reports whether a credential name looks like the
// secret itself: a key's prefix, or 32 characters or more with at least
// eight digits. model.credential takes the name the secret is stored
// under (agent-admin set-credential), never the secret.
func LooksLikeSecret(s string) bool {
	for _, p := range secretPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	if len(s) < 32 {
		return false
	}
	digits := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	return digits >= 8
}

// SecretAsName says that a credential name looks like the secret, without
// repeating it (it may be one: it must not reach a log).
const SecretAsName = "model.credential looks like a key, not a name: it takes the name the key is stored under " +
	"with agent-admin set-credential, such as model-key"

// ValidName reports whether s is a valid agent name.
func ValidName(s string) bool { return nameRE.MatchString(s) }

type Config struct {
	Version         int         `yaml:"version"`
	Name            string      `yaml:"name"`
	Description     string      `yaml:"description"`
	Model           Model       `yaml:"model"`
	Instruction     string      `yaml:"instruction"`
	InstructionFile string      `yaml:"instruction_file"`
	Gateway         Gateway     `yaml:"gateway"`
	Limits          Limits      `yaml:"limits"`
	Interaction     Interaction `yaml:"interaction"`
	Sessions        Sessions    `yaml:"sessions"`
	Schedules       []Schedule  `yaml:"schedules"`
	Events          []Event     `yaml:"events"`
	Sandbox         Sandbox     `yaml:"sandbox"`

	// Dir is the directory the file was read from (not in the file).
	Dir string `yaml:"-"`
}

type Model struct {
	Provider    string   `yaml:"provider"`
	Name        string   `yaml:"name"`
	Endpoint    string   `yaml:"endpoint"`
	Credential  string   `yaml:"credential"`
	Temperature *float32 `yaml:"temperature"`
	// TimeoutSeconds bounds one request to the model (0: no bound of its
	// own; the activity's max_invocation_seconds bounds it). A model
	// server on slow hardware may need minutes to read a long prompt.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// MaxOutputTokens bounds what the model writes in one answer (0: no
	// bound of its own; the model server's). A small model can write
	// without end; with a bound its answer is cut off and the activity
	// goes on (or ends) in time.
	MaxOutputTokens int `yaml:"max_output_tokens"`
}

// Timeout is TimeoutSeconds as a duration (0: none).
func (m Model) Timeout() time.Duration { return time.Duration(m.TimeoutSeconds) * time.Second }

type Gateway struct {
	Socket string `yaml:"socket"`
	// URL is mcp-gateway's Streamable HTTP endpoint (aggregated), for
	// sandboxed agents that cannot reach the socket (CONTRACT.md §3a).
	URL string `yaml:"url"`
	// TokenEnv names the environment variable holding the bearer token
	// for URL. Under OpenShell it holds a placeholder that the supervisor
	// replaces with the real token (the agent never sees it).
	TokenEnv string `yaml:"token_env"`
	// OAuth: the token for URL is an OAuth access token that OpenShell's
	// gateway mints and renews with the client credentials grant
	// (CONTRACT.md §16a); the client secret is set with agent-admin
	// set-credential NAME gateway-client-secret. Without it, the token is
	// a static one (set-credential NAME gateway-token).
	OAuth *OAuth `yaml:"oauth,omitempty"`

	Servers []string `yaml:"servers"`
	Tools   []string `yaml:"tools"`
}

// OAuth is an agent's OAuth client at the token issuer the gateway
// trusts for agents (its access tokens have the subject agent-<name> and
// the gateway's audience: the issuer's client configuration).
type OAuth struct {
	TokenURL string   `yaml:"token_url"`
	ClientID string   `yaml:"client_id"`
	Scopes   []string `yaml:"scopes"`
	// RefreshBefore: how long before a token's expiry a new one is minted
	// (seconds; default OpenShell's).
	RefreshBefore int `yaml:"refresh_before_seconds"`
}

type Limits struct {
	MaxLLMCalls          int `yaml:"max_llm_calls" json:"max_llm_calls"`
	MaxToolCalls         int `yaml:"max_tool_calls" json:"max_tool_calls"`
	MaxInvocationSeconds int `yaml:"max_invocation_seconds" json:"max_invocation_seconds"`
}

// MaxInvocation is the invocation deadline.
func (l Limits) MaxInvocation() time.Duration {
	return time.Duration(l.MaxInvocationSeconds) * time.Second
}

type Interaction struct {
	AGUI        bool        `yaml:"agui"`
	ClientTools ClientTools `yaml:"client_tools"`
}

// ClientTools is "deny" or {allow: [...]}.
type ClientTools struct {
	Allow []string
}

func (c *ClientTools) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value != "deny" {
			return fmt.Errorf("client_tools: %q: want deny or {allow: [...]}", n.Value)
		}
		c.Allow = nil
		return nil
	}
	var v struct {
		Allow []string `yaml:"allow"`
	}
	if err := n.Decode(&v); err != nil {
		return fmt.Errorf("client_tools: %w", err)
	}
	c.Allow = v.Allow
	return nil
}

// Allowed reports whether the frontend tool name may be offered.
func (c ClientTools) Allowed(name string) bool {
	for _, a := range c.Allow {
		if a == name {
			return true
		}
	}
	return false
}

// Sandbox is the agent's sandbox backend (CONTRACT.md §3a).
type Sandbox struct {
	Kind string `yaml:"kind"` // systemd (default) | openshell
	// OpenShellGateway names the OpenShell gateway (openshell only).
	OpenShellGateway string `yaml:"openshell_gateway"`
}

// Lifecycle is /v1/status's lifecycle (CONTRACT.md §3a).
func (c *Config) Lifecycle() map[string]string {
	if c.Sandbox.Kind == "openshell" {
		return map[string]string{"kind": "openshell", "sandbox": "agent-" + c.Name, "gateway": c.Sandbox.OpenShellGateway,
			"service": "agent-openshell@" + c.Name + ".service"}
	}
	return map[string]string{"kind": "systemd", "service": "agent@" + c.Name + ".service", "socket": "agent@" + c.Name + ".socket"}
}

// Schedule is a recurring activity, run by a systemd timer
// (agent-schedule@<name>_<schedule>.timer, CONTRACT.md §11).
type Schedule struct {
	Name       string `yaml:"name" json:"name"`
	OnCalendar string `yaml:"on_calendar" json:"on_calendar"`
	Prompt     string `yaml:"prompt" json:"prompt"`
	// Playbook names a diagnostic playbook to run (CONTRACT.md §14c); the
	// prompt is then optional and added to it.
	Playbook string `yaml:"playbook,omitempty" json:"playbook,omitempty"`
}

var scheduleRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// Event is an activity started when something happens on the host
// (CONTRACT.md §14, §15): for now, when one of the named systemd units
// fails. agent-admin puts an OnFailure= drop-in on each unit, which
// starts agent-event@<name>_<event>_<unit>.service.
type Event struct {
	Name string `yaml:"name" json:"name"`
	// OnUnitFailure are the units whose failure starts it.
	OnUnitFailure []string `yaml:"on_unit_failure" json:"on_unit_failure"`
	// Prompt is the activity's prompt; {unit} is the unit that failed.
	Prompt string `yaml:"prompt" json:"prompt"`
	// Playbook names a diagnostic playbook to run (CONTRACT.md §14c), as
	// for a schedule.
	Playbook string `yaml:"playbook,omitempty" json:"playbook,omitempty"`
	// MinIntervalSeconds is how long after it started an activity for a
	// unit the event starts none for the same unit (0: DefaultEventInterval).
	MinIntervalSeconds int `yaml:"min_interval_seconds,omitempty" json:"min_interval_seconds,omitempty"`
}

// DefaultEventInterval is an event's minimum interval per unit.
const DefaultEventInterval = 600

// MinInterval is the event's minimum interval per unit.
func (e Event) MinInterval() time.Duration {
	if e.MinIntervalSeconds == 0 {
		return DefaultEventInterval * time.Second
	}
	return time.Duration(e.MinIntervalSeconds) * time.Second
}

// eventUnitRE is a unit an event may watch: a plain unit name, no
// template instance (no "@"), of a kind that can fail.
var eventUnitRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,190}\.(service|socket|mount|swap|timer|path)$`)

// ownUnit reports whether a unit is Custos's own: an event on it could
// start itself.
func ownUnit(u string) bool {
	return strings.HasPrefix(u, "agent@") || strings.HasPrefix(u, "agent-") || strings.HasPrefix(u, "custos-")
}

type Sessions struct {
	RetentionDays int `yaml:"retention_days"`
}

// Load reads <dir>/agent.yaml and checks it.
func Load(dir string) (*Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		return nil, err
	}
	c, err := Parse(b)
	if err != nil {
		return nil, err
	}
	c.Dir = dir
	if base := filepath.Base(dir); base != c.Name {
		return nil, fmt.Errorf("name %q does not match the directory %q", c.Name, base)
	}
	if c.InstructionFile != "" {
		p := c.InstructionFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		ib, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("instruction_file: %w", err)
		}
		c.Instruction = string(ib)
	}
	return c, nil
}

// Parse decodes and checks a configuration, filling in defaults.
// Unknown keys are errors, so a typo does not silently drop a limit.
func Parse(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("agent.yaml: %w", err)
	}
	c.defaults()
	if err := c.Check(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) defaults() {
	if c.Gateway.Socket == "" && c.Gateway.URL == "" {
		c.Gateway.Socket = GatewaySocket
	}
	if c.Limits.MaxLLMCalls == 0 {
		c.Limits.MaxLLMCalls = 25
	}
	if c.Limits.MaxToolCalls == 0 {
		c.Limits.MaxToolCalls = 40
	}
	if c.Limits.MaxInvocationSeconds == 0 {
		c.Limits.MaxInvocationSeconds = 900
	}
	if c.Sessions.RetentionDays == 0 {
		c.Sessions.RetentionDays = 30
	}
	if c.Sandbox.Kind == "" {
		c.Sandbox.Kind = "systemd"
	}
	if c.Sandbox.Kind == "openshell" && c.Sandbox.OpenShellGateway == "" {
		c.Sandbox.OpenShellGateway = "openshell" // the name OpenShell's installer registers
	}
}

// Check checks a model block (model: in agent.yaml, or the host's
// defaults for new agents), returning all problems at once.
func (m Model) Check() error {
	var errs []error
	bad := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	switch m.Provider {
	case "gemini", "vertex":
		if m.Endpoint != "" {
			bad("model.endpoint: only for provider openai")
		}
	case "openai":
		if !strings.HasPrefix(m.Endpoint, "https://") && !strings.HasPrefix(m.Endpoint, "http://") {
			bad("model.endpoint: %q: want an http(s) URL", m.Endpoint)
		}
	case "":
		bad("model.provider: missing")
	default:
		bad("model.provider: %q: want gemini, vertex or openai", m.Provider)
	}
	if m.Name == "" {
		bad("model.name: missing")
	}
	if m.TimeoutSeconds < 0 {
		bad("model.timeout_seconds: %d: want 0 (none: max_invocation_seconds bounds a call) or more", m.TimeoutSeconds)
	}
	if m.MaxOutputTokens < 0 || m.MaxOutputTokens > math.MaxInt32 {
		bad("model.max_output_tokens: %d: want 0 (none: the model server's) or more", m.MaxOutputTokens)
	}
	if m.Provider == "gemini" && m.Credential == "" {
		bad("model.credential: provider gemini needs an API key credential")
	}
	if m.Credential != "" && !ValidCredential(m.Credential) {
		if LooksLikeSecret(strings.ToLower(m.Credential)) {
			bad("%s", SecretAsName)
		} else {
			bad("model.credential: %q: want a credential name such as model-key", m.Credential)
		}
	}
	return errors.Join(errs...)
}

// Check returns all problems at once, so an editor can show them together.
func (c *Config) Check() error {
	var errs []error
	bad := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	if c.Version != ContractVersion {
		bad("version: %d: this launcher implements version %d", c.Version, ContractVersion)
	}
	if !ValidName(c.Name) {
		bad("name: %q: want lower-case letters, digits and '-', 2-32 characters, starting with a letter", c.Name)
	}
	if err := c.Model.Check(); err != nil {
		errs = append(errs, err)
	}
	if c.Instruction != "" && c.InstructionFile != "" {
		bad("instruction and instruction_file: set one of them")
	}
	switch {
	case c.Gateway.URL != "" && c.Gateway.Socket != "":
		bad("gateway: set socket or url, not both")
	case c.Gateway.URL != "":
		u, err := url.Parse(c.Gateway.URL)
		if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) || u.Host == "" {
			bad("gateway.url: %q: want an https URL (http only on the host's loopback, for tests)", c.Gateway.URL)
		}
		if c.Gateway.TokenEnv != "" && !envRE.MatchString(c.Gateway.TokenEnv) {
			bad("gateway.token_env: %q: want an environment variable name", c.Gateway.TokenEnv)
		}
	case !filepath.IsAbs(c.Gateway.Socket):
		bad("gateway.socket: %q: want an absolute path", c.Gateway.Socket)
	}
	if c.Gateway.TokenEnv != "" && c.Gateway.URL == "" {
		bad("gateway.token_env: only with gateway.url")
	}
	if o := c.Gateway.OAuth; o != nil {
		if c.Sandbox.Kind != "openshell" {
			bad("gateway.oauth: only with sandbox kind openshell (OpenShell's gateway mints the tokens)")
		}
		u, err := url.Parse(o.TokenURL)
		if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) || u.Host == "" {
			bad("gateway.oauth.token_url: %q: want an https URL (http only on the host's loopback, for tests)", o.TokenURL)
		}
		if o.ClientID == "" || strings.ContainsAny(o.ClientID, " \t\n=") {
			bad("gateway.oauth.client_id: %q: want the agent's OAuth client id", o.ClientID)
		}
		for _, s := range o.Scopes {
			if s == "" || strings.ContainsAny(s, " \t\n") {
				bad("gateway.oauth.scopes: %q: want scope names", s)
			}
		}
		if o.RefreshBefore < 0 {
			bad("gateway.oauth.refresh_before_seconds: want 0 or more")
		}
	}
	if c.Sandbox.Kind == "openshell" && c.Gateway.URL == "" {
		bad("gateway.url: an openshell agent reaches the gateway over HTTPS (CONTRACT.md §3a)")
	}
	if len(c.Gateway.Servers) == 0 {
		bad("gateway.servers: name at least one gateway server")
	}
	for _, t := range c.Gateway.Tools {
		srv, _, ok := strings.Cut(t, "__")
		if !ok {
			bad("gateway.tools: %q: want <server>__<tool>", t)
		} else if !contains(c.Gateway.Servers, srv) {
			// The fix in the message (and for Cockpit to offer): a tool
			// is shown only from a server the agent names.
			bad("gateway.tools: %q: its server %q is not in gateway.servers: add %s to gateway.servers", t, srv, srv)
		}
	}
	for k, v := range map[string]int{"max_llm_calls": c.Limits.MaxLLMCalls, "max_tool_calls": c.Limits.MaxToolCalls, "max_invocation_seconds": c.Limits.MaxInvocationSeconds} {
		if v < 0 {
			bad("limits.%s: %d: must be positive", k, v)
		}
	}
	switch c.Sandbox.Kind {
	case "systemd":
		if c.Sandbox.OpenShellGateway != "" {
			bad("sandbox.openshell_gateway: only for kind openshell")
		}
	case "openshell":
	default:
		bad("sandbox.kind: %q: want systemd or openshell", c.Sandbox.Kind)
	}
	seen := map[string]bool{}
	for i, s := range c.Schedules {
		if !scheduleRE.MatchString(s.Name) || seen[s.Name] {
			bad("schedules[%d].name: %q: want a unique lower-case name", i, s.Name)
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.OnCalendar) == "" || strings.ContainsAny(s.OnCalendar, "\n\r") {
			bad("schedules[%d].on_calendar: want a systemd calendar expression (systemd.time(7))", i)
		}
		if strings.TrimSpace(s.Prompt) == "" && s.Playbook == "" {
			bad("schedules[%d].prompt: missing (or name a playbook)", i)
		}
		if s.Playbook != "" && !playbook.ValidName(s.Playbook) {
			bad("schedules[%d].playbook: %q is not a playbook name", i, s.Playbook)
		}
	}
	seenEvent := map[string]bool{}
	for i, e := range c.Events {
		if !scheduleRE.MatchString(e.Name) || seenEvent[e.Name] {
			bad("events[%d].name: %q: want a unique lower-case name", i, e.Name)
		}
		seenEvent[e.Name] = true
		if len(e.OnUnitFailure) == 0 || len(e.OnUnitFailure) > 32 {
			bad("events[%d].on_unit_failure: want 1 to 32 systemd units", i)
		}
		seenUnit := map[string]bool{}
		for _, u := range e.OnUnitFailure {
			switch {
			case seenUnit[u]:
				bad("events[%d].on_unit_failure: %q twice", i, u)
			case ownUnit(u):
				bad("events[%d].on_unit_failure: %q is one of Custos's own units", i, u)
			case !eventUnitRE.MatchString(u):
				bad("events[%d].on_unit_failure: %q: want a unit name such as nginx.service (no template instance with @)", i, u)
			}
			seenUnit[u] = true
		}
		if strings.TrimSpace(e.Prompt) == "" && e.Playbook == "" {
			bad("events[%d].prompt: missing (or name a playbook)", i)
		}
		if e.Playbook != "" && !playbook.ValidName(e.Playbook) {
			bad("events[%d].playbook: %q is not a playbook name", i, e.Playbook)
		}
		if e.MinIntervalSeconds < 0 || e.MinIntervalSeconds > 7*24*3600 {
			bad("events[%d].min_interval_seconds: want 0 (the default, %d) to %d", i, DefaultEventInterval, 7*24*3600)
		}
	}
	if c.Sessions.RetentionDays < 0 {
		bad("sessions.retention_days: must be positive")
	}
	return errors.Join(errs...)
}

// ShowsTool reports whether a gateway tool (<server>__<tool>) is offered
// to the model. This is a convenience, not a security boundary (§4).
func (c *Config) ShowsTool(name string) bool {
	srv, _, ok := strings.Cut(name, "__")
	if !ok || !contains(c.Gateway.Servers, srv) {
		return false
	}
	return len(c.Gateway.Tools) == 0 || contains(c.Gateway.Tools, name)
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
