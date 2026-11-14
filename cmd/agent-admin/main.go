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

const usage = `usage: agent-admin COMMAND …
  list                                   the agents
  show [NAME…]                           the gateway data and the emergency stop, as JSON
  check NAME|FILE                        validate a configuration (FILE -: stdin)
  create NAME --from FILE                install a new agent (supervised, no permissions; FILE -: stdin)
  create NAME --template TEMPLATE [--from FILE]
                                         install a new agent from a template: its configuration (or FILE's),
                                         permissions and mode
  templates                              the agent templates (CONTRACT.md §14a), as JSON
  playbooks                              the diagnostic playbooks (CONTRACT.md §14c), as JSON
  compare [NAME…] [--template TEMPLATE]  each agent against its template (CONTRACT.md §14a), as JSON: the
                                         configuration's and the permissions' differences (--template: for one
                                         agent created before its template was recorded)
  apply-template NAME [--template TEMPLATE] [--configuration] [--permissions]
                                         give the agent its template's configuration and/or permissions (both
                                         without either option); it keeps its name, model, schedules, events,
                                         sandbox, gateway connection, mode, categories and credentials
  feedback NAME ACTIVITY [--hypothesis ID=confirmed|rejected|unsure…] [--plan useful|not_useful]
           [--executed] [--comment TEXT]  feedback on an activity's diagnosis report (CONTRACT.md §4a)
  draft NAME [--template TEMPLATE]       a new agent's starting configuration (blank or the template's),
                                         with the host's model defaults
  defaults [--from FILE | --clear]       the model defaults for new agents (CONTRACT.md §14b), as JSON;
                                         --from sets them (FILE -: stdin), --clear removes them
  edit NAME --from FILE                  replace its configuration and apply it (restart; OpenShell: its sandbox; FILE -: stdin)
  delete NAME [--keep-state]             remove an agent (its user stays)
  set-credential NAME CRED               encrypt a secret read from stdin
  idp-client NAME [--keycloak]           the OAuth client an OpenShell agent needs at the gateway's identity
                                         provider; --keycloak: a script that creates it there (kcadm.sh)
  fence NAME                             let a systemd agent's unit reach its model endpoint, for this start
                                         (agent@.service runs it; the unit denies every other address)
  check-token NAME < secret              mint a token with the agent's OAuth client and check its claims
                                         as the gateway will (iss, sub, aud, scope, lifetime)
                                         (both: --gateway-config FILE instead of the installed gateway's)
  credentials [NAME…]                    the agents' credentials: names, what uses them, missing ones; never
                                         the values (JSON)
  remove-credential NAME CRED [--force]  remove a stored credential (--force: one its model uses)
  enable CATEGORY… | --all               enable task categories on a diagnostics-only host (CONTRACT.md §6b);
                                         they start supervised
  disable CATEGORY… | --all              disable task categories again; --all: back to diagnostics-only.
                                         Refused while an agent has permissions in them or one promoted
  diagnostics-only                       make a fresh host diagnostics-only (the bundled install, custos-setup)
  seal --agents [--accept NAME…]         make the host's seal cover the agents' data and their token issuers
                                         (CONTRACT.md §6b): signs them; a change outside agent-admin then
                                         denies that agent everything. Once it has, a file that does not match
                                         the seal is signed only when --accept names it (_http: the issuers)
  seal --rekey [--accept NAME…]          a new key for the seal (a lost one): signs again what matches the
                                         seal, and what --accept names (_enablement: the categories' state)
  issuers [--add ISSUER…] [--remove ISSUER…]
                                         the token issuers trusted for agents over HTTPS (_http.yaml); signed
                                         on a host whose seal covers them
  mode NAME advisory|supervised|bounded  set the autonomy mode (enforced by mcp-gateway)
  owners NAME [RULE…]                    who may approve its calls besides the servers' approvers
                                         (user:NAME, group:NAME, role:NAME, remote-user:SUB, remote-group:NAME); none clears
  permissions NAME                       replace its gateway permissions (YAML list on stdin)
  categories                             the task categories (CONTRACT.md §6a), as JSON
  readiness NAME [CATEGORY]              a category's promotion readiness (CONTRACT.md §6a), as JSON
  promote NAME CATEGORY --envelope FILE [--despite-readiness --reason TEXT]
                                         let a category act without approval within its envelope (FILE -: stdin)
  demote NAME CATEGORY [--reason TEXT]   back to supervised (the envelope is kept)
  review NAME [CATEGORY]                 the autonomous actions of promoted categories still to review, as JSON
  review NAME CATEGORY ACTIVITY CALL ok|incident [--reason TEXT]
                                         an approver's verdict; incident (should not have run) demotes it
  evaluate                               the demotion evaluator: demote bounded categories whose override rate
                                         or rollback conditions say so (custos-demote.timer)
  pause NAME | resume NAME               hold an agent / let it go on
  trigger NAME_SCHEDULE                  start a schedule's activity (agent-schedule@.service)
  event NAME_EVENT_UNIT                  start an event's activity for a unit that failed (agent-event@.service)
  stop-all                               emergency stop: every agent, every schedule
  release-all                            undo stop-all`

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
var groups = [][]command{agentCommands, credentialCommands, enablementCommands, templateCommands, promotionCommands, idpCommands}

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
		fmt.Println(usage)
		return nil
	}
	c, ok := commands[args[0]]
	if !ok {
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	return c.run(a, args[1:])
}

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
// NAME); "-" is an argument (standard input).
func parse(fs *flag.FlagSet, args []string, need int) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, fmt.Errorf("%s", usage)
			}
			return nil, fmt.Errorf("%s: %v\n%s", fs.Name(), err, usage)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	if len(pos) < need {
		return nil, fmt.Errorf("%s: missing arguments\n%s", fs.Name(), usage)
	}
	return pos, nil
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
