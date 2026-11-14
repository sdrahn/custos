package admin

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/host"
)

// A systemd agent's network (CONTRACT.md §3a, security review M4): the
// gateway (its Unix socket) and the model endpoint, nothing else.
// agent@.service denies every address but the host's own
// (IPAddressDeny=any, IPAddressAllow=localhost); each start, before the
// agent runs, Fence adds what the model endpoint's host resolves to and
// the nameservers the agent resolves it with, as runtime properties of
// that start. Should Fence fail, the unit's denial stands: the agent
// reaches nothing but the host. More (agent_connect_any, a proxy) is an
// administrator's drop-in with IPAddressAllow=.

// ResolvConf is where the nameservers come from.
const ResolvConf = "/etc/resolv.conf"

// FenceAddresses are the addresses a systemd agent may reach besides
// the host's own: its model endpoint's and the nameservers.
func (a *Admin) FenceAddresses(cfg *config.Config) ([]string, error) {
	if cfg.Model.Endpoint == "" {
		return nil, nil
	}
	u, err := url.Parse(cfg.Model.Endpoint)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("model.endpoint %q: no host", cfg.Model.Endpoint)
	}
	h := u.Hostname()
	var out []string
	add := func(s string) {
		if ip := net.ParseIP(s); ip != nil && !ip.IsLoopback() && !slices.Contains(out, ip.String()) {
			out = append(out, ip.String())
		}
	}
	if ip := net.ParseIP(h); ip != nil {
		add(h)
		return out, nil
	}
	lookup := a.LookupHost
	if lookup == nil {
		lookup = net.LookupHost
	}
	// As the agent resolves it (/etc/hosts, then DNS).
	addrs, err := lookup(h)
	if err != nil {
		return nil, fmt.Errorf("model.endpoint's host %s: %w", h, err)
	}
	for _, s := range addrs {
		add(s)
	}
	if b, err := os.ReadFile(a.Path(ResolvConf)); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if f := strings.Fields(strings.SplitN(l, "#", 2)[0]); len(f) >= 2 && f[0] == "nameserver" {
				add(f[1])
			}
		}
	}
	return out, nil
}

// Fence lets a systemd agent's unit reach its model endpoint, for this
// start (agent@.service's ExecStartPre).
func (a *Admin) Fence(name string) error {
	if err := host.CheckName(name); err != nil {
		return err
	}
	cfg, err := config.Load(a.AgentDir(name))
	if err != nil {
		return err
	}
	if cfg.Sandbox.Kind == "openshell" {
		return nil // OpenShell fences its sandbox (§16a)
	}
	addrs, err := a.FenceAddresses(cfg)
	if err != nil {
		return fmt.Errorf("agent %s: %w; it reaches only this host", name, err)
	}
	if len(addrs) == 0 {
		return nil // a model on this host: localhost is allowed already
	}
	args := []string{"set-property", "--runtime", "agent@" + name + ".service"}
	for _, ip := range addrs {
		args = append(args, "IPAddressAllow="+ip)
	}
	return a.Run(nil, "systemctl", args...)
}
