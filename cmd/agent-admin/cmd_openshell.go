package main

import (
	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/oshsetup"
)

// OpenShell on this host for OpenShell agents (CONTRACT.md §16a).
var openshellCommands = []command{
	{"openshell-setup", func(a *admin.Admin, args []string) error {
		fs := flags("openshell-setup")
		o := oshsetup.Options{}
		fs.StringVar(&o.User, "user", "", "")
		fs.StringVar(&o.Driver, "driver", "", "")
		fs.StringVar(&o.Gateway, "gateway", "openshell", "")
		fs.StringVar(&o.CA, "ca", "", "")
		fs.BoolVar(&o.Check, "check", false, "")
		if _, err := parse(fs, args, 0); err != nil {
			return err
		}
		return oshsetup.Setup(a, o)
	}},
}
