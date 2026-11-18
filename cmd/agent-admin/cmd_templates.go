package main

import (
	"flag"
	"os"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/templates"
)

// Templates (CONTRACT.md §14a), the model defaults (§14b), comparing an
// agent with its template, and the playbooks (§14c). create --template
// is with create, in cmd_agents.go.
var templateCommands = []command{
	{"templates", func(a *admin.Admin, args []string) error {
		if _, err := parse(flags("templates"), args, 0); err != nil {
			return err
		}
		return printJSON(templates.List(a))
	}},
	{"playbooks", func(a *admin.Admin, args []string) error {
		if _, err := parse(flags("playbooks"), args, 0); err != nil {
			return err
		}
		return printJSON(a.Playbooks())
	}},
	{"draft", func(a *admin.Admin, args []string) error {
		fs := flags("draft")
		tmpl := fs.String("template", "", "")
		be := backendFlags(fs)
		gatewayConfigFlag(a, fs)
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		b, err := templates.DraftOn(a, pos[0], *tmpl, *be)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(b)
		return err
	}},
	{"defaults", func(a *admin.Admin, args []string) error {
		fs := flags("defaults")
		from, clear := fs.String("from", "", ""), fs.Bool("clear", false, "")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		if *from != "" {
			b, err := a.ReadFrom(*from)
			if err != nil {
				return err
			}
			return templates.SetDefaults(a, b)
		}
		if *clear {
			return templates.SetDefaults(a, nil)
		}
		return printJSON(templates.GetDefaults(a))
	}},
	{"compare", func(a *admin.Admin, args []string) error {
		fs := flags("compare")
		tmpl := fs.String("template", "", "")
		names, err := parse(fs, args, 0)
		if err != nil {
			return err
		}
		return printJSON(templates.Compare(a, names, *tmpl))
	}},
	{"apply-template", func(a *admin.Admin, args []string) error {
		fs := flags("apply-template")
		tmpl := fs.String("template", "", "")
		cfg, perms := fs.Bool("configuration", false, ""), fs.Bool("permissions", false, "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		return templates.Apply(a, pos[0], *tmpl, *cfg, *perms)
	}},
}

// backendFlags are where an agent made from a template runs (create and
// draft --template, CONTRACT.md §14a).
func backendFlags(fs *flag.FlagSet) *templates.Backend {
	b := &templates.Backend{}
	fs.StringVar(&b.Kind, "sandbox", "", "")
	fs.StringVar(&b.Driver, "openshell-driver", "", "")
	fs.StringVar(&b.Gateway, "openshell-gateway", "", "")
	fs.StringVar(&b.TokenURL, "token-url", "", "")
	fs.BoolVar(&b.StaticToken, "static-token", false, "")
	return b
}

// gatewayConfigFlag is --gateway-config FILE: the gateway configuration
// read instead of the installed one (OpenShell's parts of a draft).
func gatewayConfigFlag(a *admin.Admin, fs *flag.FlagSet) {
	fs.StringVar(&a.GatewayConfig, "gateway-config", "", "")
}
