package main

import (
	"os"

	"github.com/sdrahn/custos/internal/admin"
)

// The agents' credentials: stored encrypted, listed by name only.
var credentialCommands = []command{
	{"set-credential", func(a *admin.Admin, args []string) error {
		pos, err := parse(flags("set-credential"), args, 2)
		if err != nil {
			return err
		}
		return a.SetCredential(pos[0], pos[1], os.Stdin)
	}},
	{"credentials", func(a *admin.Admin, args []string) error {
		names, err := parse(flags("credentials"), args, 0)
		if err != nil {
			return err
		}
		return printJSON(a.Credentials(names...))
	}},
	{"remove-credential", func(a *admin.Admin, args []string) error {
		fs := flags("remove-credential")
		force := fs.Bool("force", false, "")
		pos, err := parse(fs, args, 2)
		if err != nil {
			return err
		}
		return a.RemoveCredential(pos[0], pos[1], *force)
	}},
}
