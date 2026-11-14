package admin

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/sdrahn/custos/internal/config"
)

// The name an OpenShell agent reaches the gateway by (#131): the host's
// own, resolving to a network address and the gateway's audience, needs no
// note; the alias, a loopback answer, a name that does not resolve, or a
// URL that is not the audience do.
func TestGatewayHostNotes(t *testing.T) {
	a, _ := newAdmin(t)
	os.MkdirAll(a.Path(filepath.Dir(GatewayConfig)), 0o755)
	os.WriteFile(a.Path(GatewayConfig), []byte("http:\n  issuer: https://idp.example.com/realms/mcp\n  audience: https://gw.example.com:8443/mcp\n"), 0o644)
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

// The DNS-only lookup asks the nameserver as OpenShell's policy DNS does:
// an address it has, a name it does not know (NXDOMAIN).
func TestDNSQuery(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil || len(q.Questions) != 1 {
				continue
			}
			r := dnsmessage.Message{Header: dnsmessage.Header{ID: q.Header.ID, Response: true}, Questions: q.Questions}
			qq := q.Questions[0]
			if qq.Type == dnsmessage.TypeAAAA && qq.Name.String() == "gw.test." {
				continue // forwarded, and the upstream never answers
			}
			switch {
			case qq.Name.String() != "gw.test.":
				r.Header.RCode = dnsmessage.RCodeNameError
			case qq.Type == dnsmessage.TypeA:
				r.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: qq.Name, Type: qq.Type, Class: qq.Class, TTL: 5},
					Body: &dnsmessage.AResource{A: [4]byte{10, 0, 0, 5}}}}
			}
			b, _ := r.Pack()
			pc.WriteTo(b, from)
		}
	}()
	name := func(s string) dnsmessage.Name { n, _ := dnsmessage.NewName(s); return n }
	got, rc, err := dnsQuery(pc.LocalAddr().String(), name("gw.test."), dnsmessage.TypeA)
	if err != nil || rc != dnsmessage.RCodeSuccess || strings.Join(got, ",") != "10.0.0.5" {
		t.Errorf("gw.test: %v %v %v", got, rc, err)
	}
	got, rc, err = dnsQuery(pc.LocalAddr().String(), name("only-in-hosts.test."), dnsmessage.TypeA)
	if err != nil || rc != dnsmessage.RCodeNameError || len(got) != 0 {
		t.Errorf("an unknown name: %v %v %v", got, rc, err)
	}
	// A and AAAA: the A answer counts though AAAA times out.
	if got, err := lookupAt(pc.LocalAddr().String(), "gw.test"); err != nil || strings.Join(got, ",") != "10.0.0.5" {
		t.Errorf("lookupAt gw.test: %v %v", got, err)
	}
	if _, err := lookupAt(pc.LocalAddr().String(), "only-in-hosts.test"); err == nil || !strings.Contains(err.Error(), "not in DNS") {
		t.Errorf("lookupAt an unknown name: %v", err)
	}
	// No nameserver: said so.
	a, _ := newAdmin(t)
	if _, err := a.lookupDNS("gw.test"); err == nil {
		t.Error("no resolv.conf: no error")
	}
}
