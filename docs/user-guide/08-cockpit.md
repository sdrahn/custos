---
title: "8. Using the Custos page in Cockpit"
description: "Who logs in, the roles (supervisor, approver, administrator), what the person's and the root session are used for, and each tab of the page: Activity, Approvals, Agents, Policy, Audit, chat and the emergency stop."
tags: [cockpit, web, ui, roles, activity, approvals, agents, policy, audit, chat]
---

# 8. Using the Custos page in Cockpit

The Custos page (package `custos-cockpit`) is where people watch the
agents, approve their calls and intervene. Open Cockpit at
`https://<host>:9090`, log in, and choose *Custos* in the menu.

## Who logs in {#who-logs-in}

Everyone logs in with **their own account**: a local user, or a
directory account (SSSD, IPA, Active Directory) that resolves to one.
Everything the page does goes through Cockpit's session for that person,
so the agents' sockets and mcp-gateway see who it is.

Not for logging in:

- `mcp-gateway`: the gateway's system account.
- `agent-<name>`: the agents' accounts, without passwords.
- `root`: it works for administration, but an approval made as root is
  recorded as root's, and who decided is lost.

## Roles {#roles}

| Role | The account needs | Can |
|---|---|---|
| supervisor | membership in `agent-clients` | see the agents, their activities and journal records; chat with an agent |
| approver | the above, and a place in the gateway's approver rules ([chapter 3](03-gateway.md#approvers)) | decide pending calls (grants are revoked on mcp-gateway's own page, [chapter 7](07-approvals.md)) |
| administrator | the above, and sudo rights (by default `wheel`) for Cockpit's *Administrative access* | create, change and delete agents; modes, permissions and task categories; pause, stop, schedules; model defaults and credentials (listing, storing, removing); the emergency stop |

An approver need not be an administrator. Reading the journal (the
*Audit* tab, *Decided today*) needs journal access, as for Cockpit's
*Logs* page (`systemd-journal` or `wheel`).

## Two sessions in one page {#two-sessions}

With *Administrative access* off, the page uses one session: the
person's. With it on, Cockpit adds a root session, and the page uses
each for one kind of action:

| Action | Session | Why |
|---|---|---|
| approve, decline | **the person's**, always | mcp-gateway records the approver it sees |
| status, activities, the journal, chat; **Check**, the templates, a new agent's starting configuration, reading the model defaults | the person's | the sockets, the journal and these files answer as the person may see |
| pause, resume, restart, stop, start, mode, permissions, schedules, create, edit (also reading `agent.yaml`, which is not world-readable), delete; promote, demote, readiness, review; model defaults; listing, storing and removing credentials; the emergency stop | **root** | only root changes agents and units; `agent-admin` records the person |

So turning on administrative access never changes for whom an approval
is recorded. Without it, an administrative button says it needs
administrative access.

## Refreshing and errors {#refresh}

The page refreshes the tab you look at: *Activity* every 3 seconds,
*Approvals* every 2, *Agents* every 5, *Policy* every 10; *Audit* reads
the journal when you ask. An agent's details do not refresh while you
edit something in them.

An action that fails (a refused promotion, a configuration that does not
check) shows its reason in the red box at the top until you click
**Dismiss** or start another action; a refresh does not clear it. A
refresh that fails (the gateway not running, say) shows there too, and
goes once a refresh works again. Switching tabs clears both.

## Activity tab {#activity}

The cards count what runs now (and how much of it was not started in a
chat), what waits for an approval (and since when), the next scheduled
run and what ended in the last 24 hours.

*Activities* lists running activities first, then ended ones, newest
first. A delegated activity sits under the one that delegated it. Each
row shows the agent, the state, the origin (`schedule · morning-check`,
`chat · alice`, `api · root`, `delegated by triage`), the agent's last
text, and the number of tool calls. The filters show all, only running
and waiting, only those not from a chat, or only chats.

Selecting an activity shows who started it, as a chain back to its
origin, the start of the agent's response, and its tool calls with
their states and the agent's stated rationale. **Show the full
response** opens all of it in a dialog, with its Markdown rendered:
tables, headings, lists and code. It is the model's own text; the page
never runs it as HTML and shows a link's address instead of following
it. **Copy as text** copies it as the model wrote it. A call waiting for approval links to the Approvals tab.
**Cancel activity** ends a running activity. The page cancels as the
person first, which works for their own activities, and else through
administrative access.

