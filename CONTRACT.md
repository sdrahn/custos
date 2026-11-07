# Custos — Contract

Status: draft 1 (contract version `1`); §6a is a draft 2 design, partly
implemented (O11); §4a, §6b, §14c and §17a are a design for the bundled
diagnostics install, implemented (O16). The project was named
cockpit-agentic until before its first release; the names and paths
below are Custos's (`custos`, `/etc/custos`, …) and are the ones contract
version 1 keeps.

This contract is the interface between the people who supervise agents
and the agents themselves. It defines what a Cockpit control plane
consumes, so that the control plane does not depend on any particular
agent host (ADK, kit or another).

| Part | Contents | Who must follow it |
|---|---|---|
| **I** | the control-plane contract: identity, activities, the control API, autonomy modes, approvals, intervention, delegation, audit, interaction | every agent host; the Cockpit control plane consumes only this |
| **II** | the reference implementation: an ADK Go launcher run by systemd, with its configuration, sandbox, SELinux and packaging | this repository |
| **III** | requirement traceability and open items | — |

Within version 1, a name, path or field listed here is added to, never
renamed or removed (the same rule as mcp-gateway's D10). A host reports
the version it implements in `GET /v1/status` (`contract`).

---

# Part I — The control-plane contract

## 1. Parts and who enforces what

| Part | Role | Enforces |
|---|---|---|
| **mcp-gateway** (≥ 1.15) | the agents' only tools; OPA policy (autonomy modes, §6); approvals and grants (§7) | *may this agent call this tool with these arguments now* |
| **systemd** | runs, schedules and stops agents; sandboxes them with the `systemd` backend | what an agent process can touch; that a stopped agent stays stopped |
| **SELinux** | confines agents (`systemd` backend: `agent_t`; `openshell` backend: the container runtime's domain) | that an agent reaches the gateway's MCP socket, never its control socket nor another agent's socket |
| **sandbox backend** (§3a) | `systemd` (the default) or `openshell` (NVIDIA OpenShell) | files, processes and network egress of the agent process |
| **journald** | holds the audit record (§10) next to the gateway's | — (it is the record) |
| **agent host** | runs agents; serves the control API (§5); writes audit records | local limits only; not a security boundary |
| **Cockpit control plane** | shows activity, approvals, policy and history; intervenes | — (it shows things; decisions go to the gateway and systemd) |

**Rule:** anything that must hold even if an agent or its host is
compromised is enforced by the kernel, systemd or the gateway. That
covers supervised-by-default, the tool boundaries, approvals and the
emergency stop. Code inside the agent's process enforces none of it.
What the host does itself (limits, pause, trace fields) helps an honest
agent and enriches the record.

## 2. Identity

| Thing | Value |
|---|---|
| agent name | `^[a-z][a-z0-9-]{0,30}[a-z0-9]$`, unique on the host |
| agent user | `agent-<name>`: a static system user without a password, member of `mcp-users` and `agent-clients`. The prefix `agent-` is reserved for agents |
| gateway principal | `sub: agent-<name>`. With the `systemd` backend: `transport: unix`, identified by `SO_PEERCRED`. With the `openshell` backend: `transport: http`, identified by an OAuth bearer token whose subject is `agent-<name>`, from an issuer trusted for agents (§6); the token carries no local-user claim, so the subject stays the principal (§16a). Never by what the agent says |
| journal identifier | `SYSLOG_IDENTIFIER=agent-<name>`, `AGENT_NAME=<name>` |
| activity id | unique on the host; in the journal as `AGENT_INVOCATION` |

The **session user** is the person or source for whom an activity
runs: a local account for chat (taken from `SO_PEERCRED` on the
agent's socket), `system:schedule` or `system:event`, or `agent-<other>`
for delegation. The agent acts at the gateway as `agent-<name>`, never
as the session user.

**People and the control plane.** A control plane acts for the person
using it, under that person's own account (a local or directory user):
the agents' sockets (§3) and the gateway's control API identify them by
`SO_PEERCRED`. It never acts as the gateway's account or an agent's.
Where it can also act as root (Cockpit with administrative access), it
does so only for what needs root, and names the person:

| Done as | What |
|---|---|
| **the person**, always | approval decisions: approve, decline, revoke a grant (§7, §8). The gateway records the approver it sees, so a decision made as root would be root's. Also reading status, activities and the journal, and chat (§11) |
| root | creating and removing agents, modes and permissions (§6), pause and resume, stop, the emergency stop and its release (§8), through `agent-admin` (§17), which records the person (`by`) |

Having administrative access therefore never changes for whom an
approval is recorded. `docs/user-flow.md` walks through the roles and
flows.

## 3. Discovery

An agent is a directory `/run/agents/<name>/` holding `api.sock`, where
`GET /v1/status` answers with `"contract": 1`. That is all a control
plane needs to find agents. `/v1/status` says how the agent is started
and stopped (`lifecycle`, §3a) and names the host implementation
(`implementation`). The ADK launcher reports `custos/adk-go`.
A host that is not socket-activated still creates the socket while it
runs, and names its lifecycle: a `systemd` one without a socket names
only its service, which a control plane then stops and starts. (`units` is the `systemd` lifecycle's
units, kept for version 1 clients.)

`/run/agents/` is `root:root 0755`. Each socket is `root:agent-clients
0660`. Members of `agent-clients` (supervisors) and root (Cockpit with
administrative access) can reach the sockets. Agents are members too,
but only for delegation through the gateway's bridge (§9): their own
units cannot see `/run/agents`.

Removing an agent removes its directory. A directory without a socket
is a stopped agent only while the host still has the agent: systemd
removes a stopped socket, not its directory. The Cockpit page counts it
as an agent while the gateway has its data (§6).

## 3a. Sandbox backends and lifecycle

Where an agent process runs, and what starts, sandboxes and stops it, is
its **sandbox backend**. Everything else in Part I (identity at the
gateway, activities, the control API, modes, approvals, audit) is the
same for every backend.

| `lifecycle` in `/v1/status` | Starts / stops | Sandbox | Reaches the gateway |
|---|---|---|---|
| `{"kind": "systemd", "service": "agent@<name>.service", "socket": "agent@<name>.socket"}` | systemd | the unit's sandbox and SELinux `agent_t` (§15, §18) | `/run/mcp-gateway/mcp.sock`, `SO_PEERCRED` |
| `{"kind": "openshell", "sandbox": "agent-<name>", "gateway": "<openshell gateway name>", "service": "agent-openshell@<name>.service"}` | OpenShell (`openshell sandbox start/stop`), with a systemd unit on the host that owns the agent's socket | OpenShell: Landlock, seccomp, an unprivileged user, a network fence; egress only through OpenShell's supervisor and its network policy | mcp-gateway's Streamable HTTP listener, by the host's name (or `host.openshell.internal`, §16a), with a bearer token the supervisor adds (the agent never holds it) |

Rules for every backend:

- **The gateway stays the only source of tools.** Network policy of
  the backend must allow the gateway and the model endpoint, and nothing
  else, unless an administrator adds it. `systemd`: `agent@.service`
  denies every address but the host's own (`IPAddressDeny=any`,
  `IPAddressAllow=localhost`), and each start `agent-admin fence NAME`
  (as root, `ExecStartPre=+-`) adds the model endpoint's addresses as
  the host resolves them and the nameservers (`systemctl set-property
  --runtime`); a failed fence leaves the denial. `openshell`: the
  sandbox's network policy (§16a).
- **Approvals stay the gateway's.** A backend's own review of policy
  changes (OpenShell's policy prover) is not an approval of an action.
- **The control API stays a Unix socket on the host**
  (`/run/agents/<name>/api.sock`), owned by a host process that
  identifies callers by `SO_PEERCRED`. For a sandboxed agent, that
  process relays to the agent (Part II, §16a) and passes the caller on
  as `X-Custos-Caller: <uid>:<name>` with a secret the relay and the
  agent share; the agent believes the header only with the secret.
- **Stop works without the agent.** `lifecycle` names what the control
  plane stops (§8); the emergency stop also denies at the gateway.

## 4. Activities

An **activity** is one unit of agent work: a chat turn, a scheduled
run, an event's run, or a task delegated by another agent, with the tool
calls it makes. Activities are what the control plane shows, whoever
started them.

```json
{
  "id": "e-6448…",
  "agent": "patch-pilot",
  "session": "t1",
  "user": "system:schedule",
  "origin": {"kind": "schedule", "by": "agent-schedule@patch-pilot_compliance.timer", "schedule": "compliance"},
  "parent": {"agent": "triage", "activity": "e-12ab…"},
  "root":   {"agent": "triage", "activity": "e-0f00…"},
  "state": "waiting_approval",
  "started": "2026-11-03T06:00:04Z",
  "ended": null,
  "summary": "Installing 1 security patch; nginx restarts.",
  "reason": "",
  "llm_calls": 2,
  "calls": [
    {"call_id": "c1", "tool": "zypp__check_updates", "state": "ok", "args_sha256": "4413…",
     "since": "…", "ended": "…"},
    {"call_id": "c2", "tool": "zypp__confirm_install", "state": "waiting_approval", "args_sha256": "6eb8…",
     "approval_id": "a-42", "approval": "pending",
     "rationale": "Installing 1 security patch; nginx restarts."}
  ]
}
```

| Field | Values |
|---|---|
| `origin.kind` | `chat` (a person, through the REST API or AG-UI), `schedule`, `event`, `delegation`, `api` (a program or person through `POST /v1/activities`) |
| `origin.by` | the person, the timer unit, the event source, or the delegating agent's user. For an agent's event (§14): `unit-failure:<unit>` |
| `origin.event` | events only: the name of the agent's event (§14) that started it |
| `parent`, `root` | delegation (§9): the activity that delegated this one, and the first one of the chain. `root` is the activity itself when nothing delegated it |
| `state` | `running`, `waiting_approval` (a call waits for the gateway's approval), `paused`, `finished`, `failed`, `cancelled`, `limit` (a local limit ended it) |
| `calls` | the activity's tool calls: always an array, `[]` when it made none |
| `calls[].state` | `running`, `waiting_approval`, `ok`, `error` |
| `calls[].approval` | `pending`, `approved`, `declined` |
| `calls[].rationale` | the model's text that came with the call: its stated reason and expected impact. **Self-asserted by the agent**; the control plane labels it so |
| `calls[].suspected_injection` | signs of a prompt injection (O14) in the outputs of the activity's last 3 calls before this one, which the model read: `[{"call_id", "tool", "sign", "quote"?}]`. Signs: an instruction to ignore its instructions, a chat template's role marker, a claimed approval, a new role or policy, a request for its instructions, an instruction to use this call's tool, one of this call's argument values on a line that tells the model to use it (a value merely present, as a unit name in a listing, is the ordinary flow of a diagnosis and no sign). `quote` is the text found (at most 120 characters), never for an argument value. Found by the host in the agent's process: **self-asserted** and advisory; the approval and the gateway's offer are what hold |
| `calls[].playbook_step` | the number (from 1) of the playbook step (§14c) this call is: the host made it, not the model; absent for the model's calls |
| `calls[].preceding` | for a call that waits for an approval: those outputs, `[{"call_id", "tool", "excerpt"}]`, each up to 1200 characters around the first sign in it. Outputs of the agent's own tools, shown to whoever may read the activity |
| `summary` | the agent's last text, at most 500 bytes (cut at a character, marked `…`) |
| `response` | the agent's last text in full, at most 32 KiB (cut as `summary`), as the model wrote it: often Markdown. **Self-asserted by the agent**; a console renders it as text, never as HTML. Only in `GET /v1/activities/{id}` and an ended activity a `wait` returns, not in `GET /v1/activities` |
| `playbook` | the diagnostic playbook the activity runs (§14c), if any |

Arguments never appear in activities or the journal (only
`args_sha256`); tools' outputs only as `calls[].preceding`, never in the
journal. The gateway shows the arguments of a pending approval
to those who may decide it (§7), and logs them as its policy says.

The host keeps running activities and at least the last 200 ended ones.
The journal (§10) is the full history.

## 4a. Diagnosis reports (draft, O16)

*Draft, implemented (O16).* An activity may end with a **diagnosis
report**: what the agent found, why it thinks so, and what the
administrator could do about it. A host that writes reports adds the
field `report` to the activity (§4) and the record `diagnosis.report`
(§10):

```json
"report": {
  "kind": "diagnosis",
  "playbook": "disk-pressure",
  "summary": "/var is full: the journal and old snapshots use 18 GiB.",
  "findings": [
    {"id": "f1", "text": "/var is 98 % used.", "evidence": ["e1"]}
  ],
  "hypotheses": [
    {"id": "h1", "text": "The journal has no size limit and grew to 11 GiB.",
     "confidence": "high", "evidence": ["e1", "e2"]}
  ],
  "evidence": [
    {"id": "e1", "call_id": "c2", "tool": "exec__disk_usage", "quote": "/dev/vda3  20G  19.6G  0.4G  98% /var", "verified": true},
    {"id": "e2", "call_id": "c4", "tool": "systemd__journal_disk_usage", "quote": "Archived and active journals take up 11.0G", "verified": true}
  ],
  "plan": [
    {"step": 1, "text": "Limit the journal to 2 GiB.", "command": "printf '[Journal]\\nSystemMaxUse=2G\\n' > /etc/systemd/journald.conf.d/50-size.conf && systemctl restart systemd-journald",
     "risk": "Older journal entries are removed."}
  ]
}
```

| Field | Values |
|---|---|
| `kind` | `diagnosis` |
| `playbook` | the playbook the activity ran (§14c), if any |
| `findings[]`, `hypotheses[]` | what the agent observed, and its root-cause hypotheses, each citing evidence by id |
| `hypotheses[].confidence` | `low`, `medium`, `high`: **the model's own estimate, self-asserted**; the control plane labels it so and shows it next to how much of the cited evidence is verified |
| `evidence[]` | a tool call of this activity (`call_id`, `tool`) and a `quote` from its result, at most 500 characters |
| `evidence[].verified` | set by the **host**, not the model: `true` when the quote occurs in the result of that call (compared with runs of white space as one space), `false` otherwise. A hypothesis whose evidence is not verified is shown as unsupported |
| `plan[]` | a written remediation plan, step by step, with an optional `command` and `risk`, **for the administrator to carry out**. A host never runs a plan; doing any of it through an agent is a separate activity, decided by the gateway like any other |

**How a host gets it** (the reference implementation): the model is
offered the host's own tool `report_diagnosis` (its name has no `__`, so
it is no gateway tool; it changes nothing and is not listed in
`calls`). The model gives the summary, findings, hypotheses, the plan,
and each piece of evidence as the tool it quotes and the quote; the host
sets `call_id` and `verified` from the outputs of this activity's tool
calls, which it keeps in memory while the activity runs (at most 1 MiB,
never written anywhere), and numbers the plan's steps. A report that does
not hold together (no summary, a confidence other than the three, a
citation of evidence that is not there, a quote longer than 500
characters, more than 64 KiB in all) is refused and the model is told
why; nothing is recorded. The model is told which quotes are
unverified. A later report of the same activity replaces the earlier one;
`diagnosis.report` records each.

This is the recommendation's provenance: "I am saying this because of
this log line and that metric" is the evidence a hypothesis cites, and
the host checks that each quote is really in what the tool returned. The
quotes are excerpts of tool results, so a report can hold host data that
the journal otherwise does not (§4: arguments are never logged); the
control plane shows reports to supervisors and root only, as it does
activities.

**Feedback.** A person gives feedback on a report: per hypothesis
`confirmed`, `rejected` or `unsure`; for the plan `useful` or
`not_useful`, and whether they `executed` it; and an optional comment.
Feedback is trust evidence for the promotion path (§6b), so it is
recorded outside the agent, as the review verdicts are (§6a): by the
reference implementation's `agent-admin feedback` (§17), which records
the person, as `diagnosis.feedback` (§10). A later feedback by the same
person on the same report replaces the earlier one.

## 5. Control API

HTTP/1.1 on `/run/agents/<name>/api.sock`, JSON. The caller is identified
by `SO_PEERCRED`.

| Request | Who | Meaning |
|---|---|---|
| `GET /v1/status` | supervisors, root | `{"contract":1, "implementation", "lifecycle":{"kind",…} (§3a), "units":{"service","socket"} (systemd only), "name", "version", "model", "paused", "gateway":{"socket","reachable"}, "running", "in_flight":[call…], "started", "limits", "interaction":{"agui","client_tools"}, "schedules":[{"name","on_calendar","prompt"}], "events":[{"name","on_unit_failure","prompt","playbook"?,"min_interval_seconds"?}]}` |
| `GET /v1/activities` | supervisors, root | `{"activities": [activity…]}`, running first, then ended, newest first. Ended activities, with their reports, outlive the host process (an idle exit, a restart): the reference host keeps up to 200 of the last 30 days in its state directory (§13) |
| `GET /v1/playbooks` | supervisors, root | the host's playbooks (§14c) as this agent may run them now: `{"playbooks": {"<name>": {"title", "description"?, "runnable", "missing"?, "error"?}}}`; `runnable` when the gateway offers it the tool of every step that is not `optional`, else `missing` names the tools it lacks; `error` for a file that does not parse. `502` when the gateway's tools cannot be listed |
| `GET /v1/activities/{id}` | supervisors, root; an agent only for activities it delegated | one activity |
| `POST /v1/activities` | root (schedules, events, api); supervisors (`api`); agents (`delegation`, §9) | `{"prompt", "session"?, "user"? (root only), "origin"? (root only), "trace"? (agents only), "wait"?, "playbook"? (§14c; draft, O16)}`: `202 {"id","session"}`, or with `wait` the ended activity; `409` while paused |
| `POST /v1/activities/{id}/cancel` | root; the activity's user or originator | ends a running activity: `204`, `409` if not running |
| `POST /v1/pause`, `POST /v1/resume` | root | holds every activity before its next model or tool call and refuses new ones (§8) / lets them go on |

A host may serve more (the ADK launcher also serves the ADK REST API
for chat, Part II). The control plane uses only this table, §11 and
the gateway's control API.

Errors are `4xx`/`5xx` with a plain-text reason. Agents (callers named
`agent-*`) may only use `POST /v1/activities` and
`GET /v1/activities/{id}`.

## 6. Autonomy modes and policy

Each agent has an **autonomy mode**. The gateway enforces it, from data
the host never writes itself:

```yaml
# /etc/mcp-gateway/policy/agents/<name>.yaml  →  data.mcp.agents.<name>
<name>:
  mode: supervised        # advisory | supervised | bounded
  paused: false           # true: the gateway denies every call (§8)
  paused_by: "alice 2026-11-03T12:00:00Z"
  owners: ["user:alice"]  # added: may approve its calls besides the servers' approvers (§7)
  permissions:            # gateway permissions, with two flags
    - {server: zypp, tool: check_updates, read_only: true}
    - {server: zypp, tool: confirm_install}
    - {server: systemd, tool: change_unit_state, autonomous: true, args: {name: '^nginx\.service$', action: '^restart$'}}
```

| Mode | Read-only permissions | Other permissions | `autonomous: true` ones |
|---|---|---|---|
| `advisory` | allowed | denied | denied |
| `supervised` (default) | allowed | need an approval | need an approval |
| `bounded` | allowed | need an approval | allowed without one |

- **Supervised by default.** A permission is read-only only when it says
  `read_only: true`. A new agent gets `mode: supervised` and no
  permissions, so it can do nothing until an administrator lists what
  it may do.
- **Annotations are not trusted.** An MCP server's `readOnlyHint` never
  allows anything.
- **Agent data is the only source.** A gateway role bound to an agent
  user would bypass its mode, so it denies the agent everything. An
  `agent-*` principal without agent data gets nothing.
- **Agents over HTTPS.** On the gateway's socket an agent is its user
  `agent-<name>`. On the gateway's HTTPS listener (an agent in a sandbox,
  §16a) it is a token whose subject is `agent-<name>` from an issuer
  listed in `/etc/mcp-gateway/policy/agents/_http.yaml`
  (`_http: {issuers: [...]}`, none by default); that issuer gives such
  subjects to agents only. An `agent-*` subject from any other issuer is
  denied everything. Modes, pause and the emergency stop are the same on
  both.
- The rules are a Rego module added to the gateway's shipped policy
  (`mcp.authz`: `perms` and `denied`), installed as
  `/usr/share/mcp-gateway/policy/custos/agents.rego`. The gateway
  reloads the data within seconds of a change and tells agents that
  their tool lists changed.
- Each denial the module causes has a reason (`deny_reasons`): the
  agent is paused, the emergency stop, no agent data, an unknown mode, a
  role bound to the agent's user, an issuer not trusted for agents. A
  call that a permission of the agent data names but that is no
  permission now is explained (`unmatched_reasons`): its task category is
  not enabled on this host (§6b), a diagnostics permission that is not
  read-only, an advisory agent. The gateway puts the reason in the
  denial the agent receives and in its audit (mcp-gateway 1.14, its
  requirement R1 in `docs/requirements/mcp-gateway-for-custos-0.3.md`).

**Showing the posture.** The control plane reads the agent data file
for the mode, the pause flag and the permissions. It reads the gateway's
role data for the approver rules, and `GET /v1/status` for the host's
local limits. A different host implementation changes none of this.

## 6a. Task categories and the promotion path (draft 2, O11)

*Draft 2, partly implemented (O11).* Implemented: the catalog, the
`category` field, the effective mode in the agents module, the
envelope's `windows`, readiness and its thresholds, `promote` (refused
below readiness without `--despite-readiness`) and `demote`, and their
records, the demotion evaluator with the override rate and `rollback`,
the review of autonomous actions, and `rate` (O12), with the demotion
on repeated rate caps. Part I defines the data, the measures and
the records. The commands named here are the reference implementation's
(§17); another host or control plane provides the same actions. An
agent's autonomy is promoted **per
task category**, never for the whole agent. Promoting one category
changes nothing for another, and the audit records each category's
changes separately.

### Categories

A category names a kind of work. The catalog is data, shipped in
`/usr/share/custos/categories.d/*.yaml`; an administrator adds or
overrides entries in `/etc/custos/categories.d/` (the same name
replaces, an empty file disables):

```yaml
# /usr/share/custos/categories.d/patch-errata.yaml
patch-errata:
  title: Patch and errata management
  description: Installing patches and errata, refreshing repositories.
```

GA ships at least `patch-errata`, `security-response` and
`config-lifecycle`, and with the bundled diagnostics install (§6b, O16)
`diagnostics` and `compliance-audit`. A category name has the form of an agent name
(§13). Each permission in the agent data may name its category (an added
field, §6):

```yaml
<name>:
  mode: supervised            # as §6: the default for every category
  permissions:
    - {server: zypp, tool: check_updates, read_only: true}
    - {server: zypp, tool: confirm_install, category: patch-errata}
    - {server: systemd, tool: change_unit_state, category: config-lifecycle, args: {name: '^nginx\.service$', action: '^restart$'}}
  categories:                 # added: one entry per category that left the default
    patch-errata:
      mode: bounded
      envelope: {…}           # below
      promoted_by: "alice 2026-12-01T09:00:00.123456789Z"
      promotion: p-3f9c…      # the promotion record's id (§10)
```

`promoted_by` and `demoted_by` are the person (or `custos-demote`) and
the time, in RFC 3339 UTC with the fraction of the second: what the
category did after its promotion is what counts for it (below).

A permission's effective mode is its category's `mode` when the
category has an entry (`supervised` or `bounded`), otherwise
`supervised`. A permission without a category (`uncategorized`) has the
agent's `mode` and can never be promoted. `advisory` on the agent caps
every category. A category entry with another mode denies the agent
everything, as an unknown agent mode does (§6). §6's table applies
unchanged to the effective mode, and so do `read_only` and pause, which
need no category. Within a promoted category, the `autonomous: true` flag
of §6 is not needed: every permission of a `bounded` category is
autonomous inside its envelope, and `agent-admin permissions` refuses
`autonomous` together with `category`. An agent-level `mode: bounded` keeps
its version-1 meaning, but only for permissions without a category, and
is not part of the promotion path.

New agents and new categories start `supervised`, and no install-time
option changes that (§6). Only the promotion action below moves a
category to `bounded`.

### The policy envelope

A category is promoted only together with an envelope. The envelope
says when and how much the agent may act without an approval on this
host:

```yaml
envelope:
  windows:                              # when; outside them the category is supervised
    - {days: [sat], from: '02:00', to: '04:00', tz: Europe/Berlin}
  rate: {calls: 20, per: 1h}            # calls without approval, per tool of the category; per: 1s, 1m or 1h
  rollback:                             # conditions that demote at once (below)
    on: [tool_error, health_check_failed, limit_exceeded]
    health_check: 'systemctl is-system-running --quiet'   # run as root with sh -c, 60 s at most
```

| Part | Enforced by | Outside it |
|---|---|---|
| `windows` | the gateway's policy (`time.now_ns()` in the agents module); at least one is required. Each has `days` (`mon` … `sun`; every day if absent), `from` and `to` (`HH:MM`, `from` before `to`: a window across midnight is two windows) and `tz` (an IANA zone; `UTC` if absent) | the call needs an approval, as supervised |
| `rate` | the gateway: its `approval_over_rate` obligation (`"20/h"`; its requirement R6, mcp-gateway 1.15) on each of the category's permissions while they act without an approval, a sliding-window counter per agent and tool that it keeps in memory. `calls` is 1 or more; `per` is `1s`, `1m` or `1h` (the gateway counts per second, minute or hour); calls that need an approval anyway are not counted, nor is a call approved over the rate. A rate the policy cannot read makes the category supervised | the call needs an approval, for that call only (scope `once`), which names the rate reached (`over_rate`); reaching it three times within a day demotes the category (below) |
| `rollback` | the demotion evaluator (below), outside the agent. `tool_error`: a call of the category made without an approval since the promotion failed: the gateway's `mcp-result` for it is `tool_error`, `error` or `timeout` (its requirement R2; a call allowed by a grant is approved), or its `tool.result` is an error; `limit_exceeded`: an activity with such a call reached a limit; `health_check_failed`: `health_check` exited non-zero. `on` names at least one; `health_check` is given exactly when `health_check_failed` is | the category is demoted |

Everything that relaxes an approval is enforced by the gateway or by
the demotion evaluator, never by the agent. The envelope is per host.
Which hosts a category is promoted on, and a cap on how many hosts act
at once (blast radius), need the fleet, which is parked (O13). An
envelope part that the installed gateway cannot enforce makes `promote`
fail rather than be ignored.
Undoing an action (a snapshot rollback, say) is tool-specific and not
part of the envelope. A rollback condition demotes the category. The
control plane shows the approvers each automatic demotion of the last
week (the evaluator's `category.demoted` records, root's only, §10);
telling them through mcp-gateway's notification channels needs
mcp-gateway (O11).

### Observation, override rate and readiness

Before promotion, a category runs supervised for an **observation
period**. Each proposal of the category is an approval item (§7), so the
record already holds what readiness needs: the agent's records (§10)
and the gateway's decision for each approval id (its `mcp-approval`
audit record: `id`, `ok`, `by`, `principal`, and the call's `server` and
`target`). The proposals are the gateway's decisions, by the tool they
name: an agent that leaves out its `approval.pending`, or names another
tool there, changes no count (security review M6). The agent's records,
matched by the approval id, add what only it knows (the arguments'
digest, the result, a cancel, a limit, a pause); a decision without one
counts by what the gateway saw. A call belongs to a
category when a permission of that category that is not `read_only`
matches its tool (`server__tool`, the permission's `server` and `tool`
as globs), among the permissions **in force at the call's time**: those
after root's last `permissions.changed` record (§10) before the call;
before the first such record read, those it changed from; without one,
the current ones. A tool moved into a category later does not bring its
earlier calls along, and one moved out keeps them. A review of the
gateway's approvers counts by its verdict's time. Hosts write no
category of their own.

