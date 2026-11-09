---
title: "10. Monitoring agents and reading the audit record"
description: The control API on each agent's socket, the agents' journal records and their fields, the gateway's audit records, following one action from proposal to execution, retention, and SELinux denials.
tags: [monitoring, audit, journal, journalctl, control api, history]
---

# 10. Monitoring agents and reading the audit record

Everything an agent does is in two places: its control API, for what
runs now and the recent past, and the journal, for the full history,
next to mcp-gateway's own audit. The Cockpit page reads the same
sources ([chapter 8](08-cockpit.md)). The exact fields are in the
contract (`CONTRACT.md` §4, §5 and §10).

## The control API on an agent's socket {#control-api}

Each agent serves HTTP on `/run/agents/<name>/api.sock`, reachable by
root and members of `agent-clients`. The caller is identified by the
socket's peer credentials.

| Request | Answers |
|---|---|
| `GET /v1/status` | the agent: contract version, lifecycle (units or sandbox), model, paused, the gateway and whether it is reachable, running activities, calls in flight, limits, interaction, schedules |
| `GET /v1/activities` | running activities, then the last ended ones (at least 200), newest first |
| `GET /v1/activities/{id}` | one activity with its calls |
| `POST /v1/activities` | start one: `{"prompt": "…", "wait": true}`; `409` while paused |
| `POST /v1/activities/{id}/cancel` | end a running one |
| `POST /v1/pause`, `POST /v1/resume` | root only; `agent-admin pause` uses them |

```bash
api() { curl -s --unix-socket /run/agents/$1/api.sock "http://agent$2"; }
api patch-pilot /v1/status | jq '{paused, running, gateway, model}'
api patch-pilot /v1/activities | jq -r '.activities[] | [.id, .state, .origin.kind, .summary] | @tsv'
```

Asking the socket starts an idle agent (socket activation). The answer
waits for that start.

## The agents' journal records {#journal}

Each agent writes records with `SYSLOG_IDENTIFIER=agent-<name>` and
fields named `AGENT_*`:

