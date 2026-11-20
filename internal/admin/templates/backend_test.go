package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/host"
)

const gatewayHTTP = `http:
  issuer: https://idp.example.com/realms/mcp
  audience: https://gw.example.com:8443/mcp
  scopes: [mcp-agents]
`

func withGatewayHTTP(t *testing.T, a *admin.Admin, conf string) {
	t.Helper()
	os.MkdirAll(a.Path("/etc/mcp-gateway"), 0o755)
	if err := host.WriteFile(a.Path("/etc/mcp-gateway/gateway.yaml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
}

// systemdOnly makes the test's copy of the shipped disk-watch a template
// for systemd only (no backends), as a template of a site may be: every
// shipped one allows OpenShell (#241).
func systemdOnly(t *testing.T, a *admin.Admin) {
	t.Helper()
	p := a.Path(filepath.Join(TemplatesVendor, "disk-watch", "template.yaml"))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	keep := regexp.MustCompile(`(?ms)^backends:.*\z`).ReplaceAll(b, nil)
	if err := host.WriteFile(p, keep, 0o644); err != nil {
		t.Fatal(err)
	}
}

func parseDraft(t *testing.T, b []byte, err error) *config.Config {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Parse(b)
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return c
}

// A template that allows OpenShell gives a draft for it: the gateway's
// audience as gateway.url, the agent's OAuth client at the gateway's
// (Keycloak) issuer, the sandbox with OpenShell's gateway and driver; the
// template's comments stay. For systemd, the draft is the template's.
func TestDraftOnOpenShell(t *testing.T) {
	a, _ := withTemplates(t)
	withGatewayHTTP(t, a, gatewayHTTP)
	b, err := DraftOn(a, "pp", "patch-pilot", Backend{Kind: "openshell", Driver: "systemd"})
	c := parseDraft(t, b, err)
	if c.Gateway.URL != "https://gw.example.com:8443/mcp" || c.Gateway.Socket != "" {
		t.Errorf("gateway %+v", c.Gateway)
	}
	if o := c.Gateway.OAuth; o == nil || o.TokenURL != "https://idp.example.com/realms/mcp/protocol/openid-connect/token" || o.ClientID != "agent-pp" || strings.Join(o.Scopes, ",") != "mcp-agents" {
		t.Errorf("oauth %+v", c.Gateway.OAuth)
	}
	if c.Sandbox.Kind != "openshell" || c.Sandbox.OpenShellDriver != "systemd" || c.Sandbox.OpenShellGateway != "openshell" {
		t.Errorf("sandbox %+v", c.Sandbox)
	}
	if !strings.HasPrefix(string(b), "# Agent template patch-pilot") || len(c.Gateway.Servers) == 0 {
		t.Errorf("the template's configuration is not kept:\n%s", b)
	}
	// Defaults: the container driver; a static token, no OAuth client.
	b, err = DraftOn(a, "pp", "patch-pilot", Backend{Kind: "openshell", StaticToken: true})
	c = parseDraft(t, b, err)
	if c.Gateway.OAuth != nil || c.Sandbox.OpenShellDriver != "container" {
		t.Errorf("static token: %+v %+v", c.Gateway.OAuth, c.Sandbox)
	}
	// systemd (or nothing said): the template's, as Draft gives it.
	plain, _ := Draft(a, "pp", "patch-pilot")
	sd, err := DraftOn(a, "pp", "patch-pilot", Backend{Kind: "systemd"})
	if err != nil || string(sd) != string(plain) {
		t.Errorf("systemd draft differs: %v", err)
	}
}

// What a draft for OpenShell refuses.
func TestDraftOnRefusals(t *testing.T) {
	a, _ := withTemplates(t)
	systemdOnly(t, a)
	withGatewayHTTP(t, a, gatewayHTTP)
	for _, c := range []struct {
		tmpl string
		b    Backend
		want string
	}{
		{"disk-watch", Backend{Kind: "openshell"}, "template disk-watch is for systemd, not openshell"},
		{"patch-pilot", Backend{Kind: "kubernetes"}, `--sandbox "kubernetes"`},
		{"patch-pilot", Backend{Driver: "systemd"}, "are for --sandbox openshell"},
		{"patch-pilot", Backend{Kind: "openshell", Driver: "docker"}, `--openshell-driver "docker"`},
		{"patch-pilot", Backend{Kind: "openshell", StaticToken: true, TokenURL: "https://x"}, "not with --static-token"},
	} {
		if _, err := DraftOn(a, "pp", c.tmpl, c.b); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %+v: %v", c.tmpl, c.b, err)
		}
	}
	// An issuer that is not Keycloak's: the token URL must be given.
	withGatewayHTTP(t, a, strings.Replace(gatewayHTTP, "https://idp.example.com/realms/mcp", "https://login.example.com", 1))
	if _, err := DraftOn(a, "pp", "patch-pilot", Backend{Kind: "openshell"}); err == nil || !strings.Contains(err.Error(), "--token-url") {
		t.Errorf("no token URL: %v", err)
	}
	b, err := DraftOn(a, "pp", "patch-pilot", Backend{Kind: "openshell", TokenURL: "https://login.example.com/oauth2/token"})
	if c := parseDraft(t, b, err); c.Gateway.OAuth.RefreshBefore != admin.DraftRefreshBefore {
		t.Errorf("the draft's renewal window: %+v", c.Gateway.OAuth)
	}
	if c := parseDraft(t, b, err); c.Gateway.OAuth.TokenURL != "https://login.example.com/oauth2/token" {
		t.Errorf("token URL %+v", c.Gateway.OAuth)
	}
	// Without the gateway's HTTPS listener there is nothing to reach.
	os.Remove(a.Path("/etc/mcp-gateway/gateway.yaml"))
	if _, err := DraftOn(a, "pp", "patch-pilot", Backend{Kind: "openshell"}); err == nil {
		t.Error("no gateway configuration")
	}
}

// create --template --sandbox openshell: the agent runs in OpenShell, is
// the same as its template to compare, and agent-admin says what is
// next; with --from, the file says where it runs, and the template must
// allow that.
func TestCreateOnOpenShell(t *testing.T) {
	a, _ := withTemplates(t)
	systemdOnly(t, a)
	withGatewayHTTP(t, a, gatewayHTTP)
	var out strings.Builder
	a.Out = &out
	if err := CreateOn(a, "pp", "patch-pilot", "", Backend{Kind: "openshell"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(a.Path(config.ConfigRoot + "/pp/agent.yaml"))
	if c := parseDraft(t, b, err); c.Sandbox.Kind != "openshell" || c.Gateway.URL == "" {
		t.Errorf("created %+v", c.Sandbox)
	}
	if !strings.Contains(out.String(), "next: agent-admin idp-client pp") {
		t.Errorf("output:\n%s", out.String())
	}
	if cmp := compareOne(t, a, "pp", ""); cmp.Configuration == nil || !cmp.Configuration.Same {
		t.Errorf("compare: %+v", cmp.Configuration)
	}

	// --from with an OpenShell configuration, for a template that is not.
	f := t.TempDir() + "/dw.yaml"
	dw, _ := DraftOn(a, "dw", "patch-pilot", Backend{Kind: "openshell"})
	os.WriteFile(f, dw, 0o644)
	if err := CreateOn(a, "dw", "disk-watch", f, Backend{}); err == nil || !strings.Contains(err.Error(), "is for systemd, not openshell") {
		t.Errorf("--from for a systemd template: %v", err)
	}
	if err := CreateOn(a, "dw", "patch-pilot", f, Backend{Kind: "openshell"}); err == nil || !strings.Contains(err.Error(), "not with --from") {
		t.Errorf("--sandbox with --from: %v", err)
	}
}

// templates lists where each may run, and what OpenShell needs.
func TestTemplatesListBackends(t *testing.T) {
	a, _ := withTemplates(t)
	systemdOnly(t, a)
	l, err := List(a)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(l.Templates["patch-pilot"].Backends, ","); got != "systemd,openshell" || len(l.Templates["patch-pilot"].NeedsOpenShell) == 0 {
		t.Errorf("patch-pilot: %s %v", got, l.Templates["patch-pilot"].NeedsOpenShell)
	}
	if got := strings.Join(l.Templates["disk-watch"].Backends, ","); got != "systemd" {
		t.Errorf("disk-watch: %s", got)
	}
}

// A value from the command line stays a value in the draft's YAML
// (security review 0.8, S8). Scopes are quoted as well; config.Parse
// already refuses one that is not a scope name.
func TestDraftOnQuotesValues(t *testing.T) {
	a, _ := withTemplates(t)
	withGatewayHTTP(t, a, gatewayHTTP)
	b, err := DraftOn(a, "pp", "patch-pilot", Backend{Kind: "openshell", Driver: "systemd", Gateway: "gw, openshell_driver: container"})
	c := parseDraft(t, b, err)
	if c.Sandbox.OpenShellGateway != "gw, openshell_driver: container" || c.Sandbox.OpenShellDriver != "systemd" {
		t.Errorf("sandbox %+v", c.Sandbox)
	}
}

// Every shipped template allows OpenShell (#241), and gives a valid
// configuration for it, with either driver, for an agent name OpenShell
// takes.
func TestShippedTemplatesOnOpenShell(t *testing.T) {
	a, _ := withTemplates(t)
	withGatewayHTTP(t, a, gatewayHTTP)
	l, err := List(a)
	if err != nil {
		t.Fatal(err)
	}
	for name, tm := range l.Templates {
		if !strings.Contains(strings.Join(tm.Backends, ","), "openshell") || len(tm.NeedsOpenShell) == 0 {
			t.Errorf("%s: backends %v, needs_openshell %v", name, tm.Backends, tm.NeedsOpenShell)
		}
		for _, driver := range []string{"container", "systemd"} {
			b, err := DraftOn(a, "osh-agent", name, Backend{Kind: "openshell", Driver: driver})
			if c := parseDraft(t, b, err); c.Sandbox.Kind != "openshell" || c.Sandbox.OpenShellDriver != driver || c.Gateway.URL == "" {
				t.Errorf("%s, %s: %+v %+v", name, driver, c.Sandbox, c.Gateway)
			}
		}
	}
}