An activity that ended with a **diagnosis report**
([chapter 9](09-administration.md#reports)) shows it above its tool
calls: the summary and the playbook; each hypothesis with the agent's
confidence (its own estimate) next to how many of its quotes Custos
verified; each quote marked **✓ verified** (found in what that tool
returned in this activity) or **unverified** (the agent's claim alone);
the findings; and the remediation plan, each command with **Copy**.
**Was it right?** records your verdict per hypothesis, on the plan,
whether you carried it out, and a comment (`agent-admin feedback`, with
administrative access); it is the evidence for letting the agent do more
later.

## Approvals tab {#approvals}

The pending calls, each with the gateway's half and the agent's half,
and the buttons for the scopes the gateway offers
([chapter 7](07-approvals.md#cockpit)). *Decided today* lists the day's
decisions from the journal. *Demoted automatically* appears when the
demotion evaluator set a task category back to supervised in the last
7 days: the agent, the category, when and why; its changes wait for your
approval again ([chapter 6](06-permissions-and-modes.md#auto-demote)).
Both need access to the journal (`systemd-journal` or `wheel`).

## Agents tab {#agents}

**Task categories on this host** comes first: which categories are
enabled and which are disabled (the gateway denies their calls), who
changed it last, and a warning when the state was changed outside
agent-admin. On a host installed with the full capability every
category is enabled. **Enable…** enables the ones you pick, after a
confirmation (`agent-admin enable`, with administrative access); they
start supervised. **Disable…** disables the ones you pick, or returns
the host to diagnostics-only (`agent-admin disable`); while an agent
still uses a category it is refused, and the form shows what is in the
way ([chapter 6](06-permissions-and-modes.md#diagnostics-only)). In an
agent's *Task categories*, a disabled category is marked *disabled on
this host* and has no **Promote…**.

*Agents on this host* lists every agent: those with a socket in
`/run/agents`, and those that have gateway data but are stopped. Each
shows its state, autonomy mode, host and model, schedules and current
work. An agent whose socket does not answer within 8 seconds shows as
*unreachable*, with "not answering (no answer within 8 s): its service
may be failing to start; see its Logs"; the other agents are listed as
usual ([chapter 12](12-troubleshooting.md#agent-start)).

**Create agent** opens a form for a new agent: a name (2 to 32
lower-case letters, digits and dashes, starting with a letter), and its
configuration, blank or **Start from** one of the shipped templates,
which also gives the agent the template's permissions and mode
([chapter 9](09-administration.md#templates)). The editor starts with
the model defaults. **Check** validates it, **Create** creates it. When
its model names a credential, the page then asks for the secret and
stores it (**Store**, `agent-admin set-credential`).

**Model defaults** sets the model new agents start with: provider,
model, endpoint (for `openai`), credential (a name) and temperature.
**Save** writes them, **Remove the defaults** removes them; existing
agents keep their model ([chapter 5](05-configuration.md#model-defaults)).

Selecting an agent shows:

- its state and mode, its units, with **Pause**/**Resume**,
  **Restart**, **Stop**/**Start**, **Logs** and **Delete…**. **Restart**
  restarts the agent's service, which ends its running activities.
  **Logs** opens Cockpit's *Logs* page for the agent's unit: the unit's
  messages and the agent's own records ([chapter 10](10-monitoring-and-audit.md#journal));
- **Autonomy mode**: advisory, supervised or bounded, one click each;
  and **Owners**, who may approve its calls besides the servers'
  approvers (**Save owners**, `agent-admin owners`);
- **Tool boundaries**: its permissions and their effect now, and **Edit
  the permissions** as a YAML list;
- **Task categories**: each category its permissions name, with its
  mode and windows, its readiness (ready or not, and what is not met;
  the override and incident rates against their limits; the proposals),
  **Demote** for a bounded one, and **Promote…** (for a bounded one
  **Change envelope…**), which opens a form for the envelope: windows
  (the days as checkboxes, with *every day*, *weekdays* and *weekend*;
  from and to; the time zone, the host's at first), an optional rate
  (calls per tool per second, minute or hour) and the conditions that
  demote it at once (a tool call fails, the rate is exceeded, a health
  check fails, with its command). It says the envelope in words, checks
  it before anything is sent, and shows the category's readiness.
  **Edit as YAML** is the same envelope as text, for what the form does
  not show. A category that is not ready is refused unless you promote
  it despite its readiness, with a reason. Once a category has been promoted, *To
  review* lists the actions it took without an approval, with **Fine**
  and **Should not have run**. **Demote** and **Should not have run**
  take the reason from the *Reason (recorded)* field (Demote's default:
  "demoted in Cockpit"). Readiness and the actions need administrative
  access
  ([chapter 6](06-permissions-and-modes.md#readiness));
- **Schedules**: each with its calendar, next run and **Enable**/**Disable**;
- **Events**: each with the units whose failure starts it, how often at
  most per unit, and its prompt or playbook
  ([chapter 5](05-configuration.md#events)); change them in the
  configuration;
- **Diagnostic playbooks**: pick one of the playbooks this agent can
  run, add what to look at if you like, and **Run**: the agent runs it
  as an activity started by you, and the page opens it, where its report
  appears. Only the playbooks whose tools the gateway offers the agent
  are listed; the host's others are listed apart with the tools they
  need, so an agent without diagnostic permissions offers none
  ([chapter 9](09-administration.md#playbooks));
- **Credentials** (with administrative access): the agent's credentials
  by name, what uses them, and when each was stored; never a value. A
  credential its model names but that is not stored is *missing: the
  agent does not start*, and the agents list marks the agent *credential
  missing*. **Set**/**Replace** fills the form below: type the secret
  and **Store** (`agent-admin set-credential`, the secret on stdin; the
  page keeps no copy). The form offers to store the same key for the
  other agents whose model uses the same name, each its own encrypted
  copy, and restarts the agents that run so that they use it.
  **Remove…** removes one after a confirmation (`agent-admin
  remove-credential`; the model's needs `--force`, and the confirmation
  says the agent does not start without it). An OpenShell agent's gateway
  token is *held by OpenShell*: it can be replaced, not removed
  ([chapter 9](09-administration.md#credentials));
- **Configuration**: **Edit configuration** loads `agent.yaml`, **Check**
  validates it, **Save and apply** runs `agent-admin edit`;
- **Chat**: your own thread with the agent over AG-UI.

## Chat {#chat}

The chat in an agent's view is the person's own thread: the agent knows
the person by their account, and each message is an activity with the
origin `chat · <person>`. The agent's answer streams in, with its tool
calls. A call waiting for approval shows as a notice with **Review in
Approvals**. Approvals still go through the gateway: chat gives no
extra rights. The thread lasts as long as the page is open; the agent
keeps the conversation's history. An agent shows chat only with
`interaction.agui: true` ([chapter 5](05-configuration.md#interaction)).

## Policy tab {#policy}

**Posture** lists every agent's mode and how many of its permissions
run, need an approval, or run without one, with the approvers of its
servers (the rule `self` left out, as it never applies to agents).
**What would the gateway decide?** answers for one agent and one
`server__tool`, with optional arguments: the gateway's policy decides it
and gives its reason, read through the root bridge from the gateway's
OPA (`/run/mcp-gateway/opa.sock`); without administrative access the
page works it out from the agent's permissions
([chapter 6](06-permissions-and-modes.md#posture)).

## Audit tab {#audit}

**State-changing actions** joins the agents' records and the gateway's
by approval id: each action proposed (with the agent's rationale),
decided (approved or declined, by whom, scope, via), and executed or
not. **Records** lists the agents' journal records and the gateway's
audit records about agents, filtered by agent, kind and text. The tab
reads the journal when opened or refreshed, as far as the person may
read it. Without access to the gateway's records it says so, and the
approvers are missing.

## The emergency stop {#emergency-stop}

**Stop all agents** in the page's header asks first, then stops every
agent: the gateway denies every agent call at once, and systemd stops
every agent and schedule. A banner says who stopped them and when.
**Release…** says what it does, then lets the gateway allow agent calls
again and starts the sockets and schedules again; paused agents stay
paused. Both need administrative access
([chapter 9](09-administration.md#emergency-stop)).
