package main

import (
	"fmt"

	"github.com/sdrahn/custos/internal/admin"
)

// What a host allows at all: the task categories enabled on it
// (CONTRACT.md §6b), the seal over that and the agents' data, and the
// token issuers trusted for agents.
var enablementCommands = []command{
	{"enable", func(a *admin.Admin, args []string) error {
		fs := flags("enable")
		all := fs.Bool("all", false, "")
		cats, err := parse(fs, args, 0)
		if err != nil {
			return err
		}
		return a.Enable(by(), cats, *all)
	}},
	{"disable", func(a *admin.Admin, args []string) error {
		fs := flags("disable")
		all := fs.Bool("all", false, "")
		cats, err := parse(fs, args, 0)
		if err != nil {
			return err
		}
		return a.Disable(by(), cats, *all)
	}},
	{"diagnostics-only", func(a *admin.Admin, args []string) error {
		if _, err := parse(flags("diagnostics-only"), args, 0); err != nil {
			return err
		}
		return a.InitDiagnosticsOnly(by())
	}},
	{"seal", func(a *admin.Admin, args []string) error {
		fs := flags("seal")
		agents, rekey := fs.Bool("agents", false, ""), fs.Bool("rekey", false, "")
		var accept list
		fs.Var(&accept, "accept", "")
		pos, err := parse(fs, args, 0)
		if err != nil {
			return err
		}
		if len(pos) > 0 {
			return fmt.Errorf("seal: %q: want --accept NAME\n%s", pos[0], usage)
		}
		switch {
		case *rekey && !*agents:
			return a.Rekey(by(), accept)
		case *agents && !*rekey:
			return a.SealAgents(accept)
		}
		return fmt.Errorf("seal: --agents or --rekey\n%s", usage)
	}},
	{"issuers", func(a *admin.Admin, args []string) error {
		fs := flags("issuers")
		var add, remove list
		fs.Var(&add, "add", "")
		fs.Var(&remove, "remove", "")
		pos, err := parse(fs, args, 0)
		if err != nil {
			return err
		}
		if len(pos) > 0 {
			return fmt.Errorf("issuers: %q: want --add ISSUER or --remove ISSUER\n%s", pos[0], usage)
		}
		return a.Issuers(add, remove)
	}},
}
