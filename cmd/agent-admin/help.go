package main

import (
	"fmt"
	"sort"
	"strings"
)

// help is agent-admin's help: its commands in groups of what they are
// for, each with its synopsis and what it does. A paragraph that starts
// with two spaces is printed as it is (a list, an example); the others
// are wrapped. "agent-admin help" prints all of it, "agent-admin help
// COMMAND" and "COMMAND --help" one command's, and a command line that is
// not understood shows the command's synopsis.
type helpEntry struct {
	synopsis string
	text     []string
}

type helpGroup struct {
	title   string
	entries []helpEntry
}

const backendText = `  --sandbox systemd
  --sandbox openshell [--openshell-driver container|systemd]
                      [--openshell-gateway NAME]
                      [--token-url URL | --static-token]
                      [--gateway-config FILE]`

var help = sortedHelp([]helpGroup{
	{"Agents", []helpEntry{
		{"list", []string{"List the agents."}},
		{"show [NAME…]", []string{"The agents' gateway data and the state of the emergency stop, as JSON."}},
		{"check NAME|FILE", []string{"Validate an agent's configuration, or the configuration in FILE."}},
		{"draft NAME [--template TEMPLATE] [BACKEND]", []string{
			"Print a starting configuration for a new agent: blank or the template's, with the host's model defaults, for BACKEND (as for create).",
		}},
		{"create NAME --from FILE", []string{
			"Install a new agent with the configuration in FILE. It starts supervised, with no permissions.",
		}},
		{"create NAME --template TEMPLATE [--from FILE | BACKEND]", []string{
			"Install a new agent from a template: the template's configuration (or FILE's), permissions and mode. BACKEND is where the agent runs, as the template allows:",
			backendText,
			"With openshell, gateway.url and gateway.oauth come from mcp-gateway's HTTP settings: the installed gateway's, or those in --gateway-config FILE.",
		}},
		{"edit NAME --from FILE", []string{
			"Replace the agent's configuration with FILE's and apply it: the agent restarts; an OpenShell agent gets a new sandbox.",
		}},
		{"delegation-setup [--check]", []string{
			"Bring delegation between agents to the bridge's own account, custos-delegate (0.9.17): replace an unchanged earlier copy of agents.yaml in /etc/mcp-gateway/servers.d with the shipped one, then take the agents' users out of agent-clients, which only the earlier bridge needed.",
			"A changed copy is kept, and the users with it. --check says what would change, and changes nothing.",
		}},
		{"delete NAME [--keep-state]", []string{
			"Remove an agent. Its user stays. --keep-state keeps its state directory, too.",
		}},
		{"pause NAME", []string{
			"Hold the agent: the gateway denies its calls, and the agent holds its activities.",
		}},
		{"resume NAME", []string{"Let a paused agent go on."}},
		{"chat NAME [--thread ID] [--message TEXT]", []string{
			"Talk with an agent on the terminal. Each line read is a message of one thread; the answer is printed as it comes: text, tool calls, approvals waited for.",
			"--thread continues a thread. --message sends one message and ends.",
		}},
	}},
	{"Templates and model defaults", []helpEntry{
		{"templates", []string{"The agent templates (CONTRACT.md §14a), as JSON."}},
		{"playbooks", []string{"The diagnostic playbooks (CONTRACT.md §14c), as JSON."}},
		{"defaults [--from FILE | --clear]", []string{
			"The model defaults for new agents (CONTRACT.md §14b), as JSON. --from sets them, --clear removes them.",
		}},
		{"compare [NAME…] [--template TEMPLATE]", []string{
			"Compare each agent with its template, as JSON: the differences in configuration and permissions. --template names the template of an agent created before its template was recorded.",
		}},
		{"apply-template NAME [--template TEMPLATE] [--configuration] [--permissions]", []string{
			"Give the agent its template's configuration, its permissions, or both (without either option).",
			"The agent keeps its name, model, schedules, events, sandbox, gateway connection, mode, categories and credentials.",
		}},
	}},
	{"What an agent may do (enforced by mcp-gateway)", []helpEntry{
		{"mode NAME advisory|supervised|bounded", []string{"Set the agent's autonomy mode."}},
		{"permissions NAME", []string{
			"Replace the agent's gateway permissions with the YAML list read from standard input.",
		}},
		{"owners NAME [RULE…]", []string{
			"Set who may approve the agent's calls besides the servers' approvers. A RULE is one of:",
			"  user:NAME  group:NAME  role:NAME  remote-user:SUB  remote-group:NAME",
			"Without a RULE, there are none.",
		}},
	}},
	{"Credentials and the identity provider", []helpEntry{
		{"credentials [NAME…]", []string{
			"The agents' credentials, as JSON: their names, what uses them, which are missing. Never the values.",
		}},
		{"set-credential NAME CRED < secret", []string{"Encrypt and store a secret read from standard input."}},
		{"remove-credential NAME CRED [--force]", []string{
			"Remove a stored credential. --force removes one the agent's model uses.",
		}},
		{"idp-client NAME [--keycloak [--token-lifetime D]] [--gateway-config FILE]", []string{
			"Describe the OAuth client an OpenShell agent needs at the gateway's identity provider.",
			"--keycloak prints a script (kcadm.sh) that creates it there, its tokens valid for D (default 5m, at most 1h).",
		}},
		{"idp-client NAME --keycloak --apply --admin USER [--admin-realm R] [--keycloak-url URL] [--token-lifetime D] [--gateway-config FILE]", []string{
			"Create the client in Keycloak through its admin API (USER's password is asked for, or read from standard input), add its issuer to the ones trusted for agents, check a token and store the secret.",
			"Without --token-lifetime, a replaced client keeps its token lifetime.",
		}},
		{"check-token NAME [--gateway-config FILE] < secret", []string{
			"Mint a token with the agent's OAuth client and check its claims as the gateway will: iss, sub, aud, scope, lifetime.",
		}},
	}},
	{"Promotion and review (CONTRACT.md §6a)", []helpEntry{
		{"categories", []string{"The task categories, as JSON."}},
		{"readiness NAME [CATEGORY]", []string{"A category's promotion readiness, as JSON."}},
		{"promote NAME CATEGORY --envelope FILE [--despite-readiness --reason TEXT]", []string{
			"Let a category act without approval, within the envelope in FILE.",
		}},
		{"demote NAME CATEGORY [--reason TEXT]", []string{"Put a category back to supervised. Its envelope is kept."}},
		{"review NAME [CATEGORY]", []string{
			"The autonomous actions of promoted categories still to be reviewed, as JSON.",
		}},
		{"review NAME CATEGORY ACTIVITY CALL ok|incident [--reason TEXT]", []string{
			"An approver's verdict on one action. incident (it should not have run) demotes the category.",
		}},
		{"feedback NAME ACTIVITY [--hypothesis ID=confirmed|rejected|unsure…] [--plan useful|not_useful] [--executed] [--comment TEXT]", []string{
			"Feedback on an activity's diagnosis report (CONTRACT.md §4a).",
		}},
	}},
	{"The host: task categories, the seal, issuers (CONTRACT.md §6b)", []helpEntry{
		{"enable CATEGORY… | --all", []string{
			"Enable task categories on a diagnostics-only host. They start supervised.",
		}},
		{"disable CATEGORY… | --all", []string{
			"Disable task categories again; --all makes the host diagnostics-only. Refused while an agent has permissions in them, or has one promoted.",
		}},
		{"diagnostics-only", []string{"Make a fresh host diagnostics-only (the bundled install, custos-setup)."}},
		{"seal --agents [--accept NAME…]", []string{
			"Make the host's seal cover the agents' data and their token issuers, and sign them. A change made outside agent-admin then denies that agent everything.",
			"Once the seal covers them, a file that does not match it is signed only when --accept names it (_http: the issuers).",
		}},
		{"seal --rekey [--accept NAME…]", []string{
			"A new key for the seal, when the old one is lost: signs again what matches the seal, and what --accept names (_enablement: the categories' state).",
		}},
		{"issuers [--add ISSUER…] [--remove ISSUER…]", []string{
			"The token issuers trusted for agents over HTTPS (_http.yaml). Signed on a host whose seal covers them.",
		}},
	}},
	{"OpenShell (CONTRACT.md §16a)", []helpEntry{
		{"openshell-setup --user USER --driver container|systemd [--gateway NAME] [--ca FILE] [--check]", []string{
			"Set up OpenShell for OpenShell agents. It checks what is installed (it installs nothing), configures the driver, registers the gateway for root, and puts mcp-gateway's CA (FILE) where the driver needs it.",
			"--check says what would change, and changes nothing.",
		}},
	}},
	{"Emergency stop", []helpEntry{
		{"stop-all", []string{"Stop every agent and every schedule."}},
		{"release-all", []string{"Undo stop-all."}},
	}},
	{"Run by systemd units, not by hand", []helpEntry{
		{"trigger NAME_SCHEDULE", []string{"Start a schedule's activity (agent-schedule@.service)."}},
		{"event NAME_EVENT_UNIT", []string{"Start an event's activity for a unit that failed (agent-event@.service)."}},
		{"fence NAME", []string{
			"Let a systemd agent's unit reach its model endpoint, for this start (agent@.service); the unit denies every other address.",
		}},
		{"evaluate", []string{
			"The demotion evaluator: demote bounded categories whose override rate or rollback conditions say so (custos-demote.timer).",
		}},
	}},
})

