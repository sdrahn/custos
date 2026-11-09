---
title: "6. Permissions and autonomy modes"
description: What an agent may do at the gateway, how a permission is written (server, tool, args, read_only, autonomous, category), the three autonomy modes, task categories and promoting one to act without approval within its time windows, and checking what the gateway would decide.
tags: [permissions, autonomy, modes, supervised, bounded, advisory, policy, categories, promote, demote, envelope]
---

# 6. Permissions and autonomy modes

What an agent may do is decided by mcp-gateway from two things in the
agent's gateway data: its **permissions**, the tools it may call at all,
and its **autonomy mode**, whether those calls need a person. Both are
set with `agent-admin` (or the *Agents* tab in Cockpit) and enforced by
the gateway, whatever the agent does.

## The three autonomy modes {#modes}

| Mode | Read-only permissions | Other permissions | Permissions marked `autonomous` |
|---|---|---|---|
| `advisory` | run | denied | denied |
| `supervised` (the default) | run | wait for an approval | wait for an approval |
| `bounded` | run | wait for an approval | run without one |

- **advisory**: the agent can look and advise, never change.
- **supervised**: every change waits for a person. A new agent starts
  here.
- **bounded**: like supervised, but changes an administrator marked
  `autonomous`, usually narrowed by `args`, run without asking.

Set the mode:

```bash
agent-admin mode patch-pilot bounded
```

The gateway applies it within seconds; `agent-admin` returns once it is
in force. A tool list change reaches the agent at its next call.

## Writing permissions {#permissions}

The permissions are a YAML list, replaced as a whole:

```bash
agent-admin permissions patch-pilot < perms.yaml
```

```yaml
# perms.yaml
- {server: zypp, tool: check_updates, read_only: true}
- {server: zypp, tool: confirm_install}
- {server: systemd, tool: change_unit_state, autonomous: true, args: {name: '^nginx\.service$', action: '^restart$'}}
```

| Key | Meaning |
|---|---|
| `server` | the gateway server's name; a glob (`*`) matches several |
| `tool` | the tool's name on that server; a glob, such as `'find_*'` |
| `resource`, `prompt` | instead of `tool`: an MCP resource or prompt of the server |
| `args` | per argument, a regular expression its value must match; the call is refused otherwise |
| `read_only: true` | the tool only reads: it runs in every mode without an approval |
| `autonomous: true` | in `bounded` mode, this change runs without an approval |
| `approval_scopes` | the scopes an approver is offered, as in the gateway's permissions, such as `[once]` so that no standing grant can be given |
| `effect`, `obligations` | as in the gateway's permissions (mcp-gateway user guide, chapter 6), for example an obligation for full audit of the arguments |

Rules:

- **Read-only only where it says so.** Without `read_only: true`, a
  permission counts as a change. An MCP server's own `readOnlyHint` never
  makes a tool read-only.
- **Unknown keys are refused** (`permissions[2]: unknown key "allow_everything"`),
  and each entry needs `server`.
- **An approval of an agent always goes out of band** (the gateway's
  `oob` channel): to the Approvals tab, the gateway's notifications and
  its command line. The agent is never asked.
- **Unlisted tools are denied.** The gateway also hides them from the
  agent.

## Choosing permissions for a task {#examples}

