package admin

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/openshell"
)

// The name an OpenShell agent reaches mcp-gateway by (CONTRACT.md §16a,
// #131). OpenShell's proxy, in the host's network, terminates the agent's
// TLS and connects to the gateway by the name in gateway.url, checking the
// gateway's certificate for it. The host's own name, which an official
// certificate can carry, is the default; host.openshell.internal (what a
// loopback URL becomes) needs a certificate of a private CA. The proxy
// never connects to a loopback or link-local address of a name that is
// not the alias. With Podman, the sandbox resolves the name through
// OpenShell's policy DNS, which asks the first nameserver of the host's
// /etc/resolv.conf and never reads /etc/hosts: the name must be in DNS.

// gatewayHostNotes says what keeps an OpenShell agent from reaching the
// gateway by the name in its gateway.url, or a certificate from covering it.
func (a *Admin) gatewayHostNotes(cfg *config.Config) []string {
	if cfg.Sandbox.Kind != "openshell" || cfg.Gateway.URL == "" {
		return nil
	}
	u, err := url.Parse(cfg.Gateway.URL)
	if err != nil {
		return nil
	}
	var notes []string
	h := u.Hostname()
	ip := net.ParseIP(h)
	switch {
	case h == "localhost" || h == openshell.HostAlias || (ip != nil && ip.IsLoopback()):
		notes = append(notes, fmt.Sprintf("gateway.url %s: the sandbox reaches the gateway as %s, a name only a private CA's certificate can carry; "+
			"with the gateway's own host name an official certificate does (user guide, chapter 11, step 3)", cfg.Gateway.URL, openshell.HostAlias))
	case ip != nil:
		notes = append(notes, fmt.Sprintf("gateway.url %s: an IP address; the gateway's certificate must name it (an official one names the host)", cfg.Gateway.URL))
	default:
		lookup := a.LookupHost
		if lookup == nil {
			lookup = a.lookupDNS
		}
		addrs, err := lookup(h)
		if err != nil {
			notes = append(notes, fmt.Sprintf("gateway.url %s: %s: %v; OpenShell resolves the name by DNS, not /etc/hosts (user guide, chapter 11, step 3)", cfg.Gateway.URL, h, err))
			break
		}
		for _, s := range addrs {
			if x := net.ParseIP(s); x != nil && (x.IsLoopback() || x.IsLinkLocalUnicast() || x.IsUnspecified()) {
				notes = append(notes, fmt.Sprintf("gateway.url %s: %s resolves to %s on this host, which OpenShell's proxy never connects to; "+
					"make it resolve to the host's network address (/etc/hosts or DNS)", cfg.Gateway.URL, h, s))
				break
			}
		}
	}
	if gh, err := a.gatewayHTTP(); err == nil && strings.TrimRight(gh.Audience, "/") != strings.TrimRight(cfg.Gateway.URL, "/") {
		notes = append(notes, fmt.Sprintf("gateway.url %s is not the gateway's http.audience %s: the agent's tokens carry the audience, "+
			"and the gateway's certificate must name the URL's host", cfg.Gateway.URL, gh.Audience))
	}
	return notes
}

// noteGatewayHost prints the notes, which do not stop anything.
func (a *Admin) noteGatewayHost(cfg *config.Config) {
	for _, n := range a.gatewayHostNotes(cfg) {
		fmt.Fprintf(a.Out, "agent %s: note: %s\n", cfg.Name, n)
	}
}

// lookupDNS resolves a name as OpenShell's policy DNS does: A and AAAA
// from the first nameserver of /etc/resolv.conf, never /etc/hosts.
func (a *Admin) lookupDNS(host string) ([]string, error) {
	b, err := os.ReadFile(a.p("/etc/resolv.conf"))
	if err != nil {
		return nil, err
	}
	server := ""
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(strings.SplitN(l, "#", 2)[0])
		if len(f) >= 2 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil {
			server = f[1]
			break
		}
	}
	if server == "" {
		return nil, errors.New("no nameserver in /etc/resolv.conf")
	}
	name, err := dnsmessage.NewName(strings.TrimSuffix(host, ".") + ".")
	if err != nil {
		return nil, err
	}
	var addrs []string
	nx := false
	for _, t := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		got, rcode, err := dnsQuery(net.JoinHostPort(server, "53"), name, t)
		if err != nil {
			return nil, fmt.Errorf("asking the nameserver %s: %w", server, err)
		}
		nx = nx || rcode == dnsmessage.RCodeNameError
		addrs = append(addrs, got...)
	}
	if len(addrs) == 0 {
		if nx {
			return nil, fmt.Errorf("not in DNS (the nameserver %s says it does not exist)", server)
		}
		return nil, fmt.Errorf("no address in DNS (the nameserver %s)", server)
	}
	return addrs, nil
}

func dnsQuery(server string, name dnsmessage.Name, t dnsmessage.Type) ([]string, dnsmessage.RCode, error) {
	id := uint16(time.Now().UnixNano())
	q, err := (&dnsmessage.Message{
		Header:    dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: t, Class: dnsmessage.ClassINET}},
	}).Pack()
	if err != nil {
		return nil, 0, err
	}
	c, err := net.DialTimeout("udp", server, 3*time.Second)
	if err != nil {
		return nil, 0, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write(q); err != nil {
		return nil, 0, err
	}
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, 0, err
		}
		var m dnsmessage.Message
		if m.Unpack(buf[:n]) != nil || m.Header.ID != id || !m.Header.Response {
			continue
		}
		var out []string
		for _, r := range m.Answers {
			switch b := r.Body.(type) {
			case *dnsmessage.AResource:
				out = append(out, net.IP(b.A[:]).String())
			case *dnsmessage.AAAAResource:
				out = append(out, net.IP(b.AAAA[:]).String())
			}
		}
		return out, m.Header.RCode, nil
	}
}
