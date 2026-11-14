package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/templates"
)

// The agents themselves: lifecycle, mode, permissions, owners, schedules
// and events, the network fence, and the emergency stop.
var agentCommands = []command{
	{"list", func(a *admin.Admin, args []string) error {
		if _, err := parse(flags("list"), args, 0); err != nil {
			return err
		}
		names, err := a.List()
		if err == nil && len(names) > 0 {
			fmt.Println(strings.Join(names, "\n"))
		}
		return err
	}},
	{"show", func(a *admin.Admin, args []string) error {
		names, err := parse(flags("show"), args, 0)
		if err != nil {
			return err
		}
		return printJSON(a.Show(names...))
	}},
	{"check", func(a *admin.Admin, args []string) error {
		pos, err := parse(flags("check"), args, 1)
		if err != nil {
			return err
		}
		return a.Check(pos[0])
	}},
	{"create", func(a *admin.Admin, args []string) error {
		fs := flags("create")
		tmpl, from := fs.String("template", "", ""), fs.String("from", "", "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if *tmpl != "" {
			return templates.Create(a, pos[0], *tmpl, *from)
		}
		if *from == "" {
			return fmt.Errorf("create: --from FILE is needed")
		}
		return a.Create(pos[0], *from)
	}},
	{"edit", func(a *admin.Admin, args []string) error {
		fs := flags("edit")
		from := fs.String("from", "", "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		if *from == "" {
			return fmt.Errorf("edit: --from FILE is needed")
		}
		return a.Edit(pos[0], *from)
	}},
	{"delete", func(a *admin.Admin, args []string) error {
		fs := flags("delete")
		keep := fs.Bool("keep-state", false, "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		return a.Delete(pos[0], *keep)
	}},
	{"mode", func(a *admin.Admin, args []string) error {
		pos, err := parse(flags("mode"), args, 2)
		if err != nil {
			return err
		}
		return a.SetMode(pos[0], pos[1])
	}},
	{"permissions", func(a *admin.Admin, args []string) error {
		pos, err := parse(flags("permissions"), args, 1)
		if err != nil {
			return err
		}
		return a.SetPermissions(pos[0], os.Stdin)
	}},
	{"owners", func(a *admin.Admin, args []string) error {
		pos, err := parse(flags("owners"), args, 1)
		if err != nil {
			return err
		}
		return a.SetOwners(pos[0], pos[1:])
	}},
	{"pause", func(a *admin.Admin, args []string) error { return pause(a, "pause", args) }},
	{"resume", func(a *admin.Admin, args []string) error { return pause(a, "resume", args) }},
	{"trigger", func(a *admin.Admin, args []string) error {
		pos, err := parse(flags("trigger"), args, 1)
		if err != nil {
			return err
		}
		return a.Trigger(pos[0])
	}},
	{"event", func(a *admin.Admin, args []string) error {
		pos, err := parse(flags("event"), args, 1)
		if err != nil {
			return err
		}
		return a.Event(pos[0])
	}},
	{"fence", func(a *admin.Admin, args []string) error {
		pos, err := parse(flags("fence"), args, 1)
		if err != nil {
			return err
		}
		return a.Fence(pos[0])
	}},
	{"stop-all", func(a *admin.Admin, args []string) error {
		if _, err := parse(flags("stop-all"), args, 0); err != nil {
			return err
		}
		return a.StopAll(by())
	}},
	{"release-all", func(a *admin.Admin, args []string) error {
		if _, err := parse(flags("release-all"), args, 0); err != nil {
			return err
		}
		return a.ReleaseAll(by())
	}},
}

func pause(a *admin.Admin, cmd string, args []string) error {
	pos, err := parse(flags(cmd), args, 1)
	if err != nil {
		return err
	}
	return a.Pause(pos[0], by(), cmd == "pause")
}