const (
	helpWidth  = 78
	helpIndent = "      "
)

const usageLine = "usage: agent-admin COMMAND [ARGUMENT…]\n       agent-admin help [COMMAND]"

// usage is the whole help.
var usage = func() string {
	var b strings.Builder
	b.WriteString(usageLine + "\n\n")
	b.WriteString(wrap("Run as root. Where a command reads a FILE, - is standard input. agent-admin help COMMAND shows one command.", "") + "\n")
	for _, g := range help {
		b.WriteString("\n" + g.title + "\n")
		for i, e := range g.entries {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(entryText(e))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}()

// sortedHelp puts each group's commands in alphabetical order; the forms
// of one command stay in the order given.
func sortedHelp(groups []helpGroup) []helpGroup {
	for _, g := range groups {
		sort.SliceStable(g.entries, func(i, j int) bool { return helpName(g.entries[i]) < helpName(g.entries[j]) })
	}
	return groups
}

// entryText is a command's synopsis and what it does.
func entryText(e helpEntry) string {
	var b strings.Builder
	b.WriteString(synopsis(e, "  ") + "\n")
	for _, p := range e.text {
		if strings.HasPrefix(p, "  ") {
			for _, l := range strings.Split(p, "\n") {
				b.WriteString(helpIndent + l + "\n")
			}
			continue
		}
		b.WriteString(wrap(p, helpIndent) + "\n")
	}
	return b.String()
}

// entries are a command's help entries (some commands have two forms).
func entries(name string) []helpEntry {
	var es []helpEntry
	for _, g := range help {
		for _, e := range g.entries {
			if helpName(e) == name {
				es = append(es, e)
			}
		}
	}
	return es
}

func helpName(e helpEntry) string { return strings.Fields(e.synopsis)[0] }

// commandHelp is one command's help.
func commandHelp(name string) (string, error) {
	es := entries(name)
	if len(es) == 0 {
		return "", fmt.Errorf("unknown command %q; agent-admin help lists them", name)
	}
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = entryText(e)
	}
	return strings.TrimRight(strings.Join(parts, "\n"), "\n"), nil
}

// commandUsage is what a command line that was not understood shows: the
// command's synopses, and where its help is.
func commandUsage(name string) string {
	es := entries(name)
	if len(es) == 0 {
		return usageLine
	}
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = synopsis(e, "       agent-admin ")
	}
	return "usage:" + strings.TrimPrefix(strings.Join(lines, "\n"), "      ") + "\n(agent-admin help " + name + ": what it does)"
}

