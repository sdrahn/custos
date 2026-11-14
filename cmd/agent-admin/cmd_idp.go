package main

import (
	"os"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/idp"
)

// The site's identity provider, which issues an OpenShell agent's tokens
// (CONTRACT.md §16a).
var idpCommands = []command{
	{"idp-client", func(a *admin.Admin, args []string) error {
		fs := flags("idp-client")
		gw := fs.String("gateway-config", "", "")
		keycloak := fs.Bool("keycloak", false, "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		a.GatewayConfig = *gw
		return idp.Client(a, pos[0], *keycloak)
	}},
	{"check-token", func(a *admin.Admin, args []string) error {
		fs := flags("check-token")
		gw := fs.String("gateway-config", "", "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		a.GatewayConfig = *gw
		return idp.CheckToken(a, pos[0], os.Stdin)
	}},
}
