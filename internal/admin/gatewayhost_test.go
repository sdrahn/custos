package admin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gatewayconf"
)

// The name an OpenShell agent reaches the gateway by (#131): the host's
// own, resolving to a network address and the gateway's audience, needs no
// note; the alias, a loopback answer, a name that does not resolve, or a
// URL that is not the audience do.
func TestGatewayHostNotes(t *testing.T) {
	a, _ := newAdmin(t)
	os.MkdirAll(a.Path(filepath.Dir(gatewayconf.Config)), 0o755)
	os.WriteFile(a.Path(gatewayconf.Config), []byte("http:\n  issuer: https://idp.example.com/realms/mcp\n  audience: https://gw.example.com:8443/mcp\n"), 0o644)
	dns := map[string][]string{"gw.example.com": {"10.0.0.5"}, "loop.example.com": {"127.0.0.2"}}
	a.LookupHost = func(h string) ([]string, error) {
		if v, ok := dns[h]; ok {
			return v, nil
		}
		return nil, errors.New("no such host")
	}
	notes := func(url string) string {
		t.Helper()
		cfg, err := config.Parse([]byte(strings.Replace(oshOAuthYml, "https://gw.example.com:8443/mcp", url, 1)))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(a.gatewayHostNotes(cfg), "\n")
	}
	if n := notes("https://gw.example.com:8443/mcp"); n != "" {
		t.Errorf("the host's name: %s", n)
	}
	for url, want := range map[string]string{
		"https://host.openshell.internal:8443/mcp": "only a private CA's certificate can carry",
		"https://127.0.0.1:8443/mcp":               "only a private CA's certificate can carry",
		"https://loop.example.com:8443/mcp":        "loop.example.com resolves to 127.0.0.2 on this host, which OpenShell's proxy never connects to",
		"https://nowhere.example.com:8443/mcp":     "nowhere.example.com: no such host; OpenShell resolves the name by DNS, not /etc/hosts",
		"https://10.0.0.5:8443/mcp":                "an IP address",
	} {
		n := notes(url)
		if !strings.Contains(n, want) || !strings.Contains(n, "is not the gateway's http.audience") {
			t.Errorf("%s: %s", url, n)
		}
	}
	// An agent on the host's socket: none.
	cfg, _ := config.Parse([]byte("version: 1\nname: sb\nmodel: {provider: openai, name: m, endpoint: \"http://127.0.0.1:8008/v1\"}\ngateway: {servers: [zypp]}\n"))
	if n := a.gatewayHostNotes(cfg); len(n) != 0 {
		t.Errorf("a systemd agent: %v", n)
	}
	// check prints them, and the configuration is still valid.
	var out strings.Builder
	a.Out = &out
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(strings.Replace(oshOAuthYml, "https://gw.example.com:8443/mcp", "https://host.openshell.internal:8443/mcp", 1)), 0o644)
	if err := a.Check(f); err != nil || !strings.Contains(out.String(), "agent sb: note: gateway.url https://host.openshell.internal:8443/mcp") {
		t.Errorf("check: %v %q", err, out.String())
	}
}
