package templates

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gatewayconf"
)

// Backends a template may allow (template.yaml's backends, CONTRACT.md
// §14a). A template that names none is for systemd only.
var Backends = []string{"systemd", "openshell"}

// Backend is where an agent made from a template runs, chosen when it is
// created (create and draft --sandbox): the template is one per use
// case, not one per backend.
type Backend struct {
	Kind    string // systemd (the default) or openshell
	Driver  string // OpenShell's compute driver: container (the default) or systemd
	Gateway string // OpenShell's gateway, as registered for root (openshell)
	// TokenURL is the issuer's token endpoint for the agent's OAuth client;
	// "": derived from a Keycloak issuer.
	TokenURL string
	// StaticToken: no OAuth client; set-credential … gateway-token.
	StaticToken bool
}

// IsOpenShell says whether the agent runs in an OpenShell sandbox.
func (b Backend) IsOpenShell() bool { return b.Kind == "openshell" }

func (b Backend) check() error {
	switch b.Kind {
	case "", "systemd":
		if b.Driver != "" || b.Gateway != "" || b.TokenURL != "" || b.StaticToken {
			return fmt.Errorf("--openshell-driver, --openshell-gateway, --token-url and --static-token are for --sandbox openshell")
		}
	case "openshell":
		if b.Driver != "" && b.Driver != "container" && b.Driver != "systemd" {
			return fmt.Errorf("--openshell-driver %q: want container or systemd", b.Driver)
		}
		if b.StaticToken && b.TokenURL != "" {
			return fmt.Errorf("--token-url is for an OAuth client, not with --static-token")
		}
	default:
		return fmt.Errorf("--sandbox %q: want systemd or openshell", b.Kind)
	}
	return nil
}

func (b Backend) kind() string {
	if b.Kind == "" {
		return "systemd"
	}
	return b.Kind
}

// allows says whether a template may run on a backend kind.
func (t *Template) allows(kind string) error {
	if slices.Contains(t.Backends, kind) {
		return nil
	}
	return fmt.Errorf("template %s is for %s, not %s (its template.yaml's backends)", t.name, strings.Join(t.Backends, ", "), kind)
}

var (
	gatewayLine  = regexp.MustCompile(`(?m)^gateway:[ \t]*(#.*)?\n`)
	sandboxBlock = regexp.MustCompile(`(?m)^sandbox:.*\n(?:[ \t]+.*\n)*`)
	socketLine   = regexp.MustCompile(`(?m)^  socket:.*\n`)
)

// WithBackend is a configuration for an agent NAME on backend b: for
// OpenShell, gateway.url is mcp-gateway's audience (the name agents reach
// it by), gateway.oauth the agent's OAuth client at the gateway's issuer
// (client agent-NAME, the gateway's scopes) unless b.StaticToken, and
// sandbox names OpenShell's gateway and driver. The model endpoint stays:
// a loopback one becomes the sandbox's name for the host when the sandbox
// is made (§16a). For systemd it is cfg unchanged.
func WithBackend(a *admin.Admin, cfg []byte, name string, b Backend) ([]byte, error) {
	if !b.IsOpenShell() {
		return cfg, nil
	}
	h, err := gatewayconf.ReadHTTP(a.Root, a.GatewayConfig)
	if err != nil {
		return nil, err
	}
	lines := []string{"  url: " + strconv.Quote(h.Audience) + "   # mcp-gateway's http.audience: the host's name for the gateway"}
	if !b.StaticToken {
		tokenURL := b.TokenURL
		if tokenURL == "" {
			if i := strings.Index(h.Issuer, "/realms/"); i >= 0 {
				tokenURL = strings.TrimRight(h.Issuer, "/") + "/protocol/openid-connect/token"
			} else {
				return nil, fmt.Errorf("mcp-gateway's issuer %s is not a Keycloak realm: give the issuer's token endpoint with --token-url, or --static-token", h.Issuer)
			}
		}
		o := []string{"token_url: " + strconv.Quote(tokenURL), "client_id: agent-" + name,
			"refresh_before_seconds: " + strconv.Itoa(admin.DraftRefreshBefore)}
		if len(h.Scopes) > 0 {
			q := make([]string, len(h.Scopes))
			for i, sc := range h.Scopes {
				q[i] = strconv.Quote(sc)
			}
			o = append(o, "scopes: ["+strings.Join(q, ", ")+"]")
		}
		lines = append(lines, "  oauth: {"+strings.Join(o, ", ")+"}   # agent-admin idp-client "+name+" says what the client must be")
	}
	loc := gatewayLine.FindIndex(cfg)
	if loc == nil {
		return nil, fmt.Errorf("the configuration has no gateway block (a line \"gateway:\") to add the OpenShell connection to")
	}
	out := append([]byte{}, cfg[:loc[1]]...)
	out = append(out, []byte(strings.Join(lines, "\n")+"\n")...)
	out = append(out, socketLine.ReplaceAll(cfg[loc[1]:], nil)...)
	gw := b.Gateway
	if gw == "" {
		gw = "openshell"
	}
	driver := b.Driver
	if driver == "" {
		driver = "container"
	}
	// Quoted: the values are mcp-gateway's and the command line's, and
	// must stay values in the YAML (security review 0.8, S8).
	sb := fmt.Sprintf("sandbox: {kind: openshell, openshell_gateway: %s, openshell_driver: %s}\n", strconv.Quote(gw), strconv.Quote(driver))
	if loc := sandboxBlock.FindIndex(out); loc != nil {
		out = append(append(append([]byte{}, out[:loc[0]]...), sb...), out[loc[1]:]...)
	} else {
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, sb...)
	}
	if _, err := config.Parse(out); err != nil {
		return nil, fmt.Errorf("the configuration for OpenShell is not valid: %w", err)
	}
	return out, nil
}

// openShellNext says what an OpenShell agent needs after create.
func openShellNext(name string, b Backend) string {
	if b.StaticToken {
		return fmt.Sprintf("next: agent-admin set-credential %s gateway-token < token (creates the sandbox)", name)
	}
	return fmt.Sprintf("next: agent-admin idp-client %s (the OAuth client it needs), check-token %s < secret, then set-credential %s gateway-client-secret < secret (creates the sandbox)", name, name, name)
}
