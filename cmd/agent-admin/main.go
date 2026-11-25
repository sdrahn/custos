// agent-admin: administrative actions on agents, run as root (by
// Cockpit with administrative access, or by hand). CONTRACT.md §11.
//
// The commands are a table (commands, below). Each group of commands has
// its file, cmd_<group>.go, and is built on its own package:
//
//	cmd_agents.go       internal/admin            lifecycle, mode, permissions, the stop
//	cmd_credentials.go  internal/admin            the agents' credentials
//	cmd_enablement.go   internal/admin            task categories on the host, the seal, the issuers
//	cmd_templates.go    internal/admin/templates  templates, model defaults, compare and apply
//	cmd_promotion.go    internal/admin/promotion  readiness, promote, demote, review, evaluate, feedback
//	cmd_idp.go          internal/admin/idp        the identity provider
//	cmd_openshell.go    internal/admin/oshsetup   OpenShell on the host
//	cmd_chat.go         internal/admin/chat       a conversation with an agent on the terminal
//
// help.go is the help: the commands in groups of what they are for, each
// with its synopsis and what it does. A new command gets its entry there.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sdrahn/custos/internal/admin"
)

func main() {
	a := admin.New()
	a.By = by()
	if err := run(a, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-admin:", err)
		os.Exit(1)
	}
}

func by() string {
	if u := os.Getenv("SUDO_USER"); u != "" {
		return u
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "root"
}

// command is one agent-admin command.
type command struct {
	name string
	// run gets the arguments after the command's name.
	run func(a *admin.Admin, args []string) error
}

// groups are the groups' tables; a new group is added here.
var groups = [][]command{agentCommands, credentialCommands, enablementCommands, templateCommands, promotionCommands, idpCommands, openshellCommands, chatCommands}

// commands are all of them, by name.
var commands = func() map[string]command {
	m := map[string]command{}
	for _, group := range groups {
		for _, c := range group {
			m[c.name] = c
		}
	}
	return m
}()

func run(a *admin.Admin, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	switch args[0] {
	case "-h", "--help", "help":
		if args[0] == "help" && len(args) > 1 {
			h, err := commandHelp(args[1])
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Out, h)
			return nil
		}
		fmt.Fprintln(a.Out, usage)
		return nil
	}
	c, ok := commands[args[0]]
	if !ok {
		return fmt.Errorf("unknown command %q\n%s", args[0], usageLine)
	}
	err := c.run(a, args[1:])
	// COMMAND --help: the command's help, as help COMMAND.
	var h helpRequest
	if errors.As(err, &h) {
		text, _ := commandHelp(args[0])
		fmt.Fprintln(a.Out, text)
		return nil
	}
	return err
}

// helpRequest is a command's --help, which parse finds.
type helpRequest struct{ cmd string }

func (h helpRequest) Error() string { return commandUsage(h.cmd) }

// flags is a command's flag set: its errors are the caller's to word.
func flags(cmd string) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parse reads args with fs and returns the positional arguments, of
// which the command needs at least need. Options and arguments may come
// in any order (create NAME --template T, as well as create --template T
// NAME); "-" is an argument (standard input), and everything after "--"
// is one, whatever it looks like.
func parse(fs *flag.FlagSet, args []string, need int) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, helpRequest{fs.Name()}
			}
			return nil, fmt.Errorf("%s: %v\n%s", fs.Name(), err, commandUsage(fs.Name()))
		}
		rest := fs.Args()
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" && !(n > 1 && takesValue(fs, args[n-2])) {
			pos = append(pos, rest...)
			break
		}
		args = rest
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	if len(pos) < need {
		return nil, fmt.Errorf("%s: missing arguments\n%s", fs.Name(), commandUsage(fs.Name()))
	}
	return pos, nil
}

// takesValue says whether arg is an option of fs whose value is the next
// argument (--from FILE, not --from=FILE and not a switch).
func takesValue(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
	if name == arg || strings.Contains(name, "=") {
		return false
	}
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !b.IsBoolFlag()
}

// list is an option that may be given more than once.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// printJSON writes v to standard output, as a control plane reads it.
func printJSON(v any, err error) error {
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
