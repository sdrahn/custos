package main

import (
	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/chat"
)

// A conversation with an agent on the terminal (CONTRACT.md §11).
var chatCommands = []command{
	{"chat", func(a *admin.Admin, args []string) error {
		fs := flags("chat")
		o := chat.Options{}
		fs.StringVar(&o.Thread, "thread", "", "")
		fs.StringVar(&o.Message, "message", "", "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		o.Name = pos[0]
		return chat.Run(a, o)
	}},
}
