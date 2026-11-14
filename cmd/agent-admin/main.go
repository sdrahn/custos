// agent-admin: administrative actions on agents, run as root (by
// Cockpit with administrative access, or by hand). CONTRACT.md §11.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/idp"
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

func run(a *admin.Admin, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	cmd, args := args[0], args[1:]
	need := func(n int) error {
		if len(args) < n {
			return fmt.Errorf("%s: missing arguments\n%s", cmd, usage)
		}
		return nil
	}
	flag := func(name string) (string, bool) {
		for i, x := range args {
			if x == name && i+1 < len(args) {
				return args[i+1], true
			}
		}
		return "", false
	}
	has := func(name string) bool {
		for _, x := range args {
			if x == name {
				return true
			}
		}
		return false
	}
	from := func() (string, error) {
		for i, x := range args {
			if x == "--from" && i+1 < len(args) {
				return args[i+1], nil
			}
		}
		return "", fmt.Errorf("%s: --from FILE is needed", cmd)
	}
	switch cmd {
	case "list":
		names, err := a.List()
		if err == nil && len(names) > 0 {
			fmt.Println(strings.Join(names, "\n"))
		}
		return err
	case "show":
		p, err := a.Show(args...)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	case "check":
		if err := need(1); err != nil {
			return err
		}
		return a.Check(args[0])
	case "create", "edit":
		if err := need(1); err != nil {
			return err
		}
		if t, ok := flag("--template"); ok && cmd == "create" {
			f, _ := flag("--from")
			return a.CreateFromTemplate(args[0], t, f)
		}
		f, err := from()
		if err != nil {
			return err
		}
		if cmd == "create" {
			return a.Create(args[0], f)
		}
		return a.Edit(args[0], f)
	case "delete":
		if err := need(1); err != nil {
			return err
		}
		return a.Delete(args[0], len(args) > 1 && args[1] == "--keep-state")
	case "set-credential":
		if err := need(2); err != nil {
			return err
		}
		return a.SetCredential(args[0], args[1], os.Stdin)
	case "credentials":
		c, err := a.Credentials(args...)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(c)
	case "remove-credential":
		if err := need(2); err != nil {
			return err
		}
		return a.RemoveCredential(args[0], args[1], has("--force"))
	case "enable":
		var cats []string
		for _, x := range args {
			if x != "--all" {
				cats = append(cats, x)
			}
		}
		return a.Enable(by(), cats, has("--all"))
	case "disable":
		var cats []string
		for _, x := range args {
			if x != "--all" {
				cats = append(cats, x)
			}
		}
		return a.Disable(by(), cats, has("--all"))
	case "diagnostics-only":
		return a.InitDiagnosticsOnly(by())
	case "seal":
		if len(args) == 0 || (args[0] != "--agents" && args[0] != "--rekey") {
			return fmt.Errorf("seal: --agents or --rekey\n%s", usage)
		}
		var accept []string
		for i := 1; i < len(args); i++ {
			if args[i] != "--accept" || i+1 >= len(args) {
				return fmt.Errorf("seal: %q: want --accept NAME\n%s", args[i], usage)
			}
			accept = append(accept, args[i+1])
			i++
		}
		if args[0] == "--rekey" {
			return a.Rekey(by(), accept)
		}
		return a.SealAgents(accept)
	case "issuers":
		var add, remove []string
		for i := 0; i < len(args); i++ {
			switch {
			case args[i] == "--add" && i+1 < len(args):
				add = append(add, args[i+1])
				i++
			case args[i] == "--remove" && i+1 < len(args):
				remove = append(remove, args[i+1])
				i++
			default:
				return fmt.Errorf("issuers: %q: want --add ISSUER or --remove ISSUER\n%s", args[i], usage)
			}
		}
		return a.Issuers(add, remove)
	case "mode":
		if err := need(2); err != nil {
			return err
		}
		return a.SetMode(args[0], args[1])
	case "permissions":
		if err := need(1); err != nil {
			return err
		}
		return a.SetPermissions(args[0], os.Stdin)
	case "categories":
		c, err := a.Categories()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(c)
	case "compare":
		var names []string
		t, _ := flag("--template")
		for i := 0; i < len(args); i++ {
			if args[i] == "--template" {
				i++
				continue
			}
			names = append(names, args[i])
		}
		c, err := a.Compare(names, t)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(c)
	case "apply-template":
		if err := need(1); err != nil {
			return err
		}
		t, _ := flag("--template")
		return a.ApplyTemplate(args[0], t, has("--configuration"), has("--permissions"))
	case "draft":
		if err := need(1); err != nil {
			return err
		}
		t, _ := flag("--template")
		b, err := a.Draft(args[0], t)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(b)
		return err
	case "defaults":
		if f, ok := flag("--from"); ok {
			b, err := a.ReadFrom(f)
			if err != nil {
				return err
			}
			return a.SetDefaults(b)
		}
		if has("--clear") {
			return a.SetDefaults(nil)
		}
		d, err := a.GetDefaults()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(d)
	case "feedback":
		if err := need(2); err != nil {
			return err
		}
		f := admin.Feedback{Hypotheses: map[string]string{}}
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--executed":
				f.Executed = true
			case "--hypothesis", "--plan", "--comment":
				if i+1 >= len(args) {
					return fmt.Errorf("%s: a value is needed", args[i])
				}
				v := args[i+1]
				switch args[i] {
				case "--hypothesis":
					id, verdict, ok := strings.Cut(v, "=")
					if !ok {
						return fmt.Errorf("--hypothesis %s: want ID=confirmed|rejected|unsure", v)
					}
					f.Hypotheses[id] = verdict
				case "--plan":
					f.Plan = v
				case "--comment":
					f.Comment = v
				}
				i++
			default:
				return fmt.Errorf("feedback: unknown argument %q", args[i])
			}
		}
		if len(f.Hypotheses) == 0 {
			f.Hypotheses = nil
		}
		return a.RecordFeedback(args[0], args[1], by(), f)
	case "playbooks":
		p, err := a.Playbooks()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	case "templates":
		t, err := a.ListTemplates()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(t)
	case "readiness":
		if err := need(1); err != nil {
			return err
		}
		r, err := a.Readiness(args[0], args[1:]...)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	case "promote":
		if err := need(2); err != nil {
			return err
		}
		f, ok := flag("--envelope")
		if !ok {
			return fmt.Errorf("promote: --envelope FILE is needed")
		}
		reason, _ := flag("--reason")
		b, err := a.ReadFrom(f)
		if err != nil {
			return err
		}
		return a.Promote(args[0], args[1], by(), admin.Promotion{Envelope: b, Reason: reason, DespiteReadiness: has("--despite-readiness")})
	case "demote":
		if err := need(2); err != nil {
			return err
		}
		reason, _ := flag("--reason")
		return a.Demote(args[0], args[1], by(), reason)
	case "review":
		if err := need(1); err != nil {
			return err
		}
		if len(args) >= 5 && !strings.HasPrefix(args[2], "--") {
			reason, _ := flag("--reason")
			return a.Review(args[0], args[1], args[2], args[3], args[4], by(), reason)
		}
		q, err := a.ReviewQueue(args[0], args[1:]...)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(q)
	case "evaluate":
		d, err := a.Evaluate()
		for _, x := range d {
			fmt.Printf("agent %s: category %s demoted: %s\n", x.Agent, x.Category, x.Reason)
			if x.Notice != "" {
				fmt.Printf("agent %s: category %s: %s\n", x.Agent, x.Category, x.Notice)
			}
		}
		return err
	case "owners":
		if err := need(1); err != nil {
			return err
		}
		return a.SetOwners(args[0], args[1:])
	case "pause", "resume":
		if err := need(1); err != nil {
			return err
		}
		return a.Pause(args[0], by(), cmd == "pause")
	case "trigger":
		if err := need(1); err != nil {
			return err
		}
		return a.Trigger(args[0])
	case "idp-client":
		if err := need(1); err != nil {
			return err
		}
		a.GatewayConfig, _ = flag("--gateway-config")
		return idp.Client(a, args[0], has("--keycloak"))
	case "fence":
		if err := need(1); err != nil {
			return err
		}
		return a.Fence(args[0])
	case "check-token":
		if err := need(1); err != nil {
			return err
		}
		a.GatewayConfig, _ = flag("--gateway-config")
		return idp.CheckToken(a, args[0], os.Stdin)
	case "event":
		if err := need(1); err != nil {
			return err
		}
		return a.Event(args[0])
	case "stop-all":
		return a.StopAll(by())
	case "release-all":
		return a.ReleaseAll(by())
	case "-h", "--help", "help":
		fmt.Println(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n%s", cmd, usage)
}
