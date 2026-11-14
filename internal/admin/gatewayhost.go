package admin

import (
	"fmt"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gatewayconf"
)

// gatewayHostNotes says what keeps an OpenShell agent from reaching the
// gateway by the name in its gateway.url (gatewayconf.HostNotes).
func (a *Admin) gatewayHostNotes(cfg *config.Config) []string {
	return gatewayconf.HostNotes(a.Root, a.LookupHost, cfg, a.GatewayConfig)
}

// noteGatewayHost prints the notes, which do not stop anything.
func (a *Admin) noteGatewayHost(cfg *config.Config) {
	for _, n := range a.gatewayHostNotes(cfg) {
		fmt.Fprintf(a.Out, "agent %s: note: %s\n", cfg.Name, n)
	}
}