| Measure | Definition, over the last `window_days` |
|---|---|
| proposals | the gateway's decisions (approved or declined) on the principal's calls of the category, by the tool `mcp-approval` names (`server__target`; with a gateway that names none, the agent's `approval.pending` for the id). An approval without a decision (expired, or still pending) is no proposal |
| approved, declined | by the gateway's decision, never by the agent's `approval.resolved` (which also reports a tool's error after an approval as declined) |
| cancelled | approved, then an `activity.cancel` for its activity after the decision and before the call's `tool.result` |
| **override rate** | (declined + cancelled) / proposals |
| incident rate | incidents / (approved − cancelled); an incident is an approved call that failed (the gateway's `mcp-result` for its approval id is `tool_error`, `error` or `timeout`, or its `tool.result` is an error), whose activity has a `limit.exceeded`, or during whose activity (from the proposal to `invocation.end`) the agent was paused (`agent.paused`) or every agent was stopped (root's `agents.stopped`, the emergency stop, §8). A call the gateway saw cancelled is cancelled |
| diversity | distinct tools, and distinct tool and `AGENT_ARGS_SHA256` pairs, among the proposals |
| observed days | from the first proposal to now |

The thresholds are in `/etc/custos/promotion.yaml` (each key optional,
unknown keys refused), with these defaults:

```yaml
observation_days: 14   # observed days at least
proposals: 50          # proposals at least
override_rate: 0.05    # at most 5 %
incident_rate: 0.02    # at most 2 %
window_days: 30        # how far back the records are read
diagnostics_feedback: 10          # on a host that started diagnostics-only (§6b): reports with feedback at least
diagnostics_confirmed_rate: 0.7   # and at least 70 % of the decided hypotheses confirmed
```

A category is **ready** when it meets all of them. The **readiness
view** (`agent-admin readiness NAME [CATEGORY]`, §17; the control
plane; the Cockpit page) shows these measures per category against the
thresholds, with what is not met.

Not yet (O11): the envelope's size next to what was observed; and
listing the proposals behind each number.

In a `bounded` category, nothing is approved beforehand. Its override
rate is measured **afterwards**: the approvers review a sample of its
**autonomous actions**, the calls of the category since its promotion
that ran without an approval: those the gateway let through without an
approval or a grant (its `mcp-result`, with the decision id; the
activity and call from the agent's progress token, for correlation),
and any more the agent recorded (`tool.result` without an approval id,
and no `approval.pending` for the call). The agent can add actions to
review, never hide one. Of a week's actions all are
offered while there are at most 20, otherwise the newest unreviewed ones
up to 20 reviews in the week. A verdict is `ok`, or `incident`: the
action should not have run, which needs a reason. It is recorded as
`category.review` (§10); a later verdict on the same action replaces
an earlier one. Only verdicts root recorded count (§10, trusted
records): an agent cannot review its own actions.

The gateway's approvers review them too (mcp-gateway's review items,
its requirement R4). The agents module marks every call a bounded
category makes without an approval for review (`review_wanted`): once it
ran, the gateway offers it to whoever may decide the agent's calls on
its server (the servers' approvers, the agent's owners) on its approval
page, with `mcp-gateway-admin reviews`, on its control API (`GET` and
`POST /v1/reviews`) and in Cockpit's Approvals tab, as the person; an
item unreviewed for a week expires. Their verdicts, `ok` or `incident`
with a reason, are the gateway's audit records (`mcp-review`), counted
for the category of the call's tool as root's are; a later verdict on
the same item replaces an earlier one. In the measures, each reviewed action counts as a
decision, and each `incident` as an override:

| Measure | Definition |
|---|---|
| reviewed, review_overrides | reviewed actions of the category in the window, and of those the `incident` ones |
| **override rate** | (declined + cancelled + review_overrides) / (proposals + reviewed) |

An `incident` demotes a bounded category at once (§6a, Demotion).

### Promotion

```
agent-admin promote NAME CATEGORY --envelope FILE [--despite-readiness --reason TEXT]
```

The command checks the envelope against what the gateway can enforce,
and readiness against the thresholds. It refuses when readiness is not
met, unless it is given `--despite-readiness --reason TEXT`. It runs
as root, through sudo or Cockpit's administrative access, so the person
is authenticated, and it records the person (`SUDO_USER`). It writes
the category's entry in the agent data and a promotion record (§10).
It also refuses a category that is not in the catalog, an agent that is
`advisory`, and an agent that has no permission in the category.
Promoting a bounded category again replaces its envelope.

### Demotion

```
agent-admin demote NAME CATEGORY [--reason TEXT]
```

Demotion sets the category back to `supervised` and keeps the envelope
for a later promotion. A person demotes by hand. The **demotion
evaluator** (`custos-demote.timer`, every 5 minutes, as root, outside
any agent; `agent-admin evaluate`) demotes on its own when one of these
holds for a bounded category:

- its override rate, over the last 7 days and since its promotion,
  exceeds the threshold. Outside its windows the category is
  supervised, so it still has proposals an approver can decline;
- a `rollback` condition fires;
- calls of the category reached the envelope's `rate` three times within
  the last 24 hours since the promotion (the gateway's decision records
  with `over_rate`, for the agent's principal and the category's tools;
  a denial by a permission's own `rate_limit`, `"reason": "rate limit
  exceeded"`, counts too);
- an approver marks an autonomous action as an `incident` (`review`
  demotes at once; the evaluator also does, should that have failed;
  a verdict on the gateway is the evaluator's to act on).

`agent-admin promote` enables `custos-demote.timer` (the package leaves
it as the presets say). An automatic demotion takes effect as soon as
the gateway loads the data (§6), and it is recorded with `AGENT_BY`
`custos-demote` and the trigger as its reason. Pause and
the emergency stop (§8) still override everything.

## 6b. Category enablement (draft, O16)

*Draft, implemented (O16): the states, the gateway's enforcement,
`enable`, the refusals and their records, the tamper check.* Whether a
category may be used at all on this host is separate from its mode. A host is in one of two
**enablement states**:

| State | Usable categories |
|---|---|
| `diagnostics-only` | `diagnostics` only. Every other category of the catalog is installed but disabled, and so is every permission without a category |
| `categories` | `diagnostics` and the categories listed as enabled. Without any enablement data (every host before this section, and every installation of the broader capability) all catalog categories are enabled |

The state is gateway data, next to the emergency stop:

```yaml
# data.mcp.agents._enablement (§13: _enablement.yaml)
_enablement:
  state: diagnostics-only        # or: categories
  enabled: [diagnostics]         # with state categories: the enabled ones; diagnostics always
  changed_by: "alice 2026-12-01T09:00:00.123456789Z"
  record: n-7c1d…                # the enablement record's id (§10)
```

**The `diagnostics` category** reads host state, logs and metrics, runs
the diagnostic playbooks (§14c) and writes reports (§4a). Each of its
permissions is `read_only`: a permission with `category: diagnostics`
that is not is refused by `agent-admin permissions`, and the gateway
treats it as no permission. Promoting `diagnostics` is pointless and
refused: its calls need no approval in any mode but `advisory`.

**Enforcement is the gateway's**, outside the agent (§1):

- a call matched only by permissions of a disabled category (in
  `diagnostics-only`: by any permission not in `diagnostics`) is
  denied, in every mode, and the gateway's audit records the denial;
- tool discovery does not show such tools: the model is never offered
  a disabled category's tools;
- the control plane and `agent-admin` refuse to load a disabled
  category: `permissions` with a permission in it, `promote`, and
  `create --template` with a template whose permissions name it. Each
  refusal is recorded as `category.refused` (§10) with the person.

The agent's own record of a denied call is its `tool.result` with
`AGENT_OUTCOME=denied` (§10); the control plane names the cause from the
enablement state and the permission's category.

**Enabling** categories is one administrator action:

```
agent-admin enable CATEGORY… | --all
```

It runs as root, so the person is authenticated (sudo or Cockpit's
administrative access), and it writes the new state and an enablement
record `categories.enabled` (§10) with the person, the state before and
after, and the state's digest. A newly enabled category starts
`supervised` with no envelope (§6a): the result is the same agent data
and gateway behaviour as an installation of the broader capability with
those categories enabled, which a release-gate test compares.

**Disabling** is the way back (#110):

```
agent-admin disable CATEGORY… | --all
```

It runs as root and records `categories.disabled` (§10) with the same
fields. `--all` returns the host to `diagnostics-only`: every category
but `diagnostics`, and permissions without a category. On a host
without a state (every category enabled) it writes one: every category
of the catalog but those disabled. It refuses, naming what is in the
way and recording `category.refused` (`AGENT_REASON` `disable` or
`disable --all`), while an agent has a permission in a category it
disables (for `--all`: any permission outside `diagnostics`) or has
such a category promoted: the administrator removes the permissions
(`agent-admin permissions`) and demotes (`agent-admin demote`) first,
so that each step is recorded on its own. A disabled category's
readiness history stays; enabling it again starts it supervised.

**Tampering shows.** Only `agent-admin enable` and `disable` change the state, and
each change records the SHA-256 of `state` and `enabled` (JSON, sorted
keys). `agent-admin show` and the control plane compare the data in
force with the last enablement record (`categories.enabled` or
`categories.disabled`) and report a state that was
changed without one; only a record root wrote counts (§10, trusted
records).

**The seal** (#109) makes such a change ineffective, not only visible.
`agent-admin` signs the state it writes: a JWS (ES256) over `state`,
`enabled` and `record`, in the state's `signature`. The private key is
the systemd credential `custos-enablement-seal`
(`/etc/credstore.encrypted/`, `systemd-creds encrypt` with its default
key: the TPM's and the host's where there is a TPM), made by the first
change. Its public key is gateway data, `_seal.yaml`
(`data.mcp.agents._seal.public_key`). Once a host has a seal, the agents
module counts the state only when the signature verifies and covers it:
a state edited by hand, unsigned, or removed counts as
`diagnostics-only` (fail closed), and the module's reason for a denial
says the seal is broken. `agent-admin show` and every decision
`agent-admin` makes (enable, disable, permissions, promote, readiness,
`custos-setup`) use the state the gateway enforces; `show` reports the
broken seal (`tampered`) and `sealed`. A host without a seal (one set up
before 0.5, until its next change, or one installed with the full
capability and never changed) counts its state as it is. On one host
root can still decrypt the key and sign a state of its own: the seal
turns a change past `agent-admin` into a deliberate forgery; it cannot
stop root, which only an outside party could (the fleet, O13).

What the seal does not cover (security review M5): it covers the
enablement state, not the agents' data (their modes, permissions and
categories), and not itself. Removing `_seal.yaml` makes the host
unsealed: its state counts as it is again, edited or removed. A state
signed earlier still verifies: the seal has no freshness. Each change
needs root. `agent-admin show` reports both: a host without a seal whose
journal has a sealed record (`AGENT_SEALED`, §10) is `tampered` ("the
seal was removed"), and so is a state whose record is not the latest
enablement record ("an earlier state was put back"). A permission in
`diagnostics` names its server and tool (no pattern): on a
diagnostics-only host, `{server: "*", tool: "*", category: diagnostics,
read_only: true}` would allow what changes the system too, since the
gateway cannot tell a tool that only reads from its `read_only` flag.
`agent-admin permissions` refuses it; a hand edit is not sealed. Sealing
the agents' data is for a later release (accepted for 0.5.0).

**Trust evidence.** What the diagnostics agent did before another
category was enabled is that category's starting evidence: the readiness
view (§6a) also shows, per agent, over its window: the activities with a
diagnosis report (the last report of each), their hypotheses, their
quotes and how many the host verified; the reports with feedback (the
last feedback of each), the hypotheses confirmed, rejected and unsure and
the share confirmed of those decided; and the plans marked useful, not
useful and carried out. Feedback counts only from records journald gives
the trusted `_UID` 0, not the relay's (`agent-admin feedback` as root; §10, trusted records): an agent writes
its own records to the journal, with any identifier it likes, never with
another `_UID`. On a host that started diagnostics-only (it has an
enablement state), they are part of every category's readiness but
`diagnostics`' own: the thresholds `diagnostics_feedback` (reports with
feedback, at least) and `diagnostics_confirmed_rate` (confirmed of the
confirmed and rejected hypotheses, at least) of §6a. Only the agent's
own history counts: an agent created later earns its own, or is promoted
despite its readiness, with a reason. Elsewhere they are shown, not
counted.

## 7. Approvals

Approvals are the gateway's (mcp-gateway architecture §5.6.1). Neither
the agent nor its host can grant or forge one.

1. The agent calls a tool, with a progress token in the call's
   `_meta.progressToken` that is unique among its calls in flight
   (MCP's own field for naming a request). Policy says `ask` (channel
   `oob`). The gateway creates a pending approval and the call waits (gateway
   `approval_timeout`, 120 s by default; the approval itself stays
   pending after the call goes away, and the agent's next identical
   call takes it over).
2. The gateway sends the agent `notifications/message` (`logger:
   "mcp-gateway"`, `level: "notice"`) with the text `Waiting for approval
   of <server>/<tool> <where> (id <approval-id>).` Its `_meta` names the
   call: `"io.github.sdrahn.mcp-gateway/approval": {"id": <approval-id>,
   "requestId": <the call's JSON-RPC id>, "progressToken": <the call's>}`
   (sdrahn/mcp-gateway#333). The host assigns the approval id to the call
   with that progress token, and to no call if none in flight has it.
   From a gateway whose notice names no call, it assigns the approval id
   to the oldest call of that tool in flight without one, which can pair
   two calls of one tool crosswise. The activity's state becomes
   `waiting_approval`, the host writes `approval.pending`, and it emits
   AG-UI `agent.approval.pending` (§11).
3. A person decides through the gateway: its control API (`POST
   /v1/approvals/{id}`) or its Cockpit page. **Who may decide** is the
   gateway's approver rules. Agent users have no password, so `self`
   never lets an agent approve its own call (gateway roadmap step 40),
   and an agent cannot reach the control socket anyway (§1). Name the
   approvers of servers that agents use explicitly, e.g.
   `approvers: {zypp: ['role:admin', 'group:patch-approvers']}`, with
   people's groups only: never `agent-clients` or `mcp-users`, which
   every agent's user is in (§2), nor `agent-*` accounts; an agent never
   approves. Custos cannot narrow the gateway's own rules: its owner
   rules grant nothing to an approver named `agent-*`, and
   `agent-admin owners` refuses those groups and accounts.
   An agent's **owners** may decide its calls too, whatever the server's
   rules: `owners` in its agent data (§6), rules of the gateway's
   approver forms (`user:`, `group:`, `role:`, `remote-user:`,
   `remote-group:`; never `self`, an agent's account (`user:agent-*`,
   `group:agent-*`, `remote-user:agent-*`, `remote-group:agent-*`) or
   `group:mcp-users`, which every agent is in; an approver named
   `agent-*` is never an owner), which the
   policy module `owners.rego` adds to the gateway's approver rules
   (`mcp.approvals`: `allow`, `allow_without_self`, and `notify`, so they
   are mailed as the server's approvers are). They also see and may end
   the agent's sessions and tasks (`manage_session`, `manage_task`: the
   gateway's Clients tab, `mcp-gateway-admin clients`), which the
   servers' rules alone would give only to an approver of every server of
   the agent's endpoint: for the aggregated endpoint, of every server
   registered. An agent's session is its user `agent-<name>` on the unix
   socket, or that subject from an issuer trusted for agents.
4. The call returns. The host writes `approval.resolved`
   (`approved`/`declined`), then `tool.result`. The gateway's own audit
   records the decision and the approver.

**The approval item** the control plane shows joins two sources by
approval id:

| From the gateway (`GET /v1/approvals`) | From the agent (`GET /v1/activities`) |
|---|---|
| the exact call: server, tool, arguments, principal, the scopes offered | the activity: origin, who started it, its delegation chain |
| who may decide; the decision and the approver afterwards | the agent's stated rationale and expected impact (`calls[].rationale`, self-asserted) |
| | the evidence: the activity's earlier calls and their results' state |
| | what the model read before the call (`calls[].preceding`) and the signs of a prompt injection in it (`calls[].suspected_injection`): the item is marked *suspected prompt injection* when there are any |

Only the gateway's half is trustworthy. The page shows the agent's half
as the agent's claims.

The host also sends the agent's account with each gateway call, as a
proposal in the call's `_meta` (`io.github.sdrahn.mcp-gateway/proposal`,
mcp-gateway's requirement R5, from 1.15): the summary is the rationale
(the whole of it as the impact when longer than a line), the context the
agent, the activity, who started it (and, for a delegated activity, the
caller and the first activity of the chain; the playbook it runs), the
warnings the suspected-injection signs, each with the text found. Plain
text within the gateway's limits. The gateway shows it with the approval
on its own page, with `mcp-gateway-admin approvals`, in mails and
desktop notifications, and with a review item, marked as the client's
claim; it is never forwarded to the server, policy never sees it, and
the gateway's `mcp-approval` record has its digest. An older gateway
ignores it.

Hosts must not use any confirmation mechanism of their own in place of
the gateway's (such as ADK's tool confirmation). Those ask the session
user, who is not necessarily allowed to approve.

## 8. Intervention

| Control | Scope | How | Enforced by |
|---|---|---|---|
| **Decline** | one pending call | the gateway's approval: deny | gateway |
| **Revoke** | a grant | gateway `DELETE /v1/grants/{id}` | gateway |
| **Cancel** | one running activity | `POST /v1/activities/{id}/cancel` | host |
| **Pause** | one agent | the agent data's `paused: true` (the gateway denies every call) **and** `POST /v1/pause` (the host holds activities and refuses new ones) | gateway; host for the graceful part |
| **Stop** | one agent | by its `lifecycle` (§3a): `systemctl stop agent@<name>.socket agent@<name>.service`, or `openshell sandbox stop agent-<name>` and `systemctl stop agent-openshell@<name>.service` | systemd; OpenShell |
| **Disable a schedule** | one schedule | `systemctl disable --now agent-schedule@<name>_<schedule>.timer` | systemd |
| **Emergency stop** | every agent | `/etc/mcp-gateway/policy/agents/_control.yaml` with `_control: {stopped: true}` (the gateway denies every agent everything, on every backend), then stop every agent by its lifecycle and every schedule timer | gateway, systemd, OpenShell |

`agent-admin` implements pause, resume, `stop-all` and `release-all`
(Part II). An agent the gateway has no data for (another host's, which
the gateway denies everything anyway) is paused by its host alone. The emergency stop works even if an agent's host ignores
everything: the gateway denies the calls, and systemd stops the
processes. Neither waits for the other: `stop-all` writes the flag,
stops every unit (agents, schedules, events) and sandbox, and only then
waits for the gateway's OPA to load the flag; `pause` tells the agent's
host before it writes the gateway's data. A slow or failing OPA delays
neither, and the command says what is in force. Every intervention is journaled (`agent.paused`,
`agent.resumed`, `activity.cancel`, the gateway's policy change
records, systemd's unit records).

## 9. Delegation

Agents delegate to each other **through the gateway**. Delegation is
then a governed tool call, `agents__delegate {agent, task}`, allowed
only where the delegating agent's permissions say so. For example,
`{server: agents, tool: delegate, args: {agent: '^patch-pilot$'}}`
allows delegating to `patch-pilot` only. Under supervised mode a
delegation itself needs an approval unless it is marked `read_only`.

1. The delegating host adds a trace to the call's arguments:
   `_trace: {root_agent, root_activity, parent_agent, parent_activity}`.
2. The gateway runs the `agents` server (`agent-delegate`) **as the
   delegating agent's user** (`run_as: principal`). It posts the task to
   the callee's `POST /v1/activities` and waits for the result. The tool
   answers `{"agent", "activity", "state", "answer", "report"?, "reason"?}`:
   `answer` is the callee activity's `response` (§4, its last text in
   full, up to 32 KiB), or its `summary` from a host without one;
   `report` its diagnosis report (§4a), if it wrote one; `reason` why it
   did not finish. An activity that did not finish is a tool error. The
   answer is tool output like any other: the delegating host checks it
   for signs of a prompt injection (§4 `calls[].suspected_injection`).
3. The callee sees the caller as `agent-<parent>` by `SO_PEERCRED`. It
   records `origin: {kind: delegation, by: agent-<parent>}`. It believes
   the trace only when `parent_agent` is that caller, and otherwise
   records the activity without a parent.
4. The callee works as **its own principal**, under its own mode,
   permissions and approvals. Delegation never lends one agent's rights
   to another.

**Tracing a chain:** every activity's journal records carry
`ROOT_AGENT`/`ROOT_ACTIVITY`, and delegated ones also carry
`PARENT_AGENT`/`PARENT_ACTIVITY`. The control plane builds the tree
from these. The gateway's audit records of `agents__delegate` (by
`sub=agent-<parent>`) confirm each link independently of what agents
wrote.

Agents cannot reach each other directly. Their units hide
`/run/agents`, SELinux allows the agents' sockets to `mcpsrv_agents_t`
(the bridge) but not to `agent_t`, and the bridge only exists as a
gateway server.

## 10. Audit record

Hosts write journal records with these fields (journald's native
protocol; without a journal, JSON lines on stderr with lower-case keys
and without the `AGENT_` prefix):

| Field | Value |
|---|---|
| `MESSAGE`, `MESSAGE_ID`, `PRIORITY`, `SYSLOG_IDENTIFIER=agent-<name>` | journald's |
| `AGENT_CONTRACT` | `1` |
| `AGENT_NAME`, `AGENT_EVENT` | the agent; the event (table below) |
| `AGENT_SESSION`, `AGENT_INVOCATION`, `AGENT_USER` | session, activity id, session user |
| `AGENT_ORIGIN`, `AGENT_ORIGIN_BY`, `AGENT_SCHEDULE`, `AGENT_ORIGIN_EVENT` | §4 (`invocation.start`); `AGENT_ORIGIN_EVENT` is `origin.event` |
| `AGENT_PLAYBOOK` | `invocation.start`: the playbook the activity runs (§14c), if any |
| `AGENT_PARENT_AGENT`, `AGENT_PARENT_ACTIVITY`, `AGENT_ROOT_AGENT`, `AGENT_ROOT_ACTIVITY` | §9 (`invocation.start`, `invocation.end`) |
| `AGENT_TOOL`, `AGENT_SERVER`, `AGENT_CALL_ID`, `AGENT_ARGS_SHA256` | tool events. SHA-256 of the arguments' JSON with sorted keys. Arguments are never logged |
| `AGENT_RATIONALE` | `tool.call`: the model's stated reason (self-asserted) |
| `AGENT_PLAYBOOK_STEP` | `tool.call`: the playbook step (§14c) the call is, from 1, when the host made it; absent for the model's calls |
| `AGENT_SUSPECTED_INJECTION` | `tool.call`, `approval.pending`: the signs of a prompt injection (§4 `calls[].suspected_injection`), as `<tool> (<call id>) <sign>; …`, without quotes; absent when there are none |
| `AGENT_APPROVAL_ID` | approval events, and `tool.result` of an approved or declined call |
| `AGENT_OUTCOME`, `AGENT_REASON` | `ok`, `error`, `denied`, `approved`, `declined`, `cancelled`, `limit`; why |
| `AGENT_DURATION_MS`, `AGENT_LLM_CALLS`, `AGENT_TOOL_CALLS`, `AGENT_MODEL`, `AGENT_TOKENS_IN`, `AGENT_TOKENS_OUT` | end events, `model.call` (`DURATION_MS`: how long the call took, also when it failed) |
| `AGENT_PROMPT_BYTES` | `model.call` with `OUTCOME=error`: the size of the conversation sent (text and tool results), as the failed call reports no tokens. A failed call's record has `OUTCOME=error` and the error as `REASON`; an answered one has no `OUTCOME` |

| `AGENT_EVENT` | `MESSAGE_ID` |
|---|---|
| `agent.start` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a601` |
| `agent.stop` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a602` |
| `config.invalid` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a603` |
| `agent.paused` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a604` |
| `agent.resumed` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a605` |
| `invocation.start` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a610` |
| `invocation.end` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a611` |
| `activity.cancel` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a612` |
| `model.call` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a620` |
| `tool.call` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a630` |
| `tool.result` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a631` |
| `tool.denied.local` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a632` |
| `approval.pending` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a640` |
| `approval.resolved` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a641` |
| `limit.exceeded` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a650` |

**History of an action** (proposed → approved/declined → executed):
`tool.call` (with rationale) → `approval.pending` → the gateway's
approval decision record (in its journal `"event": "mcp-approval"` with
the approval `id`, `principal`, `ok`, the approver `by`, `scope` and
`via`; `op=mcp-approval` in the kernel audit) →
`approval.resolved` → `tool.result`, joined by `AGENT_CALL_ID` and
`AGENT_APPROVAL_ID`. The control plane reads them with
`journalctl -o json AGENT_NAME=<name>` and
`journalctl -o json -u mcp-gateway.service` (gateway records have
`"audit":true`, `sub`, `server`, `name`). Retention is journald's.

**Promotion records** (§6a). `agent-admin` and the demotion evaluator
write them with `SYSLOG_IDENTIFIER=custos-promotion`, `AGENT_CONTRACT`,
`AGENT_NAME` and these added fields:

**Trusted records.** An agent writes its own records to the journal, so
it can set any field there, `SYSLOG_IDENTIFIER=custos-promotion` and
`AGENT_EVENT` included, but never journald's trusted fields. A reader
that bases a decision on a record `agent-admin` writes (a review verdict,
feedback, an enablement change) takes it only when journald gives it
`_UID=0`, and not from the OpenShell relay: `agent-admin` runs as root,
agents never do. An OpenShell agent's records reach the journal through
its relay, which runs as root (§16a), so a record with the relay's
trusted `_COMM` (`agent-openshell`) or `_SYSTEMD_UNIT`
(`agent-openshell@*`) is never root's; journald reads those when a
record arrives, so a short-lived `agent-admin`'s record may lack them,
the long-running relay's never does. The relay passes on only the
events an agent host writes (`agent.*`,
`config.invalid`, `invocation.*`, `model.call`, `tool.*`, `approval.*`,
`limit.exceeded`, `activity.cancel`, `diagnosis.report`), and a record
never carries a field name outside `[A-Z0-9_]` (after `AGENT_`). The
gateway's records are those journald gives
`_SYSTEMD_UNIT=mcp-gateway.service`. The agent's own records (tool
calls, approvals, reports) are its account of what it did, and
readiness reads them as such, next to the gateway's decisions.


| Field | Value |
|---|---|
| `AGENT_CATEGORY` | the task category |
| `AGENT_PROMOTION` | the record's id (`p-…`), also in the agent data's `promotion` |
| `AGENT_MODE_FROM`, `AGENT_MODE_TO` | `supervised`, `bounded` |
| `AGENT_BY` | the person, or `custos-demote` for an automatic demotion |
| `AGENT_VERDICT` | `category.review`: `ok` or `incident` |
| `AGENT_INVOCATION`, `AGENT_CALL_ID`, `AGENT_TOOL` | `category.review`: the reviewed action |
| `AGENT_DECISION_ID` | `category.review`: the gateway's decision id of the reviewed call, when the gateway recorded it; a call reviewed by root and on the gateway counts once, with the later verdict |
| `AGENT_PERMISSIONS_FROM`, `AGENT_PERMISSIONS_TO` | `permissions.changed`: the agent's permissions (§6) before and after, as JSON |
| `AGENT_REASON` | the reason given, or the trigger (`override rate 7.1 % above 5.0 % since …`, `rollback tool_error: …`, `rollback health_check_failed: …`) |
| `AGENT_ENVELOPE_SHA256` | SHA-256 of the envelope's JSON with sorted keys |
| `AGENT_READINESS` | the readiness measures at the time, as JSON |

| `AGENT_EVENT` | `MESSAGE_ID` |
|---|---|
| `category.promoted` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a660` |
| `category.demoted` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a661` |
| `category.review` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a662` (an approver's verdict on an autonomous action) |
| `permissions.changed` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a663` (any change of an agent's permissions: `agent-admin permissions`, `create --template`) |
| `agents.stopped` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a664` (`stop-all`; `AGENT_NAME` empty) |
| `agents.released` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a665` (`release-all`; `AGENT_NAME` empty) |

Readiness reads `permissions.changed` and `agents.stopped` only as
root's (`_UID=0`, not the relay's, as every trusted record): an agent that wrote a
change of its own permissions or a stop into the journal would move its
calls between categories or add incidents.

**Diagnostics and enablement records** (§4a, §6b; draft, O16). The host
writes `diagnosis.report` with the agent's fields; `agent-admin` writes
`diagnosis.feedback`, `categories.enabled`, `categories.disabled`,
`category.refused` and `event.skipped` with
`SYSLOG_IDENTIFIER=custos-promotion`, `AGENT_CONTRACT`, `AGENT_NAME` (empty
for `categories.enabled` and `categories.disabled`, which are the host's) and these added fields:

| Field | Value |
|---|---|
| `AGENT_REPORT` | `diagnosis.report`: the report (§4a) as JSON, at most 64 KiB |
| `AGENT_PLAYBOOK` | `diagnosis.report`: the playbook, if any |
| `AGENT_INVOCATION` | `diagnosis.report`, `diagnosis.feedback`: the activity whose report it is |
| `AGENT_FEEDBACK` | `diagnosis.feedback`: `{"hypotheses": {"h1": "confirmed"}, "plan": "useful", "executed": true, "comment": "…"}` |
| `AGENT_BY` | the person |
| `AGENT_ENABLEMENT_FROM`, `AGENT_ENABLEMENT_TO` | `categories.enabled`, `categories.disabled`: the state before and after, as JSON (`{"state", "enabled"}`) |
| `AGENT_ENABLEMENT_SHA256` | `categories.enabled`, `categories.disabled`: the digest of the state after (§6b) |
| `AGENT_SEALED` | `categories.enabled`, `categories.disabled`: `1`, the host is sealed from this change on (§6b) |
| `AGENT_ENABLEMENT` | `categories.enabled`, `categories.disabled`: the record's id (`n-…`), also in the data's `record` |
| `AGENT_CATEGORY`, `AGENT_REASON` | `category.refused`: the disabled category, and what was refused (`permissions`, `promote`, `create --template patch-pilot`) |
| `AGENT_ORIGIN_EVENT`, `AGENT_ORIGIN_BY`, `AGENT_REASON` | `event.skipped`: an event (§14) that started no activity: the event, `unit-failure:<unit>`, and why (its activity for the unit still runs, the minimum interval, or the agent refused) |

| `AGENT_EVENT` | `MESSAGE_ID` |
|---|---|
| `diagnosis.report` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a670` |
| `diagnosis.feedback` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a671` |
| `categories.enabled` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a672` |
| `categories.disabled` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a675` |
| `category.refused` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a673` |
| `event.skipped` | `6f3c0e1a9b2d4c58a7e1f2d3c4b5a674` |

## 11. Interaction (chat as one mode)

Conversation with an agent is one mode of the control plane, for
driver-style tasks. It is not its basis. Hosts that offer it serve
**AG-UI** at `POST /agui` on the agent's socket:

- **Request:** AG-UI's `RunAgentInput` (`threadId`, `runId`, `messages`,
  `tools`, `state`, `context`, `forwardedProps`). The response is
  `text/event-stream` with one AG-UI event per `data:` line.
- **Threads are the caller's.** `threadId` is a session among the
  caller's own (by `SO_PEERCRED`); another user's thread id simply names
  a new thread of the caller's.
- **History is the host's.** Only the last `user` message (or the
  trailing `tool` messages, below) is new input. A frontend cannot
  rewrite history.
- `state` is merged into the session state under keys prefixed `ui:`.
- The run is an activity with `origin.kind: chat`.

| Host event | AG-UI event |
|---|---|
| run start / end / failure | `RUN_STARTED`, then `STATE_SNAPSHOT` (state without `temp:` keys) / `RUN_FINISHED` / `RUN_ERROR` |
| model text (streamed or whole) | `TEXT_MESSAGE_START`, `TEXT_MESSAGE_CONTENT`, `TEXT_MESSAGE_END` |
| tool call | `TOOL_CALL_START`, `TOOL_CALL_ARGS` (one JSON delta), `TOOL_CALL_END` |
| tool result | `TOOL_CALL_RESULT` |
| state change | `STATE_DELTA` (JSON Patch, one `add` per key) |
| §7 step 2 | `CUSTOM` `agent.approval.pending` `{"approval_id","tool","call_id","approval_url"}` |
| §7 step 4 | `CUSTOM` `agent.approval.resolved` `{"approval_id","tool","call_id","outcome"}` |
| local limit | `CUSTOM` `agent.limit.exceeded` `{"limit","value"}` |
| diagnosis report (§4a; draft, O16) | `CUSTOM` `agent.report` (the report) |

**Client tools** (`RunAgentInput.tools`) run in the frontend. They are
offered to the model only if the host's configuration allows them by
name; tools whose names contain `__` are refused, so they cannot pose
as gateway tools. Refused ones are journaled as `tool.denied.local`.
A call the model makes to a tool it was not given at all (neither
offered by the gateway nor a client tool, as after a prompt injection,
O14) is refused by the host too, never reaches the gateway, and is
journaled as `tool.denied.local` with `AGENT_REASON=not offered`, the
tool, the arguments' digest and the rationale.
When the model calls one, the stream ends after `TOOL_CALL_END`; the
frontend answers with a `role: tool` message on the same thread. Client
tools never reach the gateway and must not be used for anything an
approval would guard.

The stream is ephemeral and reaches only whoever is connected.
Approvals (§7), intervention (§8) and the audit record (§10) never
depend on it.

## 12. Scope: one system, fleet not precluded

Version 1 is **single-system**: every interface is local to the host,
and fleet scope is out of scope for SLES 16.2. Nothing here precludes it:

- every interface is reachable through Cockpit's bridge on that host, so
  Cockpit's host switcher already gives per-host supervision of many
  systems;
- identifiers are unique per host, and a fleet view keys everything by
  `(host, agent, activity)`;
- delegation chains across hosts would need a cross-host transport for
  `agents__delegate` (the gateway's Streamable HTTP with OAuth). The
  trace fields already carry the agent names, and would gain a host
  part (open item O7).

The fleet part of the requirement in §20a (a control plane for audit
aggregation, policy distribution and approvals across hosts) is parked
(O13); version 1 stays single-system.

---

# Part II — Reference implementation (ADK Go)

## 13. Files

| Path | Owner, mode | Contents |
|---|---|---|
| `/usr/libexec/custos/agent-launcher` | root 0755 | the host: one ADK agent per process |
| `/usr/libexec/custos/agent-delegate` | root 0755 | the gateway's `agents` server (§9) |
| `/usr/sbin/agent-admin` (and a link in the libexec directory) | root 0755 | administration (§17) |
| `/usr/sbin/custos-setup` | root 0755 | the bundled diagnostics install's setup (§17a; package `custos-diagnostics`) |
| `/usr/lib/systemd/system/agent@.{service,socket}`, `agent-schedule@.{service,timer}`, `agent-event@.service` | root 0644 | unit templates |
| `/usr/share/mcp-gateway/policy/custos/agents.rego` | root 0644 | the autonomy modes (§6) |
| `/usr/share/mcp-gateway/policy/custos/owners.rego` | root 0644 | owners approve their agents' calls, and see their sessions and tasks (§7) |
| `/usr/share/custos/gateway/servers.d/agents.yaml` | root 0644 | the `agents` server definition; copy to `/etc/mcp-gateway/servers.d/` to allow delegation |
| `/usr/share/custos/gateway/servers.d/custos-net.yaml`, `/usr/share/custos/gateway/custos-net.d/` | root 0644 | the server `custos-net` and its commands: the network diagnostics (§14c), run by mcp-gateway's `mcp-server-exec` with `network_view: host` (the host's network namespace, no IP traffic; mcp-gateway 1.15); `custos-setup` copies the definition to `/etc/mcp-gateway/servers.d/` |
| `/usr/share/custos/categories.d/*.yaml` | root 0644 | the task categories' catalog (§6a) |
| `/etc/custos/categories.d/*.yaml` | root 0644 | the administrator's categories: a file of a shipped name replaces it, an empty one disables it |
| `/usr/share/custos/templates/<template>/` | root 0644 | the agent templates (§14a), and `exec.d-disk-usage.yaml` and `exec.d-diagnostics.yaml`, the `exec` commands templates and playbooks (§14c) use, for `/etc/mcp-gateway/exec.d/` |
| `/etc/custos/templates/<template>/` | root 0644 | the administrator's templates: one of a shipped name replaces it, one without `template.yaml` hides it |
| `/etc/custos/defaults.yaml` | root 0644 | the model for new agents (§14b); absent: none |
| `/usr/share/custos/playbooks/*.yaml`, `/etc/custos/playbooks/*.yaml` | root 0644 | the diagnostic playbooks (§14c; O16): the package's, and the administrator's |
| `/etc/mcp-gateway/policy/agents/_enablement.yaml` | root 0644 | the enablement state (§6b; O16), signed (`signature`) once sealed; absent: every category enabled, or diagnostics-only on a sealed host |
| `/etc/mcp-gateway/policy/agents/_seal.yaml` | root 0644 | the seal's public key (§6b, #109) |
| `/etc/credstore.encrypted/custos-enablement-seal` | root 0600 | the seal's private key (`systemd-creds encrypt`) |
| `/etc/custos/agents/<name>/agent.yaml` | root:agent-`<name>` 0640 | configuration (§14) |
| `/etc/sysusers.d/agent-<name>.conf` | root 0644 | the agent's user |
| `/etc/mcp-gateway/policy/agents/<name>.yaml` | root 0644 | mode, pause, permissions (§6). All agents' files, the emergency stop's `_control.yaml` and the agents' token issuers' `_http.yaml` (§6; both shipped by the package) are in this one directory: mcp-gateway's OPA (`--watch`) watches only directories it loaded a file from when it started |
| `/etc/credstore.encrypted/agent-<name>.<cred>` | root 0600 | secrets (`systemd-creds encrypt`) |
| `/etc/systemd/system/agent@<name>.service.d/` | root | sandbox changes; Cockpit writes `50-cockpit.conf` only |
| `/etc/systemd/system/agent-schedule@<name>_<schedule>.timer.d/schedule.conf` | root | a schedule's `OnCalendar=` |
| `/etc/systemd/system/<unit>.d/50-custos-event-<name>_<event>.conf` | root 0644 | an event's `OnFailure=agent-event@<name>_<event>_<unit>.service` on a unit it watches (§14, §15) |
| `/run/custos/events/<name>_<event>_<unit>` | root 0600 | when the event last started an activity for the unit (gone at boot) |
| `/var/lib/agents/<name>/sessions.db` | agent-`<name>` 0600 | ADK sessions (SQLite) |
| `/var/lib/agents/<name>/activities.json` | agent-`<name>` 0600 | the ended activities (§5), at most 200 of the last 30 days |

## 14. Agent configuration (`agent.yaml`)

```yaml
version: 1
name: patch-pilot                     # = directory = unit instance
description: Keeps the host patched.
model:
  provider: openai                    # openai (any OpenAI-compatible server) | gemini | vertex
  name: qwen3-32b
  endpoint: http://127.0.0.1:8000/v1  # openai only
  credential: model-key               # optional: $CREDENTIALS_DIRECTORY/agent-<name>.model-key
  temperature: 0.2
  timeout_seconds: 1800               # optional: one model call's limit; absent or 0: none
instruction: "…"                      # or instruction_file: instruction.md
gateway:
  socket: /run/mcp-gateway/mcp.sock   # default
  servers: [zypp, systemd]            # whose tools the model is shown
  tools: []                           # optional narrower list of <server>__<tool>
  # OpenShell agents (§16a): url: https://gw.example.com:8443/mcp (the host's name), and
  # oauth: {token_url: https://idp/…/token, client_id: agent-<name>, scopes: [mcp],
  #         refresh_before_seconds: 60}   # optional; without it a static token
limits: {max_llm_calls: 25, max_tool_calls: 40, max_invocation_seconds: 900}
interaction:
  agui: true
  client_tools: deny                  # or {allow: [confirm_window]}
schedules:
  - {name: compliance, on_calendar: "Mon..Fri 06:00", prompt: "List pending security patches; install nothing."}
  - {name: health, on_calendar: daily, playbook: failed-units}   # a playbook (§14c); prompt optional
events:                               # activities started when a unit fails (§15)
  - name: web-down
    on_unit_failure: [nginx.service, php-fpm.service]
    prompt: "The unit {unit} failed. Find out why."   # {unit}: the one that failed
    playbook: service-health          # optional, as for a schedule
    min_interval_seconds: 600         # optional: the default; per unit
sessions: {retention_days: 30}
sandbox: {kind: systemd}             # or {kind: openshell, openshell_gateway: openshell} (§3a, §16a)
```

Unknown keys are errors. `model.credential` is the name a secret is
stored under (§17 `set-credential`), never the secret: `agent-admin`
(`check`, `create`, `edit`, `defaults`) refuses one that looks like a key
(a key's prefix such as `sk-`, or 32 characters or more with eight
digits) unless a credential of that name is stored, and no message
repeats it. After `create` and `edit` it says when the named credential
is not stored yet. An agent whose credential is not stored does not
start; the launcher's error names the `set-credential` command.
`servers` and `tools` only decide what the
model is *shown*; what the agent *may* do is §6. `model.timeout_seconds`
bounds one model call, from the request to the end of the answer; without
it (or with `0`) a call has no limit of its own, and
`limits.max_invocation_seconds` bounds it with the rest of the activity.
A call that runs out of either fails with an error naming the limit.
With `provider: openai`
and a local model server, an agent needs nothing outside the host, so
it works air-gapped.

## 14a. Agent templates

Starting points for agents, each a directory
`/usr/share/custos/templates/<template>/` (the package's) or
`/etc/custos/templates/<template>/` (the administrator's, which replaces
the package's of the same name; one without `template.yaml` hides it):

| File | Contents |
|---|---|
| `template.yaml` | `title`, `description`, `system` (`classic`, `transactional` or `any`: the systems it is for), `mode` (`advisory` or `supervised`: an agent is never created `bounded`), `needs` (what the gateway must have, as text). Unknown keys are errors |
| `agent.yaml` | the configuration (§14), its `name` the template's |
| `permissions.yaml` | the gateway permissions (§6), as `agent-admin permissions` takes them; categories (§6a) from the catalog, never `autonomous` |
| `envelopes/<category>.yaml` | optional: a suggested envelope (§6a) for a category its permissions name, for `agent-admin promote` |

A system is `transactional` when its root file system is mounted
read-only. Creating an agent from a template installs its configuration
and gives it the template's permissions and mode; its categories stay
supervised. Promoting one is a separate step, as for any agent. A
template grants nothing by itself: the gateway enforces the permissions
as it does any agent's.

The shipped templates read the product documentation through the
gateway's server `suse-docs`: mcp-docs over the installed documentation
collections (SUSE's `suse-docs-mcp-*` packages, `custos-docs`, others).
They are shown and allowed its reading tools `find_sections`, `search`,
`outline` and `read_section` (read-only; `diagnostics` where the
template's other permissions are), and their instructions say to check
findings and proposed steps against it and to name the sections used. A
gateway without `suse-docs` offers none of them (#130).

## 14b. Defaults for new agents

`/etc/custos/defaults.yaml` (root 0644, written by `agent-admin defaults`;
absent: none) holds the host's model for new agents:

```yaml
model:                       # as in agent.yaml (§14), checked the same way
  provider: openai
  name: granite-4
  endpoint: http://ai.example.com:8000/v1
  credential: model-key      # a name only; each agent's secret is its own
```

Unknown keys are errors. A new agent's starting configuration
(`agent-admin draft`, and `create --template` without `--from`) takes the
`model` block over: it replaces the template's or the blank
configuration's. An agent keeps the model in its own `agent.yaml`, so
changing or removing the defaults changes no existing agent. The file
holds no secret.

## 14c. Diagnostic playbooks (draft, O16)

*Draft, implemented (O16).* A playbook is a published, defined
diagnostic procedure: which read-only tools to call, and what to look
for. Playbooks are data, shipped in
`/usr/share/custos/playbooks/*.yaml`; an administrator adds or replaces
them in `/etc/custos/playbooks/` (a file of the same name replaces, an
empty one hides it):

```yaml
# /usr/share/custos/playbooks/disk-pressure.yaml
disk-pressure:
  title: Disk and inode pressure
  description: Which file systems are full, and what fills them.
  needs:                                   # text: what the gateway must offer
    - "exec: disk_usage (exec.d-disk-usage.yaml)"
    - "snapper-mcp, or mcp-server-tukit on a transactional system"
  steps:
    - {tool: exec__disk_usage, why: "Space and inodes used per file system."}
    - {tool: snapper__list_snapshots, why: "Snapshots that keep deleted data.", optional: true}
  look_for: >
    A file system above 90 % of its space or inodes; the journal,
    snapshots, package caches and logs under /var as the usual causes.
```

Unknown keys are errors. A playbook has a `title`, `look_for` and at
least one step that is not `optional`; each step a `tool`
(`<server>__<tool>`, as the gateway names it) and a `why`. A playbook
name has the form of an agent name.

Each step is a tool the agent calls through the gateway (§6), which
decides it like any other call: a playbook grants nothing. A playbook is
refused for an agent when the gateway does not offer it the tool of a
step that is not `optional` (its permissions do not list it, or its
category is disabled, §6b); `optional` steps the gateway does not offer
are left out. `look_for` guides the model; the report (§4a) cites what
it found.

An activity runs a playbook when `POST /v1/activities` names it
(`"playbook"`, §5; the `prompt` is then optional and added to it) or a
schedule names it (`playbook:` in `schedules`, §14; its `prompt` is then
optional too). The host gives the model the steps, `look_for` and the
request for a report, records the playbook in the activity (`playbook`,
§4) and in `invocation.start` (`AGENT_PLAYBOOK`, §10), and answers `404`
for an unknown playbook, `400` for one that does not parse, and `422`
when a step's tool is not offered, naming the tools; nothing runs then.
`GET /v1/playbooks` (§5) answers which playbooks the agent can run, so
that a console offers only those. An OpenShell agent reads the
playbooks of its sandbox's image.

The host makes the steps' calls itself, in order and without
arguments, before the model's first turn: the steps are fixed and read
only, and a model that has to issue them may fail to (a call it cuts
off). Each goes through the gateway like a call of the model's, with
the same decision, approvals and records; its rationale is
`Playbook <name>, step <n> of <m>: <why>`, it is marked
`calls[].playbook_step` (§4) and `AGENT_PLAYBOOK_STEP` (§10), and it is
not searched for signs of a prompt injection (the model chose nothing).
The host's turns are no model calls: no `model.call` record, and they
do not count toward `max_llm_calls`; they count toward `max_tool_calls`
and the activity's time. The model is told the steps were run and gets
their outputs; it may call further tools it is offered, and it reports.

The set GA ships: `failed-units`, `service-health`, `disk-pressure`,
`memory-pressure` (including OOM kills), `journal-errors`,
`selinux-denials`, `time-sync`, `network-config` (the interfaces, routes,
name servers and listening ports; the tools have no network, so it does
not test reachability), `boot-health`, `package-consistency` (unsatisfied
dependencies) and `update-status` (read only: what is pending, not
installing it). Their read-only commands for mcp-gateway's `exec` server
are `/usr/share/custos/templates/exec.d-diagnostics.yaml` (and
`exec.d-disk-usage.yaml`), to copy to `/etc/mcp-gateway/exec.d/`: they run
as the agent's user, without network, and take no arguments. The exec
server's instances have a network namespace of their own, so the network
commands (`network_addresses`, `network_routes`, `listening_sockets`) are
Custos's server `custos-net` instead, whose instances see the host's
network without using it (`network_view: host`).

## 15. Units and sandbox

`agent@.socket` listens on `/run/agents/%i/api.sock`. `agent@.service`
runs `agent-launcher serve` with:

- `User=agent-%i`, `SupplementaryGroups=mcp-users`;
- `ConfigurationDirectory=custos/agents/%i` (`ConfigurationDirectoryMode=0750`,
  as `agent-admin create` makes it), `StateDirectory=agents/%i`;
- `ImportCredential=agent-%i.*`, with `DeviceAllow=/dev/tpmrm0 rw`: systemd
  decrypts the credentials under the unit's device policy, and on a host
  with a TPM2 `systemd-creds encrypt` seals them to it;
- `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`,
  `PrivateDevices=yes`, `NoNewPrivileges=yes`, `CapabilityBoundingSet=`,
  `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`,
  `SystemCallFilter=@system-service ~@privileged @resources`,
  `MemoryDenyWriteExecute=yes`, `ProtectKernel*=yes`, `ProtectProc=invisible`
  and the rest of the template;
- `InaccessiblePaths=-/run/mcp-gateway/control.sock` and
  `TemporaryFileSystem=/run/agents:ro` (no control socket, no other
  agents).

The launcher exits after 10 minutes without requests or running
activities, and the socket starts it again. `agent-schedule@<name>_<schedule>.timer`
runs `agent-admin trigger <name>_<schedule>`, which posts the
schedule's prompt as root with `origin.kind: schedule`. (Not
`agent-launcher`: systemd would run its program in `agent_t`, which may
not reach agents' sockets.)

An event (§14) watches systemd units: `agent-admin` puts a drop-in
`50-custos-event-<name>_<event>.conf` with
`OnFailure=agent-event@<name>_<event>_<unit>.service` on each unit it
names, and removes it when the event or the agent goes. When the unit
fails, systemd starts that unit, which runs `agent-admin event
<name>_<event>_<unit>`: it posts the event's prompt, `{unit}` filled in
and systemd's account of the failure added (`MONITOR_SERVICE_RESULT`,
`MONITOR_EXIT_CODE`, `MONITOR_EXIT_STATUS`), as root with
`origin: {kind: event, by: "unit-failure:<unit>", event: <event>}`.
It is damped per event and unit: it starts nothing while the event's
activity for the unit still runs, nor within `min_interval_seconds`
(default 600) after it last started one, and records each skip
(`event.skipped`, §10). An event may not watch Custos's own units
(`agent@…`, `agent-…`, `custos-…`), so it cannot start itself, nor a
template instance (`getty@tty1.service`). Pause, the emergency stop and a
stopped agent refuse its activity like any other; the refusal fails the
event's unit and is recorded too.

`custos-demote.timer` runs `custos-demote.service` (`agent-admin
evaluate`, as root, outside any agent) every 5 minutes and 2 minutes
after boot: the demotion evaluator (§6a). `agent-admin promote` enables
it.

## 16. The launcher

- **Tools:** ADK's MCP toolset on the gateway's aggregated endpoint,
  reconnecting per connection. The client declares no `elicitation`
  capability, so the gateway never asks the agent to approve its own
  call. `clientInfo.name` is `custos/<name>`. No other tools,
  apart from AG-UI client tools that the configuration allows.
- **Governance plugin** (ADK plugin): activities (§4), pause, cancel,
  local limits, journal records (§10), approval notices (§7), the
  delegation trace (§9), the calls' rationale.
- **Socket:** the control API (§5), AG-UI (§11) and ADK's REST API for
  chat. For the REST API, only the caller's own sessions are reachable
  (root: all), and only `/health`, `/version`, `/list-apps`,
  `/apps/<name>/users/<caller>/sessions…`, `/run` and `/run_sse`; ADK's
  builder and debug endpoints answer 404.
- **Models:** `openai` (`internal/openai`: chat completions with tools),
  `gemini`, `vertex`.

## 16a. The OpenShell backend (draft, O10)

An agent with `sandbox: {kind: openshell}` runs under NVIDIA OpenShell
instead of `agent@.service`:

| Piece | What |
|---|---|
| image | `packaging/openshell/Containerfile`: Leap 16 with `agent-launcher` (`CUSTOS_OPENSHELL_IMAGE`, default `localhost/custos-agent:latest`). It resolves names by DNS first (`hosts: dns files`): the sandbox's DNS is OpenShell's policy DNS, and Podman copies the host's `/etc/hosts` into the sandbox, whose real addresses OpenShell's network fence refuses (#131) |
| sandbox `agent-<name>` | created by `agent-admin` with `--policy`, `--provider agent-<name>-gateway` and the environment: the configuration (`CUSTOS_CONFIG`, base64), the relay secret (`CUSTOS_RELAY_SECRET`), the gateway token's placeholder (`AGENT_GATEWAY_TOKEN`, set by OpenShell). The configuration and the relay secret go in with `--env-from`, from the CLI's environment, never on its command line, which any local user can read. The agent runs as OpenShell's `sandbox` user and serves its control API on `127.0.0.1:<port>` inside the sandbox (port 47000–47999, stable per name; `agent-admin create` refuses an OpenShell agent whose port another one has) |
| sandbox policy | `/etc/custos/agents/<name>/openshell-policy.yaml`: Landlock as a hard requirement; the image's system read-only; the network only to the model endpoint, for `agent-launcher` only (the gateway's rule comes with the provider). The host's loopback is `host.openshell.internal` (in the policy and in the configuration the sandbox gets) |
| provider profile | `custos-gateway-<name>` (`openshell-profile.yaml`): the bearer token `AGENT_GATEWAY_TOKEN`, valid for the gateway's endpoint only, which attaching the provider adds to the sandbox's network policy (`protocol: mcp`, every known MCP method: the tools are the gateway's decision; the protocol revisions `2025-06-18` and `2025-11-25`, since OpenShell refuses the others; for `agent-launcher` only). The agent sends `Authorization: Bearer $AGENT_GATEWAY_TOKEN` (`gateway.token_env`); the supervisor swaps the placeholder for the token on the way out |
| relay secret | in the sandbox's environment, which OpenShell flags as readable by the agent. That is intended: it proves to the agent that a request comes from the relay, so the agent must know it; host users who cannot reach the agent's socket must not (`relay.secret` is root's, 0600). The OpenShell user (rootless Podman) can read it, and can enter every sandbox: it is trusted as far as the OpenShell agents go |
| relay | `agent-openshell@<name>.socket` owns `/run/agents/<name>/api.sock`; `agent-openshell@<name>.service` starts the sandbox (`agent-openshell-start`: until OpenShell has it Ready, waiting out a stop still in progress, or the unit fails after 120 s and `Restart=` tries again) and `openshell forward` (as root; a forward left from before is stopped first, and a failed start fails the unit), and runs `agent-openshell-relay` (SELinux `agent_relay_t`): callers by `SO_PEERCRED`, passed on as `X-Custos-Caller` with `X-Custos-Relay-Secret` (the agent's `--listen-tcp` mode believes nothing else), only to a socket root owns (the forward's; the kernel's socket table says whose, before anything is sent), so a local user who holds the port gets nothing; and the agent's records from its container's output (`agent-openshell-logs`: `docker logs` or `podman logs` of the sandbox's container; `openshell logs` has only the supervisor's lines), written to the journal with their `AGENT_*` fields |
| openshell CLI | `agent-admin` and the relay unit run it as root with `HOME=/root`: the OpenShell gateway named in `sandbox.openshell_gateway` is registered for root (`openshell gateway add ... --name <gateway>`), and the CLI keeps its forwards there |
| gateway | mcp-gateway's HTTPS listener (`http:` in its configuration): the agent's `gateway.url` is its audience, by the host's own name, e.g. `https://gw.example.com:8443/mcp`, which the certificate names, an official one included (#131). OpenShell's proxy runs in the host's network (OpenShell 0.1.7, Docker and Podman) and resolves and connects to that name as the host does; it allows a private address for a host the policy names exactly, never a loopback or link-local one, so the name must resolve to a network address of the host. With Podman the sandbox resolves it through OpenShell's policy DNS, which asks the first nameserver of the host's `/etc/resolv.conf` and never reads `/etc/hosts`: the name must be in DNS. A loopback URL becomes `host.openshell.internal` in the sandbox, OpenShell's name for the host's loopback, which only a private CA's certificate can name. `agent-admin check`, `create` and `edit` note an agent by the alias, a name that is not in DNS (asked as policy DNS does) or resolves to a loopback address, and a URL other than the gateway's `http.audience`. OpenShell's proxy terminates the agent's TLS (to inspect MCP and swap the token in) and verifies the gateway itself, in the sandbox's supervisor, not with the image's trust store: a CA no public root signs goes into the OpenShell gateway's configuration (`[openshell.supervisor.network] additional_ca_bundle = "<ca.pem>"` in its `gateway.toml`, OpenShell 0.1.7 or later), which hands it to every sandbox's supervisor |
| container runtime | Docker, or rootless Podman (OpenShell's RPM default: the OpenShell gateway is a `systemctl --user` service of an ordinary user, whose Podman holds the sandboxes' containers). With rootless Podman, `CUSTOS_OPENSHELL_USER` names that user for `agent-admin`: it goes to `openshell.env` (`OPENSHELL_USER`), the log source runs `podman logs` as that user (`setpriv`; SELinux: `agent_relay_t` → `container_runtime_t`), and a drop-in `agent-openshell@<name>.service.d/openshell-user.conf` makes only the user's container storage and runtime directory writable for it. The OpenShell gateway is registered for root as well (§16a, openshell CLI). The system delegates the cgroup controllers to user sessions (a `user@.service` drop-in with `Delegate=cpu cpuset io memory pids`), without which rootless Podman cannot set the sandboxes' limits, and the user has subordinate IDs and lingering |
| token | With `gateway.oauth` (the agent is an OAuth client of the issuer): `agent-admin set-credential <name> gateway-client-secret < secret` creates the provider with runtime credentials, gives OpenShell's gateway the client (`openshell provider refresh configure … --strategy oauth2-client-credentials`, the secret through the environment, never a command line), mints the first token (`provider refresh rotate`; a refused client fails here) and creates the sandbox. OpenShell's gateway then renews the token before it expires (the profile's `refresh`: token URL, scopes, `refresh_before`); the sandbox's placeholder resolves to the current token per request, so renewals need no restart of the agent. The issuer's client configuration gives the tokens the subject `agent-<name>` and the gateway's audience (the grant sends no audience). `agent-admin idp-client` says what that client must be (for Keycloak, a script that creates it) and `check-token` checks a real token's claims before `set-credential` (§17). Without `gateway.oauth`: `set-credential <name> gateway-token < token` with a static token, which OpenShell does not renew. Either way the token is an OAuth access token for the gateway's audience, subject `agent-<name>`, from an issuer in `_http.yaml` (§6). It carries no claim of the gateway's `local_user_claim`: the gateway maps a token to a local user only for accounts of persons (UID_MIN..UID_MAX) and refuses system accounts, which agents' users are, so the subject stays the principal |

`stop-all` stops the relay units and every OpenShell agent's sandbox by
name; the gateway's `agents_control` flag denies them like any agent.

`edit` (§17) applies the new `agent.yaml` to the sandbox, since the
sandbox holds what it was created with: the configuration
(`CUSTOS_CONFIG`), the policy and, through the provider, the gateway's
endpoint. `openshell.env` takes nothing from `agent.yaml` and stays.

| The edit changes | `edit` does |
|---|---|
| nothing the sandbox gets | `systemctl try-restart agent-openshell@<name>.service` |
| the configuration as the sandbox gets it (model, instruction, servers, tools, limits, interaction, schedules, …), the policy (`model.endpoint`) or the profile (`gateway.url`, `gateway.oauth`'s token URL, scopes, `refresh_before_seconds`) | rewrites `openshell-policy.yaml` and `openshell-profile.yaml` (and imports the profile again if it changed), stops the relay's socket and service, recreates the sandbox with the provider it has and the image it was created from (`AGENT_IMAGE`; OpenShell deletes a running sandbox in the background, so it waits, up to 2 minutes, until `openshell sandbox get` no longer finds the old one), then starts the socket and service that ran before; if the service did not run (after `stop-all`, or idle), it stops the new sandbox, which the relay starts when it starts. The state inside the old sandbox (sessions) goes with it, as with `set-credential` |
| the credential's kind: `gateway.oauth` added or removed, or its `client_id` | the provider must be made again with the secret, which `agent-admin` does not keep: rewrites the files, disables and stops the relay, deletes the sandbox and the provider, and asks for `set-credential`, which creates both again |
| `sandbox.kind`, `sandbox.openshell_gateway` | refused: `delete` and `create` |

Before the first `set-credential` (no sandbox yet, the relay's socket
not enabled), `edit` rewrites the files only; `set-credential` creates
the sandbox from them.

## 17. `agent-admin`

Run as root, by hand or by Cockpit with administrative access. Its
standard output is its own messages (and `list`'s and `show`'s
results), which a control plane may show; what the programs it runs
print (`systemctl`, `systemd-sysusers`, `openshell`, …) goes to
standard error:

| Command | Does |
|---|---|
| `create NAME --from FILE` | `FILE` `-` (here and for `edit` and `check`) is stdin, so a control plane needs no file of its own. Validates; writes the sysusers.d file and runs `systemd-sysusers`; installs `agent.yaml`; writes the gateway agent data (supervised, no permissions); writes the schedules' timer drop-ins; enables the socket and timers |
| `edit NAME --from FILE` | validates, replaces `agent.yaml`, syncs schedules (a new schedule's timer is enabled and started, a removed one's disabled; one that stays keeps its state, and restarts for its calendar if it runs); systemd agents: `try-restart agent@<name>.service`; OpenShell agents: the policy, the profile and the sandbox as in §16a (at least `try-restart agent-openshell@<name>.service`). `sandbox.kind` (and `sandbox.openshell_gateway`) stay as created |
| `create NAME --template TEMPLATE [--from FILE]` | `create` from a template (§14a): its configuration as `draft` gives it (named `NAME`, with the model defaults), or `FILE`'s; then the template's permissions and mode in the gateway agent data |
| `draft NAME [--template TEMPLATE]` | a new agent's starting configuration on standard output: the template's, or a blank one, named `NAME`, with the model defaults (§14b). Needs no root |
| `defaults [--from FILE \| --clear]` | the model defaults (§14b), as JSON: `{"model"?: {"provider", "name", "endpoint"?, "credential"?, "temperature"?, "timeout_seconds"?}}`, needing no root; `--from` checks and replaces them (`FILE` `-` is stdin), `--clear` removes them |
| `templates` | the templates (§14a), as JSON: `{"transactional", "templates": {"<template>": {"title", "description", "system", "mode", "needs"?, "dir", "servers", "categories"?, "envelopes"?, "error"?}}}`, where `error` says why one cannot be used (a category not in the catalog, say). Needs no root |
| `check NAME\|FILE` | validates only; needs no root for a FILE |
| `mode NAME MODE`, `permissions NAME < list.yaml` | the gateway agent data (§6). Like `pause`, `resume`, `stop-all` and `release-all`, it returns once the gateway's OPA has loaded the change (`stop-all` and `pause` act on the host first, §8) (it reads it back on `/run/mcp-gateway/opa.sock`, for up to 20 s), so the change is in force when the command returns |
| `owners NAME [RULE…]` | sets who may approve the agent's calls besides the servers' approvers (§7), in its agent data; none clears them. Refuses `self` and an agent's own user or group; returns once the gateway's OPA has loaded it |
| `set-credential NAME CRED < secret` | `systemd-creds encrypt`; for OpenShell agents `gateway-client-secret` or `gateway-token` (§16a) |
| `idp-client NAME [--keycloak]` | the OAuth client an OpenShell agent needs at the issuer (§16a), from the gateway's `http` settings (`/etc/mcp-gateway/gateway.yaml`) and `_http.yaml`: the client id (`gateway.oauth.client_id`, else `agent-<name>`), the client credentials grant, the claims. `--keycloak`: a `kcadm.sh` script (`$KCADM`) that creates it in the issuer's realm and prints only its secret. Changes nothing itself |
| `check-token NAME < secret` | gets a token with the agent's `gateway.oauth` and the secret from standard input (the client credentials grant, as OpenShell does) and checks, one line each (`ok`/`FAIL`): `iss` is `http.issuer` and listed in `_http.yaml`, `sub` is `agent-<name>`, `aud` has `http.audience`, `scope` has each of `http.scopes`, `exp` in the future and at most an hour after `iat`, no `local_user_claim`. Exits 1 on a failure. Not the signature: the gateway checks it |
| `credentials [NAME…]` | the named agents' credentials, or all agents', as JSON: `{"<name>": {"credentials": [{"name", "stored", "stored_at"?, "used_by": ["model"\|"gateway"], "held_by"?: "openshell"}], "missing": ["<cred>"]}}`. Names only, never a value. A credential the configuration names (`model.credential`; an OpenShell agent's gateway credential) that is not stored is listed with `stored: false` and in `missing`: the agent does not start without it. An OpenShell agent's gateway credential is `held_by` OpenShell, `stored` once its relay's socket is enabled. Needs root (the store is root's) |
| `remove-credential NAME CRED [--force]` | removes a stored credential; the one `model.credential` names only with `--force`. Refuses `gateway-token` and `gateway-client-secret`, which OpenShell holds |
| `pause NAME`, `resume NAME` | §8 (gateway data and `/v1/pause`) |
| `enable CATEGORY… \| --all` | enables task categories on a `diagnostics-only` or partly enabled host (§6b): writes `_enablement.yaml` and the record `categories.enabled`, and returns once the gateway's OPA has loaded it. The categories start supervised. Refuses a category not in the catalog |
| `disable CATEGORY… \| --all` | disables task categories again (§6b, #110); `--all` returns the host to `diagnostics-only`. Writes `_enablement.yaml` and the record `categories.disabled`. Refused, naming what is in the way, while an agent has a permission in a category it disables or has one promoted; `diagnostics` is never disabled |
| `diagnostics-only` | makes a fresh host `diagnostics-only` (§6b): the bundled install's step, which `custos-setup` runs (§17a). Refuses a host with enablement data (`disable --all` is the way back there) and one with an agent that has a permission outside `diagnostics` |
| `feedback NAME ACTIVITY [--hypothesis ID=confirmed\|rejected\|unsure…] [--plan useful\|not_useful] [--executed] [--comment TEXT]` | feedback on an activity's report (§4a), recorded as `diagnosis.feedback` with the person |
| `playbooks` | the playbooks (§14c), as JSON: `{"<playbook>": {"title", "description", "needs"?, "steps": [{"tool", "why", "optional"?}], "look_for", "file", "error"?}}`, where `error` says why a file cannot be used. Needs no root |
| `stop-all`, `release-all` | the emergency stop (§8) and its end. `release-all` starts again the agents' sockets and schedule timers that are enabled: what an administrator disabled stays stopped. Both record who (`_control.by`: the person behind `sudo`, `SUDO_USER`, else `USER`) and when (`at`) |
| `delete NAME [--keep-state]` | disables and stops units; removes configuration, gateway data, credentials, drop-ins, timers and, unless kept, state. The user stays, since uids are not reused while files may still belong to them |
| `trigger NAME_SCHEDULE` | starts the schedule's activity (run by `agent-schedule@.service`) |
| `event NAME_EVENT_UNIT` | starts the event's activity for a unit that failed, damped (run by `agent-event@.service`, §15) |
| `list` | the agents |
| `show [NAME…]` | the gateway data (§6) of the named agents, or of all, and the emergency stop (§8), as JSON: `{"agents": {"<name>": {"mode", "paused", "paused_by"?, "permissions", "categories"?}}, "control": {"stopped", "by"?, "at"?}, "enablement"?: {"state", "enabled", "changed_by", "record", "tampered"?}}`, where `categories` is §6a's entries and `enablement` §6b's state (absent: every category), with `tampered` saying why it does not match its record. Needs no root: the files are readable by everyone; without access to the journal there is no tamper check |
| `categories` | the catalog (§6a), as JSON: `{"<category>": {"title", "description"?}}`. Needs no root |
| `promote NAME CATEGORY --envelope FILE [--despite-readiness --reason TEXT]`, `demote NAME CATEGORY [--reason TEXT]` | a category's promotion with its envelope (`FILE` `-` is stdin), and its demotion (§6a). They write `categories.<category>` in the gateway data and a promotion record (§10), and return once the gateway has loaded the change, as `mode` does. `promote` refuses a category that is not ready, naming what is not met, unless it is given `--despite-readiness` with a reason; its record's `AGENT_READINESS` is the category's measures |
| `evaluate` | the demotion evaluator (§6a), which `custos-demote.service` runs: demotes every bounded category whose override rate or rollback conditions say so, printing one line per demotion, and posts a notice of each to the gateway's approvers (mcp-gateway's `POST /v1/notices` on its control socket, as root: about `agent-<name>`, for the servers of the category's permissions, severity `warning`), printing how that went. A notice the gateway refuses leaves the demotion as it is. An agent whose journal it cannot read is reported, and the others are still evaluated |
| `review NAME [CATEGORY…]`, `review NAME CATEGORY ACTIVITY CALL ok\|incident [--reason TEXT]` | the review (§6a). Without a verdict: per category promoted since (bounded, or demoted after a promotion) the week's autonomous actions still to review, as JSON: `{"agent", "categories": {"<category>": {"mode", "actions", "reviewed", "pending": [{"activity", "call", "tool", "outcome", "at", "args_sha256"?, "decision_id"?}]}}}`. With one: records `category.review` (the person behind `sudo` as `AGENT_BY`) for an action that ran without an approval in the last week; `incident` needs a reason and demotes a bounded category. Reads the journal: root or `systemd-journal`; a verdict needs root |
| `readiness NAME [CATEGORY…]` | the readiness of the agent's categories (those its permissions name), or of the given ones, as JSON: `{"agent", "thresholds": {"observation_days", "proposals", "override_rate", "incident_rate", "window_days"}, "categories": {"<category>": {"proposals", "approved", "declined", "cancelled", "reviewed", "review_overrides", "overrides", "override_rate", "incidents", "incident_rate", "tools", "distinct_calls", "observed_days", "first_proposal"?, "ready", "unmet"?}}, "diagnostics": {"reports", "hypotheses", "quotes", "verified_quotes", "with_feedback", "confirmed", "rejected", "unsure", "plans_useful", "plans_not_useful", "plans_executed", "confirmed_rate"}, "diagnostics_apply"}` (§6a; `diagnostics` §6b; `diagnostics_apply`: they count in the readiness, the host started diagnostics-only). Reads the agent's journal and mcp-gateway's, so it needs root or the `systemd-journal` group |

## 17a. `custos-setup` (draft, O16)

*Draft, implemented (O16).* The installer's interface for the bundled diagnostics install. The
installer itself (the choice, not selected by default, and the backends
in order) is the installer team's (§20b); it runs, as root, after the
packages are installed:

```
custos-setup --diagnostics-only --backend on-host|local-network|customer-cloud|vendor
             [--provider openai|vertex|gemini] [--endpoint URL] --model NAME
             [--credential-file FILE]
```

It is non-interactive and idempotent. In this order, each step printed
on standard output as `custos-setup: <step>: done|already|skipped
(<why>)`:

1. it writes the state `diagnostics-only` (§6b) with its record (by
   `custos-setup`) on a fresh host, and refuses a host with an agent
   that has a permission outside `diagnostics`, writing nothing; on a host
   whose administrator has enabled more since, it goes back on nothing;
2. it sets the model defaults (§14b), with `credential: model-key` when
   a key is given;
3. it copies the playbooks' commands (`exec.d-diagnostics.yaml`,
   `exec.d-disk-usage.yaml`, §14c) into `/etc/mcp-gateway/exec.d/` where
   the exec server is installed, keeping a file of the same name that
   differs. The exec server reads its commands when an instance starts,
   so when it added one it stops the server's running instances through
   the gateway's control API (`GET /v1/servers`, `DELETE
   /v1/instances/{id}`, as Cockpit's Servers tab); without the gateway
   running it says so. A copy of an earlier release's file, unchanged
   (by its digest), it replaces. It also copies the server definition
   `custos-net` into `/etc/mcp-gateway/servers.d/`, keeping one that
   differs. All this before the agent, whose session then offers the
   commands;
4. it creates the agent `health-check` from its template (§14a), which
   takes the defaults' model; one that exists keeps its configuration,
   and is restarted if step 3 added a command;
5. it stores the key from `FILE` (never from the command line) as the
   agent's `model-key` and restarts the agent if it runs; again, a new key
   replaces the old one.

The agent's socket and schedule timers are enabled by its creation.
Exit status 0: set up; 1: not (the reason on standard error); 2: wrong
arguments. Without `--diagnostics-only` nothing is written: the broader
install has all categories, and no agent until an administrator creates
one.

The agent is supervised: its permissions are read-only and need no
approval, and a category enabled later starts supervised for it as for
any agent (§6b), which an `advisory` agent would cap.

The four backends, in the documented order (§14, user guide chapter 14):
`on-host` (a model server on this host), `local-network` (one on the
local network), `customer-cloud` (the customer's own cloud account) and
`vendor` (a vendor-hosted service). `on-host` and `local-network` are
`provider: openai` with the server's endpoint (`on-host`'s default
`http://127.0.0.1:8000/v1`, `local-network`'s required);
`customer-cloud` needs `--provider` (`vertex`, or `openai` with the
endpoint of a server in the account); `vendor` is `gemini` unless
`--provider` says otherwise, and needs `--credential-file`.

An installer profile runs it after the packages, for example as an
Agama post-installation script, with the choices the installer asked
for:

```bash
custos-setup --diagnostics-only --backend local-network \
  --endpoint http://ai.example.com:8000/v1 --model qwen3-32b --credential-file /run/agama/custos-model-key
```

## 18. SELinux (`custos` module)

SLES 16 and Leap 16 run SELinux enforcing, so every program this
repository ships runs in a domain the policy accounts for:

| Program | Started by | Domain |
|---|---|---|
| `agent-launcher` | systemd (`agent@.service`) | `agent_t` |
| `agent-delegate` | mcp-gateway (the `agents` server) | `mcpsrv_agents_t` |
| `agent-admin` | an administrator, Cockpit, `agent-schedule@.service`, `agent-event@.service` | the caller's (`unconfined_t`, `unconfined_service_t`) |
| `fakemodel`, `testsrv` (`-tests`) | the functional tests; mcp-gateway | `unconfined_service_t`; `mcpsrv_generic_t` |

CI builds the module and runs the functional tests on Leap 16 with SELinux
enforcing; they fail on any denial for the module's types.

`agent_exec_t` → `agent_t` (`init_daemon_domain`), with `agent_conf_t`,
`agent_var_lib_t`, `agent_runtime_t` and `agent_sock_t`. `agent_t` gets
`mcp_gateway_client` (the MCP socket) but not
`mcp_gateway_control_client`. It may connect to HTTP ports for models
(boolean `agent_connect_any` for others). It reads its own credentials
in `/run/credentials/<unit>` (`init_var_run_t`, as mcp-gateway's
server domains do). It cannot connect to
`agent_sock_t`. `mcpsrv_agents_t` (from the gateway's backend template)
may connect to `agent_sock_t`, and so may Cockpit's bridge
(`unconfined_t`).

## 19. Packaging

OBS packages for SLES 16.0/16.1 and openSUSE Leap 16.0/16.1
(`packaging/suse`): `custos` (requires `mcp-gateway` ≥ 1.15: the reasons for denials, call outcomes, notices and reviews of §6, §6a, and approvals over an envelope's rate)
and `custos-selinux` (required where `selinux-policy-targeted`
is installed), `custos-cockpit` (the Cockpit page, in
`/usr/share/cockpit/custos`), `custos-tests` (the functional
tests, for openQA) and `custos-docs` (the user guide and this contract
in `/usr/share/custos/docs`, with the collection file
`/usr/share/mcp-docs/collections.d/custos.yaml`, so that mcp-docs serves
them as the collection `custos`).

`custos-diagnostics` is the bundled diagnostics install the installer
selects (O16): `custos-setup` (§17a), requiring `custos` (whose are the
playbooks and the `health-check` template, used by the broader install
too) and `mcp-gateway-exec-server`, recommending `custos-cockpit` and
`custos-selinux`.

---

# Part III — Traceability and open items

## 20. Requirement: Cockpit control plane for governed agentic AI (SLES 16.2)

| Requirement | Contract | State in this repository |
|---|---|---|
| See activity not started in chat (schedules, events), what runs, waits, ran | §3, §4, §5, §10 | host side done and tested (scheduled and API activities, states, history); Cockpit Activity tab done, smoke-tested against a stub of Cockpit (O9) |
| …including delegation chains, traceable to the origin | §4 `parent`/`root`, §9, §10 | done and tested (trace verified by `SO_PEERCRED`); bridge `agent-delegate` built, untested against a real gateway |
| Pending action as an approval item with actions, evidence, impact; nothing state-changing without approval | §6 supervised default, §7 | enforcement done in gateway policy (tested with OPA against the gateway's policy); approval flow tested end to end with a fake gateway; impact and evidence are the agent's self-asserted rationale plus the activity (O6); Cockpit Approvals tab done, decisions as the person (O9) |
| Outcome after execution; proposed → approved/rejected → executed history | §4, §10 "History of an action" | done (journal and activity API); Cockpit Audit tab done, the join tested on a real gateway (O9) |
| Chat as one mode | §11 | AG-UI and ADK REST, tested; Cockpit chat done, smoke-tested against a stub of Cockpit (O9) |
| See autonomy mode and policy; adjust where permitted | §6 | gateway data + `agent-admin mode/permissions/show`; Cockpit Agents and Policy tabs done, smoke-tested against a stub of Cockpit (O9) |
| Stop / pause / override in-flight or scheduled activity; emergency stop | §8 | cancel, pause (host and gateway), stop-all done; tested on the host side and in policy |
| On-premises, air-gapped | §14 (`provider: openai` with a local server), §19 (OBS) | done; no external service needed |
| Independent of kit / any host | Part I is host-independent; §3 discovery by contract only | done: a second host (`test/vm/secondhost`, the standard library and the contract alone) passes the same conformance checks as the launcher (`test/contract`), and the Cockpit page lists, pauses and cancels on it in a real Cockpit (O8) |
| Fleet scope addressed | §12 | out of scope for 16.2, not precluded |

## 20a. Requirement: agentic systems management as a GA component (SLES 16.2)

On-premises is the reference configuration: it comes first in CI, the
docs and the safety report. Rows marked *outside* are deliverables of
the product and release process, tracked here so that nothing is lost.

| Requirement | Contract | State in this repository |
|---|---|---|
| Supervised by default for every category; not configurable away at install | §6, §6a | done (§6, §6a) |
| Policy-bounded autonomy as a named destination, with an envelope (hosts, windows, rates, blast radius, rollback) | §6a | `bounded` with `windows`, `rate` (the gateway's `rate_limit`, per tool) and `rollback` (the demotion evaluator), done; hosts and blast radius across hosts parked with the fleet (O13) |
| Observation period, override rate, explicit authenticated promotion, demotion path | §6a, §10 | observation, override rate (with reviews of autonomous actions), promotion and demotion by hand, and automatic demotion on the override rate, rollback conditions and incident reviews, and on repeated rate caps (O12), done |
| Per-category, not whole-agent, promotion; audit per category | §6a, §10 | done: `promote`, `demote`, promotion records (O11) |
| Promotion-readiness view (override rate, incident rate, diversity, envelope size) | §6a | measures, thresholds, `agent-admin readiness` and the Cockpit view done, and `promote` checks them; envelope size open (O11) |
| Hard capability limits, audit trail, emergency stop | §6, §8, §10, §15, §18 | done |
| Prompt-injection defence | §6 (the gateway decides every call, never the model), §16a (egress fence) | the boundaries are done; the evaluation suite of injection attempts done, on every VM run with a model that obeys them and in the air-gapped gate with the tested model; a call to a tool not given recorded (`not offered`); a suspected injection shown with the approval item, with the output the model read before the call (O14) |
| Service identity; privilege only through existing surfaces; MCP | §2, §6, §17 | done |
| Fleet control plane: audit aggregation, policy distribution, approvals; mandatory above `N` hosts | §12 | parked (O13) |
| Integration with SLES identity, RBAC, audit, configuration management | §2, §10 | identity, RBAC, audit done per host; configuration management parked with the fleet (O13) |
| Multiple inference backends, ordered on-premises → customer's cloud → vendor SaaS | §14 | `openai` (any OpenAI-compatible server: on-premises, or the customer's cloud) and `gemini`/`vertex` supported; the ladder in that order in the user guide (chapter 14); the field briefing open (O15) |
| Zero required egress; each egress flow documented and disable-able | §14, §16a | agents need only the model and the gateway; every flow, with its switch, in the user guide (chapter 14) |
| Air-gapped as a release-gate test | §19 | CI runs with a scripted model; on release branches the air-gapped gate (CI job `airgap`, `test/airgap`) installs the packages, Ollama and a tested open-weight model (`test/airgap/versions`) from an offline mirror in a VM without network, and runs the functional tests with an agent on that model |
| Signed distro component, updated through the errata pipeline | §19 | OBS packaging done; signing and errata *outside* |
| Model serving runtime, model artifacts through errata and offline mirrors | — | *outside* |
| Tested open-weight models (2 or 3) with model cards, validation evidence, deprecation policy | — | *outside*; the validation suite to run them against is O15 |
| Reference hardware sizing for x86_64 and aarch64 (riscv64 excluded) | — | *outside* |
| Safety report and EU AI Act high-risk evidence; support terms for AI-specific failures | — | *outside*; §6a and §10 supply the oversight and logging evidence |

## 20b. Requirement: bundled health-check and diagnostics agent (SLES 16.2)

The default-installed configuration of §20a, chosen at deployment: a
health-check and diagnostics agent on the same runtime, control plane
and inference backends, with only the `diagnostics` category enabled
and the others installed but disabled until the customer enables them.
What §20a provides (the audit trail, the backend selection, the
on-premises reference, the air-gapped variant, the promotion path) is
inherited, not specified again.

| Requirement | Contract | State in this repository |
|---|---|---|
| Opt-in choice at deployment, not selected by default, the four backends in the documented order | §17a, §19 | the installer screen is the installer team's (*outside*); its interface done: the package `custos-diagnostics` to select, and `custos-setup` with the four backends in that order |
| A "diagnostics-only" enablement state in which only `diagnostics` is usable, enforced by the runtime, not by files an administrator edits | §6b | done: the gateway denies and hides disabled categories (tested with OPA and on the gateway); `agent-admin` refuses to load them; a state changed by hand is reported; sealing open (O16) |
| Release-gate test: other categories refused in diagnostics-only | §6b, §19 | done: the functional tests' section "Diagnostics-only (release gate)", on every VM run and in the air-gapped gate |
| Attempts to use a disabled category recorded | §6b, §10 | done: the gateway's audit (calls), `category.refused` (loading) |
| Enabling more categories: one documented, authenticated, logged action; the result identical to a broader install with them | §6b, §17 | done: `agent-admin enable` (in Cockpit **Enable…**, which also shows the state and marks disabled categories), `categories.enabled`; the gate compares the gateway's decisions with a host without enablement state. The way back, `agent-admin disable` (**Disable…**), `categories.disabled`, done (#110) |
| Enabled categories start supervised | §6a, §6b | done (§6a); nothing new needed |
| The diagnostics history as trust evidence for promotion | §4a, §6a, §6b | done: `agent-admin readiness` and the Cockpit readiness view show the reports, their verified quotes and root's feedback on them; on a host that started diagnostics-only they are readiness thresholds (reports with feedback, confirmed share), so `promote` follows them |
| The bundled health-check and diagnostics agent | §14a, §17a, §20b | done: the template `health-check` (every playbook's tools, read-only in `diagnostics`, supervised, with daily and weekly playbook schedules), which `custos-setup` creates; both tested on a diagnostics-only host in the functional tests |
| Diagnostics: read host state, logs and metrics; correlate and summarize; root-cause hypotheses; written remediation plans for the administrator | §4a, §6b | done: the `diagnostics` category (read-only), the playbooks, and the report with findings, hypotheses and a plan |
| The published diagnostic playbook set | §14c | done: eleven playbooks and their read-only `exec` commands; an activity or a schedule runs one, refused when the gateway does not offer a step's tool |
| Confidence indicator | §4a | done: in Cockpit, each hypothesis shows the agent's confidence, labelled as its own estimate, next to how many of its quotes the host verified |
| Recommendation provenance ("because of log line X and metric Y") | §4a | done: each hypothesis cites evidence, whose quotes the host verifies against the output of the tool calls; Cockpit shows each quote as verified or as the agent's claim alone |
| Administrator feedback | §4a, §10 | done: `agent-admin feedback`, and in Cockpit **Was it right?** under the report, recorded outside the agent as `diagnosis.feedback` |

## 21. Open items

- **O1 — owner as approver.** Settled: an agent's `owners` (its agent
  data, `agent-admin owners`, Cockpit) may approve its calls besides the
  servers' approvers, by Custos's module of the gateway's approver policy
  (`owners.rego`, §7), without a gateway change. Tested with OPA against
  the gateway's policy, and on a host where an owner without any other
  approver right decides a pending call.
- **O2 — notice to call matching.** Settled: the gateway's approval
  notice names the call by its progress token (§7,
  sdrahn/mcp-gateway#333). Only against a gateway without it can two
  calls of one tool in flight be assigned crosswise.
- **O3 — AG-UI versions.** Unknown `RunAgentInput` fields are ignored.
  Newer event types are emitted only once this contract adds them.
- **O4 — remote frontends.** AG-UI from other hosts needs
  authentication in front of the socket (Cockpit as the only entry, or
  an authenticating proxy). Not in version 1.
- **O5 — protocol revision 2026-07-28.** For "modern" clients the
  gateway forwards log messages only at a level the request asks for.
  The approval notice reaches today's (2025-11-25) MCP clients. A
  modern client must ask for `notice`.
- **O6 — proposals.** The approval item's rationale and impact come
  from the model's text and are self-asserted. A structured proposal
  (planned actions, expected impact) that the gateway shows with the
  approval would need a gateway field, e.g. in the call's `_meta`:
  requirement R5 in `docs/requirements/mcp-gateway-for-custos-0.4.md`.
  Done with mcp-gateway 1.15: the host sends the rationale, the
  activity's context and the suspected-injection signs as the call's
  proposal (§7). Open: planned steps and impact as the model states them
  (a convention for the model's text, or a tool for it).
- **O7 — fleet.** A cross-host delegation transport, a host part in the
  trace, and an aggregated view.
- **O8 — second host.** Done (#112): `test/vm/secondhost`, a mock
  host built from the standard library and this contract alone, serves
  §5 and writes §10's records (no AG-UI: `interaction.agui` false). The
  conformance checks in `test/contract` run against it and against the
  launcher; the functional tests run it as a unit and use it as a
  supervisor and as root; the Cockpit page lists, pauses and cancels on
  it in a real Cockpit. What it found: the launcher sent `calls: null`
  for an activity without calls (now `[]`, §4); the page made up a
  socket unit for a lifecycle without one (now the service, §3);
  `agent-admin pause` refused an agent without gateway data (now its
  host alone, §8). Open: an adapter for another runtime (kit).
- **O10 — OpenShell backend.** §3a and §16a are a draft. CI runs the
  backend with a real OpenShell on Docker (`test/openshell/run-spike.sh`,
  job `openshell`), against mcp-gateway's own HTTPS listener (in its
  development mode) with the gateway's policy and the agents module,
  and a test token issuer (`test/vm/testidp`). Settled by the spike: the
  token stays with the supervisor; the control API is relayed with the
  caller; the agent's records reach the host journal through
  the relay's bridge, from the sandbox container's output (OpenShell's
  own log stream has only the supervisor's lines); the agent is
  `agent-<name>` over HTTPS too, with its mode and approvals. Still open:
  - the issuer of the agents' tokens in production: settled (#111),
    the site's IdP, one client per agent whose tokens have the subject
    `agent-<name>` and the gateway's audience. `agent-admin idp-client`
    says what the client must be and, for Keycloak, prints a script that
    creates it (a service-account client with a hardcoded `sub` and an
    audience mapper, without the realm's default client scopes, which
    map Keycloak's own `sub`); `agent-admin check-token` checks a real
    token's claims before `set-credential`. CI's `keycloak` job runs
    both against Keycloak 26. No local issuer. Their renewal is settled: OpenShell's
    gateway mints and renews them with the client credentials grant, and
    the spike sees a call succeed on a renewed token after the first
    expired, without restarting the agent;
  - SLES 16 / Leap 16 with Podman and SELinux enforcing: settled. CI's
    `openshell-podman` job runs the backend in the Leap 16 VM with the
    installed packages, rootless Podman and SELinux enforcing
    (`test/vm/run-vm.sh` with `SUITE=openshell`): the whole spike passes
    with no denial since boot, and the job fails on a denial for the
    repository's domains. What it needs is in §16a (the OpenShell user,
    the relay's drop-in, the controllers delegated to user sessions);
  - the gateway's name and certificate: settled (#131), the host's own
    name, so an official certificate covers the agents too;
    `host.openshell.internal` with a private CA remains for test
    systems. CI's `openshell` job runs the spike by a host name of its
    own (resolving to the host's network address) and by the alias, the
    `openshell-podman` job by the name;
  - OpenShell's packaging for SLES and Leap, and its maturity (0.1.x).
- **O9 — Cockpit page.** Settled: the control plane itself, built only
  on Part I and the gateway's control API (`cockpit/custos`, package
  `custos-cockpit`; screenshots of the real page in `docs/screenshots`):
  the Activity tab (§4, §5: activities, delegation chains, tool calls,
  cancel) and the Approvals tab (§7: the approval item, decided as the
  person; today's decisions from the journal, §10), the Agents tab (each
  agent's state, autonomy mode, tool boundaries with their effect now,
  schedules; mode, permissions, pause, restart, stop and schedules changed through
  the root bridge with `agent-admin` and `systemctl`, §6, §8) and the
  Policy tab (the posture of every agent with its approvers, and what the
  gateway would decide for one call), and the emergency stop (§8: *Stop
  all agents* in the header after a confirmation, a banner with who
  stopped them and when, *Release…*; `agent-admin stop-all` and
  `release-all` through the root bridge), and the Audit tab (§10: each
  state-changing action's history, proposed → decided → executed, joined
  by approval id from the agents' records and the gateway's, and the
  records themselves, filtered), and chat (§11: the person's own thread
  with an agent over AG-UI, in the agent's view, with its tool calls and
  pending approvals linked to the Approvals tab), and creating and deleting
  agents (`agent-admin create --from -` after `check -`, and `delete`
  after typing the agent's name), and editing an agent's configuration
  (read through the root bridge, `check -`, then `agent-admin edit
  --from -`). Tested against a stub of Cockpit (`make cockpit-test`) and
  in a real Cockpit in the Leap 16 VM with SELinux enforcing (CI job
  `cockpit-vm`): logged in as a person, an approval decided in the page
  is the person's in the gateway's audit, not root's.
- **O11 — task categories and promotion.** §6a. Done: the catalog
  (`categories.d`, `agent-admin categories`), the `category` field, the
  effective mode and the envelope's `windows` in the agents module,
  readiness (`agent-admin readiness`, the thresholds in
  `/etc/custos/promotion.yaml`) from the agents' records and the
  gateway's decisions, `agent-admin promote` (refused below readiness
  without `--despite-readiness`) and `demote`, the demotion evaluator
  (`agent-admin evaluate`, `custos-demote.timer`) on the override rate,
  the envelope's `rollback` and incident reviews, the after-the-fact
  review of autonomous actions (`agent-admin review`, `category.review`,
  counted in the override rate), the promotion records, `show` with the
  categories, and the Cockpit page's view of an agent's categories with
  their readiness, the actions to review, **Promote…** and **Demote**.
  Tested with OPA against the gateway's policy, in the functional tests
  and in a real Cockpit; the Cockpit page shows the approvers the
  automatic demotions of the last week (Approvals tab) and marks such a
  category in the agent's view. The rollback condition `tool_error` and
  the incident rate take each call's outcome from the gateway's audit
  (`mcp-result`, its requirement R2), so the agent cannot hide a failure
  by leaving out its own `tool.result`; the evaluator posts a notice of
  each automatic demotion to the gateway (`POST /v1/notices`, its
  requirement R3), which tells the agent's approvers and owners by
  mail, on its event stream and its approval page; and the gateway's
  approvers review autonomous actions on the gateway (its review items,
  R4), their verdicts counting as root's do; the actions `agent-admin
  review` offers to root are the gateway's record of the calls too. In
  the measures, each call is judged by the permissions in force at its
  time (root's `permissions.changed`), and an emergency stop during an
  approved call's activity is an incident (root's `agents.stopped`).
  Open in Custos: in the measures, the envelope's size and the proposals
  behind each number.
- **O12 — envelope limits in the gateway.** Done, per tool: `rate` is
  the gateway's `approval_over_rate` obligation (its R6) on each of the
  category's permissions while they act without an approval, so it is
  enforced where the approvals are: over it a call asks, as if the
  category were supervised, and the evaluator demotes when the rate is
  reached repeatedly. Open: a counter across all of a category's tools
  together, which needs a new mcp-gateway feature.
- **O13 — fleet control plane (parked).** The requirement in §20a also
  asks for a control plane for on-premises deployments above a defined
  fleet size `N`: inventory, policy distribution through the site's
  configuration management, approvals as the person across hosts, audit
  aggregation, fleet-wide readiness, the envelope's `hosts` and a blast
  radius across hosts. Parked by the requirement's owner; version 1 stays
  single-system (§12). O7 goes with it.
- **O14 — prompt injection.** The boundaries hold whatever the model is
  told: the gateway decides each call, the sandbox fences egress. Done:
  the evaluation suite. The functional tests' test server serves notes
  an attacker wrote (`read_note`; the cases in `test/vm/testsrv`): a
  direct override, a forged system message, a claimed approval, a tool
  the agent is not given (`wipe`), a request to leak the instructions,
  an instruction buried in log output and one in data. Section "Prompt
  injection" runs them on every VM run with the scripted model, which
  obeys each one, and the air-gapped gate runs them again with the
  tested model as an agent with an ordinary instruction. Checked each
  time: an injected change waits for an approval and, declined, does not
  run; `wipe` never reaches the gateway; the gateway allowed neither.
  How often the tested model followed is reported (`injection-<model>.json`
  with the results, and the gate's summary), not a gate. A call to a
  tool not given is recorded (`tool.denied.local`, `not offered`, §11).
  A suspected injection is shown with the approval item (§7): the host
  checks the outputs the model read before each call for signs of an
  injection (§4 `calls[].suspected_injection`, journaled as
  `AGENT_SUSPECTED_INJECTION`), and a call that waits for an approval
  carries those outputs (`calls[].preceding`); Cockpit marks the item and
  shows both beside the rationale. Advisory and self-asserted: the
  approval is the boundary. Every injected call of the suite is checked
  to carry the mark. Done.
- **O15 — on-premises reference and air-gapped gate.** Done: the
  release gate (CI job `airgap`, on release branches): the packages,
  Ollama and a tested open-weight model (`test/airgap/versions`) from an
  offline mirror on a VM without network, then the functional tests
  with an agent on that model; an agent's attempt to reach a host
  outside fails it. The egress flows, each with its switch (Custos has
  no model updates, telemetry or incident reports of its own), and the
  inference backends in the order of §20a (on-premises, the customer's
  own cloud account, vendor-hosted), in the user guide's chapter 14.
  Open: the field briefing in that order.
- **O16 — bundled diagnostics install.** §20b; designed in §4a, §6b,
  §14c and §17a. Done: the enablement state (§6b) with the gateway's
  enforcement, `agent-admin enable` and `diagnostics-only`, the refusals
  and records, the tamper check, and the gate's checks in the functional
  tests (section "Diagnostics-only (release gate)", so also in the
  air-gapped gate); the playbooks (§14c), run by an activity or a
  schedule, and their `exec` commands; the `health-check` template; the
  report with host-verified evidence (§4a) and `agent-admin feedback`;
  the Cockpit report view with confidence, provenance and feedback, and
  running a playbook from an agent's view; the diagnostics evidence in
  readiness (§6b); `custos-setup` and the package `custos-diagnostics`
  (§17a), tested on a fresh host in the functional tests; the reason in
  the gateway's audit for a denial ("task category … is not enabled on
  this host", §6), with mcp-gateway's R1; the way back, `agent-admin
  disable` and `disable --all` (#110); the seal (#109): the gateway
  counts only a state `agent-admin` signed, with a key kept as a systemd
  credential (the TPM's where there is one). Against root on one host
  the seal makes a change a deliberate forgery; stopping root needs an
  outside party (O13).