// synopsis is a command's synopsis after prefix, filled into lines of
// helpWidth: an option in brackets stays on one line, and a line after
// the first starts under the command's first argument.
func synopsis(e helpEntry, prefix string) string {
	var units []string
	depth, start := 0, 0
	s := e.synopsis
	for i, r := range s {
		switch {
		case r == '[':
			depth++
		case r == ']':
			depth--
		case r == ' ' && depth == 0:
			units = append(units, s[start:i])
			start = i + 1
		}
	}
	units = append(units, s[start:])
	indent := strings.Repeat(" ", len([]rune(prefix))+len([]rune(units[0]))+1)
	line := prefix + units[0]
	var lines []string
	for _, u := range units[1:] {
		if len([]rune(line))+1+len([]rune(u)) > helpWidth && line != indent {
			lines = append(lines, line)
			line = indent + u
			continue
		}
		line += " " + u
	}
	return strings.Join(append(lines, line), "\n")
}

// wrap fills text into lines of helpWidth, each starting with indent.
func wrap(text, indent string) string {
	var lines []string
	line := indent
	for _, w := range strings.Fields(text) {
		if line != indent && len([]rune(line))+1+len([]rune(w)) > helpWidth {
			lines = append(lines, line)
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += w
	}
	return strings.Join(append(lines, line), "\n")
}