| Field | Value |
|---|---|
| `AGENT_CONTRACT`, `AGENT_NAME`, `AGENT_EVENT` | the contract version (`1`, on every record), the agent; the event (below) |
| `AGENT_INVOCATION`, `AGENT_SESSION`, `AGENT_USER` | the activity id, the session, for whom it runs |
| `AGENT_ORIGIN`, `AGENT_ORIGIN_BY`, `AGENT_SCHEDULE` | how it started (`chat`, `schedule`, `event`, `delegation`, `api`) |
| `AGENT_PARENT_AGENT`, `AGENT_PARENT_ACTIVITY`, `AGENT_ROOT_AGENT`, `AGENT_ROOT_ACTIVITY` | the delegation chain |
| `AGENT_TOOL`, `AGENT_SERVER`, `AGENT_CALL_ID`, `AGENT_ARGS_SHA256` | a tool call; arguments only as their SHA-256 |
| `AGENT_RATIONALE` | the model's stated reason with a call (self-asserted) |
| `AGENT_INJECTION_SIGNS` | signs of a prompt injection in a tool's output, which reached the model with the host's notice (`tool.result`; [chapter 1](01-overview.md#prompt-injection)) |
| `AGENT_SUSPECTED_INJECTION` | signs of a prompt injection in what the model read before a call (`tool.call`, `approval.pending`; [chapter 7](07-approvals.md#cockpit)) |
| `AGENT_APPROVAL_ID`, `AGENT_OUTCOME`, `AGENT_REASON` | approvals, results and why |
| `AGENT_MODEL`, `AGENT_TOKENS_IN`, `AGENT_TOKENS_OUT`, `AGENT_DURATION_MS` | a model call: the model, the tokens it reports, how long it took |
| `AGENT_PROMPT_BYTES` | a model call that failed (`AGENT_OUTCOME=error`, the error as `AGENT_REASON`): the size of what was sent |
| `AGENT_DURATION_MS`, `AGENT_LLM_CALLS`, `AGENT_TOOL_CALLS` | at the end of an activity: how long it took and how many calls it made |

Events: `agent.start`, `agent.stop`, `config.invalid`, `agent.paused`,
`agent.resumed`, `invocation.start`, `invocation.end`,
`activity.cancel`, `model.call`, `tool.call`, `tool.result`,
`tool.denied.local`, `approval.pending`, `approval.resolved`,
`limit.exceeded`, and the promotion records below. Each has its own `MESSAGE_ID` (`CONTRACT.md` §10), so
`journalctl MESSAGE_ID=…` finds one kind across agents.

```bash
journalctl -u agent@patch-pilot.service --since today            # one agent: its unit's messages and its records
journalctl -t agent-patch-pilot -f                               # its records only, as they come
journalctl -o json AGENT_CONTRACT=1 AGENT_EVENT=approval.pending  # every agent's pending approvals
journalctl -o json AGENT_NAME=patch-pilot AGENT_INVOCATION=e-0daa8a73-… | jq -r '[.AGENT_EVENT, .MESSAGE] | @tsv'
```

Arguments never appear in the agents' records. The gateway shows them
with a pending approval to those who may decide, and logs them as its
policy says.

### Calls to tools the agent was not given {#not-offered}

The gateway offers an agent only the tools it may use. A model that asks
for another one anyway has usually followed an instruction in something
it read (a prompt injection, [chapter 1](01-overview.md#prompt-injection)),
or made the tool up. The launcher refuses the call, tells the model so,
and records `tool.denied.local` with `AGENT_REASON=not offered`, the
tool and the model's stated reason. The call never reaches the gateway.

```bash
journalctl -o json AGENT_CONTRACT=1 AGENT_EVENT=tool.denied.local AGENT_REASON="not offered" |
  jq -r '[.__REALTIME_TIMESTAMP, .AGENT_NAME, .AGENT_TOOL, .AGENT_RATIONALE] | @tsv'
```

Look at what the agent read just before: the calls before it in the
same activity (`AGENT_INVOCATION`), in the journal or in the activity in
Cockpit's agent view.

## Promotion records {#promotion-records}

`agent-admin promote` and `demote`, the demotion evaluator and the
review write a record for each change of a task category's mode, and
for each verdict ([chapter 6](06-permissions-and-modes.md#promote)),
with `SYSLOG_IDENTIFIER=custos-promotion`:

| Field | Value |
|---|---|
| `AGENT_EVENT` | `category.promoted`, `category.demoted`, `category.review` (a verdict on an action, [chapter 6](06-permissions-and-modes.md#review)); and those below |
| `AGENT_NAME`, `AGENT_CATEGORY` | the agent, the category |
| `AGENT_MODE_FROM`, `AGENT_MODE_TO` | `supervised`, `bounded` |
| `AGENT_BY`, `AGENT_REASON` | who (the person behind `sudo`, or `custos-demote` for an automatic demotion) and why (for `custos-demote`, the condition that fired) |
| `AGENT_READINESS` | with a promotion: the category's readiness measures at that moment, as JSON |
| `AGENT_PROMOTION` | the promotion's id (`p-…`), also in the agent's gateway data |
| `AGENT_ENVELOPE_SHA256` | the envelope's digest |
| `AGENT_VERDICT`, `AGENT_INVOCATION`, `AGENT_CALL_ID`, `AGENT_TOOL` | a review: `ok` or `incident`, and the action |

It also records, with the same identifier, what readiness needs to judge
the past ([chapter 6](06-permissions-and-modes.md#readiness)):

| `AGENT_EVENT` | When, and its fields |
|---|---|
| `permissions.changed` | each change of an agent's permissions (`agent-admin permissions`, `create --template`): `AGENT_PERMISSIONS_FROM`, `AGENT_PERMISSIONS_TO` (JSON), `AGENT_BY` |
| `agents.stopped`, `agents.released` | `stop-all` and `release-all`: `AGENT_BY`, with no `AGENT_NAME` (they concern every agent) |

Readiness takes these, as the reviews, only as root wrote them
(journald's `_UID=0`, and not an OpenShell relay's `_COMM` or unit): an
agent can write records of its own, never root's. An OpenShell agent's
records reach the journal through its relay, which runs as root; the
relay passes on only the events an agent writes, never these.

```bash
journalctl -t custos-promotion -o json | jq -r '[.__REALTIME_TIMESTAMP, .AGENT_EVENT, .AGENT_NAME, .AGENT_CATEGORY, .AGENT_BY, .AGENT_REASON] | @tsv'
```

## The gateway's audit records {#gateway-audit}

mcp-gateway logs each decision as a JSON line with `"audit": true` in
the journal of `mcp-gateway.service`: the principal (`sub`), the server
and tool (`server`, `name`), the effect (`allow`, `deny`, `ask`) and
why. Approval decisions are records with `"event": "mcp-approval"`: the
approval `id`, `ok`, the approver `by`, `scope` and `via`.

```bash
journalctl -u mcp-gateway.service -o cat --since today | grep '"audit":true' | jq -c 'select(.sub | startswith("agent-"))'
journalctl -u mcp-gateway.service -o cat | grep '"event":"mcp-approval"' | jq -c '{id, ok, by, scope, via, principal}'
```

Denials and approval decisions also go to the kernel audit subsystem
(`op=mcp-approval`, `op=mcp-decision`), where they can be matched with
SELinux records: `ausearch -i -ts today | grep 'op=mcp-approval'`.

## Following one action from proposal to execution {#history}

An action's history is joined by the approval id and the call id:

1. `tool.call`: the agent proposes it, with its rationale (`AGENT_CALL_ID`).
2. `approval.pending`: the gateway holds it (`AGENT_APPROVAL_ID`).
3. the gateway's `mcp-approval`: who decided, how.
4. `approval.resolved`: the agent sees the outcome.
5. `tool.result`: it ran (`ok`, `error`) or not.

The *Audit* tab in Cockpit shows exactly this
([chapter 8](08-cockpit.md#audit)). On the command line, for one
approval id:

```bash
id=a-6c88df4c32d09c66eb46bc8941b53745
journalctl -o json AGENT_APPROVAL_ID=$id | jq -r '[.AGENT_EVENT, .AGENT_OUTCOME // ""] | @tsv'
journalctl -u mcp-gateway.service -o cat | grep "\"id\":\"$id\""
```

## How long the record is kept {#retention}

The journal is the record; its retention is journald's (`SystemMaxUse=`
and `MaxRetentionSec=` in `journald.conf`). Forward it to a central log
system as you would other audit-relevant logs. The agents keep at least
the last 200 ended activities for the control API, and chat sessions
for `sessions.retention_days` (30 by default).

## SELinux denials {#selinux-denials}

An agent's `Permission denied` is often SELinux. List the denials of
the domains Custos ships:

```bash
ausearch -m AVC,USER_AVC -ts today | grep -E 'scontext=[^ ]*:(agent_t|agent_relay_t|mcpsrv_agents_t):'
```

[Chapter 12](12-troubleshooting.md#selinux) has the usual causes.
