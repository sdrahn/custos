package gatewayconf

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// The gateway's settings as the gateway reads them: /etc over /usr/etc,
// the drop-ins of gateway.d over that, key by key; --gateway-config alone.
func TestReadHTTP(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	write(VendorConfig, "http:\n  issuer: https://vendor/realms/x\n  audience: https://vendor/mcp\n")
	h, err := ReadHTTP(root, "")
	if err != nil || h.Issuer != "https://vendor/realms/x" {
		t.Fatalf("vendor default: %+v %v", h, err)
	}
	write(Config, "http:\n  issuer: https://etc/realms/x\n  audience: https://etc/mcp\n  scopes: [a]\n")
	write(filepath.Join(DropInDir, "50-mcp-gateway-admin.yaml"), "http:\n  audience: https://dropin/mcp\n")
	write(filepath.Join(DropInDir, "60-more.yaml"), "http:\n  scopes: [b, c]\n")
	write(filepath.Join(DropInDir, ".hidden.yaml"), "http:\n  issuer: https://hidden\n")
	h, err = ReadHTTP(root, "")
	if err != nil || h.Issuer != "https://etc/realms/x" || h.Audience != "https://dropin/mcp" || strings.Join(h.Scopes, " ") != "b c" {
		t.Fatalf("/etc with drop-ins: %+v %v", h, err)
	}
	other := filepath.Join(t.TempDir(), "gw.yaml")
	os.WriteFile(other, []byte("http:\n  issuer: https://explicit/realms/x\n  audience: https://explicit/mcp\n"), 0o644)
	h, err = ReadHTTP(root, other)
	if err != nil || h.Issuer != "https://explicit/realms/x" || len(h.Scopes) != 0 {
		t.Fatalf("--gateway-config: %+v %v", h, err)
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
	if _, err := LookupDNS(t.TempDir(), "gw.test"); err == nil {
		t.Error("no resolv.conf: no error")
	}
}
