package admin

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// GatewayHTTPIssuers lists the issuers the gateway accepts agent-*
// subjects from (§16a).
const GatewayHTTPIssuers = "/etc/mcp-gateway/policy/agents/_http.yaml"

// AgentIssuers are the issuers listed in _http.yaml, as written.
func (a *Admin) AgentIssuers() ([]string, error) {
	b, err := os.ReadFile(a.Path(GatewayHTTPIssuers))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		HTTP struct {
			Issuers []string `yaml:"issuers"`
		} `yaml:"_http"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", GatewayHTTPIssuers, err)
	}
	return doc.HTTP.Issuers, nil
}

// TrustedIssuers are the issuers the gateway trusts for agents: _http.yaml's,
// unless the seal covers it and it does not match (then none, and why;
// security review 0.6, N6).
func (a *Admin) TrustedIssuers() ([]string, string) {
	issuers, err := a.AgentIssuers()
	if err != nil {
		return nil, err.Error()
	}
	if pub, agents, err := a.sealInfo(); err == nil && agents && !a.entryVerified(pub, HTTPIssuersName) {
		return nil, GatewayHTTPIssuers + " does not match its seal, so the gateway trusts no issuer: check it, then accept it " +
			"(agent-admin seal --agents --accept _http)"
	}
	return issuers, ""
}