| The agent should | Permissions |
|---|---|
| report on updates | `{server: zypp, tool: check_updates, read_only: true}` |
| install updates, each with approval | the above, and `{server: zypp, tool: confirm_install}` |
| restart one service on its own (bounded mode) | `{server: systemd, tool: change_unit_state, autonomous: true, args: {name: '^nginx\.service$', action: '^restart$'}}` |
| hand work to another agent | `{server: agents, tool: delegate, read_only: true, args: {agent: '^patch-pilot$'}}` ([chapter 3](03-gateway.md#delegation)) |

Narrow `args` with anchored expressions (`^…$`): `nginx\.service` alone
would also match `my-nginx.service.bak`.

## Pausing an agent at the gateway {#pause}

`paused: true` in the agent's data makes the gateway deny the agent
every call, in every mode. Set it with `agent-admin pause NAME`
([chapter 9](09-administration.md#pause)). The emergency stop does the
same for all agents ([chapter 9](09-administration.md#emergency-stop)).

## Seeing what an agent may do {#posture}

```bash
agent-admin show patch-pilot | jq '.agents["patch-pilot"]'
```

prints the mode, whether it is paused (and by whom) and the permissions.
`agent-admin show` needs no root: the files are readable by everyone.

In Cockpit:

- *Agents*, the agent's **Tool boundaries**: each permission, what it is
  declared as, its argument rules, and its **effect now** in the current
  mode (allowed, needs approval, allowed without approval, denied).
- *Policy*, **Posture**: per agent, how many permissions run, wait or run
  autonomously, and who may approve them.
- *Policy*, **What would the gateway decide?**: pick an agent, type
  `server__tool` and, if you like, the call's arguments as JSON. With
  Cockpit's administrative access, the gateway's policy decides it: the
  answer is its decision with its reason (such as `task category
  patch-errata is not enabled on this host`, [chapter
  12](12-troubleshooting.md#tool-denied)), the arguments' limits
  included. Without it, the page works the decision out from the agent's
  permissions and says which permission makes it, without the
  arguments' limits.

## Task categories {#categories}

A task category names a kind of work, such as `patch-errata`. An
agent's autonomy is promoted one category at a time, never for the whole
agent: promoting `patch-errata` changes nothing for `config-lifecycle`.
The catalog ships these categories:

| Category | For |
|---|---|
| `diagnostics` | reading host state, logs and metrics, and reporting: read-only permissions only, never promoted ([below](#diagnostics-only)) |
| `patch-errata` | installing patches and errata, refreshing repositories |
| `security-response` | responding to a security finding: isolating a service, rotating a credential, blocking an address |
| `config-lifecycle` | changing configuration files; starting, stopping or restarting services |
| `compliance-audit` | checking the system against a compliance profile and applying its remediations |

`agent-admin categories` lists the catalog. To add a category, put a
file in `/etc/custos/categories.d/`, for example `backups.yaml` with
`backups: {title: Backups}`. A file with a shipped name
(`patch-errata.yaml`) replaces that one, and an empty one disables it.

A permission joins a category with `category:`:

```yaml
- {server: zypp, tool: check_updates, read_only: true}
- {server: zypp, tool: patch, category: patch-errata}
- {server: systemd, tool: change_unit_state, category: config-lifecycle, args: {name: '^nginx\.service$', action: '^restart$'}}
```

A permission in a category is supervised until its category is
promoted, whatever the agent's mode. `advisory` still caps everything.
A permission without a category keeps the agent's mode and its
`autonomous` flag, and `autonomous` cannot be combined with `category`.

## Diagnostics-only hosts and enabling categories {#diagnostics-only}

A host installed with the bundled health-check and diagnostics agent is
**diagnostics-only**: only the `diagnostics` category can be used. The
other categories are installed but disabled, and so are permissions
without a category. The gateway enforces it: a call of a disabled
category is denied in every mode, and the agent is not even shown its
tools. `agent-admin` refuses to give an agent such a permission, to
promote a disabled category, or to create an agent from a template that
needs one; each refusal is in the journal (`category.refused`, with who
tried).

```bash
agent-admin show | jq .enablement     # the state: diagnostics-only, or the enabled categories
agent-admin enable patch-errata       # enable one category (or several)
agent-admin enable --all              # every category: the full capability
agent-admin disable patch-errata      # disable one again (or several)
agent-admin disable --all             # back to diagnostics-only
```

`enable` is the action out of diagnostics-only; in Cockpit it is
**Enable…** on the Agents tab ([chapter 8](08-cockpit.md#agents)). It needs root (sudo
or Cockpit's administrative access) and is recorded with who ran it
(`categories.enabled`). An enabled category starts supervised, as on a
host installed with the full capability: promote it later on the path
below. What the diagnostics agent did until then is the first evidence
for that.

`disable` is the way back; in Cockpit it is **Disable…**. It turns
categories off again, and `--all` returns the host to diagnostics-only
(then permissions without a category are disabled too). On a host
installed with the full capability it works as well: the host then has
an enablement state, with every category but those you disabled. It is
recorded (`categories.disabled`) like `enable`. It refuses while an
agent still uses a category, and says which:

```
agent-admin: disable: still in use: agent patch-pilot has permissions in patch-errata (agent-admin permissions patch-pilot)
```

Take the category's permissions from the agent and demote it where it
is promoted, then disable. Each step is recorded on its own. A disabled
category keeps its readiness history; enabled again, it starts
supervised.

The state is `/etc/mcp-gateway/policy/agents/_enablement.yaml`. Change it
only with `agent-admin enable` and `disable`: `agent-admin show` compares it with its
record in the journal and reports a state changed by hand
(`"tampered": "the state differs from its record n-…"`). A host without
the file (every host installed with the full capability) has every
category enabled.

The state is also **sealed**: `agent-admin` signs it, with a key it
keeps encrypted as a systemd credential (with the TPM, where the host
has one), and the gateway checks the signature with the public key in
`_seal.yaml`. A state edited by hand, or deleted, does not take effect:
the gateway treats the host as diagnostics-only until `agent-admin
enable` or `disable` writes a signed state again, and its denials say why ("the task
categories' state does not match its seal"). `agent-admin show` reports
the same (`"sealed": true`, and `tampered` when the seal is broken). The
first change made with this version seals the host; a host set up
before it keeps an unsealed state until then. Root can still decrypt
the key and sign a state by hand: the seal makes a change past
`agent-admin` deliberate, not impossible.

The seal also covers **each agent's own gateway data** (its mode, pause,
permissions, promoted categories and owners) and the token issuers
trusted for agents (`_http.yaml`): `agent-admin` signs them as it
writes them. An agent whose data was changed by hand is denied every
call, as if paused, until `agent-admin` writes its data again; its
denials say "the agent's data does not match its seal", and its owners
approve nothing. Issuers changed by hand are not trusted until
`agent-admin issuers` writes them again. The emergency stop is never
sealed: written by hand, it still stops every agent. `agent-admin show`
reports a broken seal per agent (`tampered`), for the issuers
(`issuers_tampered`), and an earlier signed copy of an agent's data put
back (it still counts; the journal knows a later one); Cockpit shows
the agent as "seal broken".

A new seal covers the agents' data from the start. On a host sealed
before Custos 0.6, run once, after checking the agents' data with
`agent-admin show`, since it signs the files as they are:

```bash
agent-admin seal --agents
```

`agent-admin diagnostics-only` makes a freshly installed host
diagnostics-only; the installer's setup runs it. It refuses a host that
has an enablement state already, or an agent with permissions outside
`diagnostics`.

## Is a category ready to promote? {#readiness}

While a category is supervised, every proposal of it is an approval
item, and the journal keeps how approvers decided. `agent-admin
readiness` measures that per category, over the last 30 days:

```bash
agent-admin readiness patch-pilot patch-errata | jq '.categories["patch-errata"]'
```

| Field | Meaning |
|---|---|
| `proposals` | the category's approval items that an approver decided (expired ones do not count) |
| `approved`, `declined`, `cancelled` | approved; declined; approved but then cancelled before the call ran |
| `reviewed`, `review_overrides` | autonomous actions [reviewed](#review) since a promotion; of those, judged `incident` |
| `overrides`, `override_rate` | `declined` + `cancelled` + `review_overrides`, and their share of `proposals` + `reviewed` |
| `incidents`, `incident_rate` | approved calls that failed (as the gateway saw them come back, or as the agent recorded them), hit a limit, or had the agent paused or every agent stopped (`stop-all`) during their activity; their share of the approved calls that ran |
| `tools`, `distinct_calls` | how varied the proposals were |
| `observed_days`, `first_proposal` | since the first proposal, and when it was |
| `ready`, `unmet` | whether every threshold is met, and which are not |

The thresholds are 14 days, 50 proposals, at most 5 % overrides and at
most 2 % incidents. Set your own in `/etc/custos/promotion.yaml`, for
example:

```yaml
proposals: 30
override_rate: 0.03
```

The keys are `observation_days`, `proposals`, `override_rate`,
`incident_rate` (fractions: `0.05` is 5 %) and `window_days`. The
command reads the agent's and the gateway's journal, so run it as root
or as a member of `systemd-journal`. It counts a call in a category by
the agent's permissions at the time of the call: `agent-admin` records
each change of an agent's permissions (`permissions.changed`, [chapter
10](10-monitoring-and-audit.md#promotion-records)), so moving a tool into
a category does not bring its earlier calls along. Before the first
recorded change, the permissions it changed from count; an agent whose
permissions never changed in the window is measured by its current
ones.

### The diagnostics history {#diagnostics-evidence}

`agent-admin readiness` (and the Task categories card in Cockpit) also
shows what the agent's diagnosis reports showed over the same window:
how many reports, how many of their quotes Custos verified, how many you
gave feedback on, how many hypotheses you confirmed, rejected or were
unsure of, and what you said of the plans
([chapter 9](09-administration.md#reports)). Only feedback given as
root (`agent-admin feedback`, or Cockpit with administrative access)
counts.

On a host that started diagnostics-only, this history is part of every
category's readiness (but `diagnostics`' own): a category you enabled is
ready only when the agent also has at least 10 reports with your
feedback in the window, and you confirmed at least 70 % of the hypotheses
you confirmed or rejected. Set them in `/etc/custos/promotion.yaml`
(`diagnostics_feedback`, `diagnostics_confirmed_rate`; 0 turns either
off). Only the agent's own history counts: an agent you create later
earns its own by running playbooks, or you promote it despite its
readiness, with a reason. On a host installed with the full capability
the history is shown, not counted.

## Promoting a category {#promote}

A promoted category acts without approval, but only within its
**envelope**: the time windows in which that is allowed. Outside them
its calls need an approval again. Write the envelope to a file:

```yaml
# window.yaml: Saturdays 02:00 to 04:00, Berlin time
windows:
  - {days: [sat], from: '02:00', to: '04:00', tz: Europe/Berlin}
```

Each window has `days` (`mon` to `sun`; every day if left out), `from`
and `to` (`HH:MM`; `from` before `to`, so a window across midnight is
two windows) and `tz` (an IANA zone; `UTC` if left out).

An envelope may also cap how often the category acts without approval:

```yaml
rate: {calls: 20, per: 1h}       # per tool; per is 1s, 1m or 1h
```

mcp-gateway enforces it as its `approval_over_rate` obligation (`20/h`)
on each of the category's permissions while they act without approval:
at most `calls` calls of each tool, by this agent, within any `per` (a
sliding window). A call over it **waits for an approval**, as if the
category were supervised: for that call only (scope `once`), and the
approval names the rate that was reached ([chapter 7](07-approvals.md)).
Approved, it runs and is not counted; declined, it is denied. The
gateway counts per tool, not across the category's tools, and in its
memory, so a restart of the gateway starts afresh. Calls that need an
approval anyway, outside the windows or with the category supervised,
are not counted. Reaching the rate three times within a day
[demotes](#auto-demote) the category.

Then promote:

```bash
agent-admin promote patch-pilot patch-errata --envelope window.yaml
```

A category that is not [ready](#readiness) is refused, and the message
says what is not met. To promote it anyway, give a reason; it is
recorded with the promotion:

```bash
agent-admin promote patch-pilot patch-errata --envelope window.yaml \
    --despite-readiness --reason "patched by hand for a year, the journal only keeps a week"
```

`promote` also refuses:

- a category that is not in the catalog, or in which the agent has no
  permission;
- an agent in `advisory` mode;
- `hosts` or `blast_radius` in the envelope (they need a fleet). It
  refuses them rather than ignore them;
- a `rate` whose `per` is not `1s`, `1m` or `1h`, or whose `calls` is
  not at least 1;
- an envelope without `windows`, or with a key it does not know;
- `--despite-readiness` without `--reason`;
- the same envelope the category already has while it is bounded.

It returns once the gateway has the change, and writes a
`category.promoted` record with the readiness measures at that moment ([chapter 10](10-monitoring-and-audit.md#promotion-records)).
Promoting a promoted category again with another envelope replaces it.
`agent-admin show NAME` lists the agent's categories with their mode,
envelope and who promoted them.

## Demoting a category {#demote}

```bash
agent-admin demote patch-pilot patch-errata --reason "a patch broke nginx"
```

The category is supervised again, so its calls need an approval. Its
envelope stays for a later promotion. The record is `category.demoted`.
Pause and the emergency stop still stop everything, promoted or not.

## Reviewing what a promoted category did {#review}

Inside its windows a promoted category acts without asking, so nobody
decided its actions beforehand. Review them afterwards:

```bash
agent-admin review patch-pilot patch-errata | jq '.categories["patch-errata"].pending'
agent-admin review patch-pilot patch-errata e-3f2a… c1 ok
agent-admin review patch-pilot patch-errata e-41b7… c1 incident --reason "patched the database host during business hours"
```

The list holds the calls of the category that ran without an approval
since its promotion, in the last week, as the gateway recorded them
(each with its `decision_id`), so an agent cannot leave one out: all of
them while there are at most 20 in the week, otherwise the newest
unreviewed ones up to 20 reviews. A call reviewed on the gateway (below)
is reviewed here too. One the gateway lists without an activity (the
agent named none) is reviewed on the gateway only. `ok` says it was fine. `incident` says it should not have run:
it needs a reason, counts as an override in the
[readiness](#readiness) (`reviewed`, `review_overrides`), and demotes
the category at once. Each verdict is a `category.review` record with
who reviewed it. Only verdicts `agent-admin` recorded as root count:
Custos checks journald's `_UID` of each record (and that it is not an
OpenShell relay's), which an agent cannot fake, so an agent cannot
review its own actions. In Cockpit, the agent's *Task categories* card lists
them under *To review*, with **Fine** and **Should not have run**.
Listing the actions to review needs root or the group
`systemd-journal`; a verdict needs root (or Cockpit's administrative
access).

**On the gateway.** The people who approve the agent's calls review them
too, without root: mcp-gateway makes each call a promoted category makes
without an approval a *review item* once it ran, for whoever may decide
the agent's calls on its server (the servers' approvers and the agent's
[owners](07-approvals.md)). They review on the gateway's approval page,
with `mcp-gateway-admin reviews`, or in Cockpit's *Approvals* tab under
*To review*, as themselves, with **Fine** or **Should not have run**
and a reason. Their verdicts are the gateway's audit records
(`mcp-review`) and count as root's do: in the readiness, and an
incident demotes the category at the evaluator's next run (within five
minutes). An item nobody reviewed expires after a week.

## Automatic demotion {#auto-demote}

The demotion evaluator, `custos-demote.timer`, checks every bounded
category every 5 minutes, as root and outside any agent. `promote`
enables the timer. It demotes a category when:

- its override rate since the promotion (at most the last 7 days) is
  above the threshold. Outside its windows the category still asks for
  approvals, and a decline counts;
- a **rollback** condition of its envelope fires;
- calls of the category reached its envelope's [rate](#promote) three
  times within a day (since the promotion; `over_rate` in the gateway's
  audit);
- a [review](#review) found an action that should not have run.

```yaml
windows:
  - {days: [sat], from: '02:00', to: '04:00', tz: Europe/Berlin}
rollback:
  on: [tool_error, limit_exceeded, health_check_failed]
  health_check: 'systemctl is-active --quiet nginx.service'
```

| Condition | Fires when |
|---|---|
| `tool_error` | a call of the category that ran without an approval since the promotion failed: as the gateway saw it come back (its audit's `mcp-result`: a tool error, an error or a timeout), or as the agent recorded it |
| `limit_exceeded` | an activity with such a call reached one of the agent's limits |
| `health_check_failed` | `health_check` (run as root with `sh -c`, at most 60 seconds) exits non-zero |

`health_check` goes with `health_check_failed`, and only with it. The
demotion is recorded as `category.demoted` by `custos-demote`, with the
trigger as the reason. Run the evaluator by hand with `agent-admin
evaluate`, which prints each demotion. A demotion stops what follows; it
does not undo what ran (for that, a snapshot, say, is the tool's).
Cockpit shows the automatic demotions of the last week on the Approvals
tab, and the category in the agent's view says it was demoted
automatically ([chapter 8](08-cockpit.md#approvals)).

The evaluator also tells the people who approve the agent's calls,
through mcp-gateway: it posts a notice about `agent-NAME` for the
servers of the category's permissions. The gateway sends it to whoever
may decide the agent's calls on those servers, owners included
([chapter 7](07-approvals.md)): by mail to its notification recipients,
as a desktop notification (`mcp-gateway-notify`), and on its approval
page, until they dismiss it or a week has passed. `agent-admin
evaluate` prints `approvers notified (notice …)` for each demotion; if
the gateway refuses the notice, it says why, and the demotion stands all
the same.

## How the gateway applies modes {#how-enforced}

The module `agents.rego` (`/usr/share/mcp-gateway/policy/custos/agents.rego`)
turns each permission of an agent principal into a gateway permission.
Read-only ones are kept as they are. Autonomous ones are kept as they are
in `bounded` mode, and so are the permissions of a promoted category
within one of its windows. Every other one gets `require_approval: true` with the
channel `oob` in `supervised` and `bounded` mode, and is left out in
`advisory` mode. It denies an agent everything when:

- the agent has no data, or its mode is none of the three, or a
  category's mode is neither `supervised` nor `bounded`;
- it is paused, or the emergency stop is on;
- a gateway role is bound to its user;
- it comes over HTTPS with a token from an issuer not listed for agents
  ([chapter 11](11-openshell.md#issuers)).

The gateway's audit records each decision with the principal
`agent-<name>`.
