---
title: "3. Connecting Custos to mcp-gateway"
description: How agents reach mcp-gateway, the policy module and the agents' data, registering the MCP servers agents use, approver rules, delegation, and checking the connection.
tags: [mcp-gateway, gateway, servers, approvers, delegation, policy]
---

# 3. Connecting Custos to mcp-gateway

mcp-gateway is where an agent's tools come from and where its rights are
decided. This chapter sets up the gateway side for agents on the same
host: the servers they use, who approves their calls, and delegation
between agents. Agents in OpenShell sandboxes reach the gateway over
HTTPS instead; that is [chapter 11](11-openshell.md).

## How an agent reaches the gateway {#socket}

An agent with the default `systemd` backend connects to the gateway's
MCP socket `/run/mcp-gateway/mcp.sock` (`gateway.socket` in its
configuration). Its unit runs it as `agent-<name>` with the supplementary
group `mcp-users`, the group that owns that socket. The gateway
identifies it by the socket's peer credentials (`SO_PEERCRED`) as the
principal `agent-<name>`, transport `unix`; nothing the agent says about
itself changes that. The agent announces itself as the MCP client
`custos/<name>`.

The agent's unit hides the gateway's control socket
(`/run/mcp-gateway/control.sock`) and SELinux keeps `agent_t` from it,
so an agent can never decide an approval.

## The policy module and the agents' data {#agent-data}

The package installs `/usr/share/mcp-gateway/policy/custos/agents.rego`,
a module of the gateway's policy package `mcp.authz`. mcp-gateway's
policy engine (OPA, `mcp-opa.service`) loads it with the gateway's
shipped policy. It reads each agent's data from
`/etc/mcp-gateway/policy/agents/<name>.yaml`:

```yaml
patch-pilot:
  mode: supervised
  paused: false
  permissions:
    - {server: zypp, tool: check_updates, read_only: true}
    - {server: zypp, tool: confirm_install, category: patch-errata}
  categories:                 # once a category is promoted (chapter 6)
    patch-errata:
      mode: bounded
      envelope: {windows: [{days: [sat], from: '02:00', to: '05:00', tz: Europe/Berlin}]}
      promoted_by: "alice 2026-11-07T09:00:00.123456789Z"
      promotion: p-3f9c…
```

`agent-admin` writes these files; do not edit them by hand. OPA picks up
a change within seconds, and `agent-admin` waits (up to 20 seconds) until
OPA has it before it returns, so a change is in force when the command
ends. If mcp-gateway's OPA is not running at all, `agent-admin` says so
and returns: the change is in force once OPA starts. The same directory holds `_control.yaml` (the emergency stop),
`_enablement.yaml` and `_seal.yaml` (the task categories' state and its
seal, [chapter 6](06-permissions-and-modes.md#diagnostics-only)) and
`_http.yaml` (the token issuers of agents over HTTPS). OPA watches only
directories it loaded a file from at start, which is why all of them
live in this one directory.

For an agent principal, the agent's data is the only source of rights:

- an `agent-*` principal without data gets nothing;
- a gateway role bound to an agent's user (in the gateway's role data)
  would bypass its mode, so the module denies that agent everything.
  Never bind roles to `agent-*` users or to `agent-clients`.

## Registering the MCP servers agents use {#servers}

An agent can only call tools of servers the gateway has. Register them
as for any client, usually with mcp-gateway's setup packages, which
install the server definitions:

```bash
zypper install mcp-gateway-profile-zypp mcp-gateway-profile-systemd
systemctl reload mcp-gateway.service
curl -s --unix-socket /run/mcp-gateway/control.sock http://localhost/v1/servers | grep -o '"name":"[^"]*"'
```

The agent templates need more servers: mcp-server-snapper,
firewalld-mcp, mcp-server-tukit on transactional systems, and
mcp-gateway-exec-server with the command in
`/usr/share/custos/templates/exec.d-disk-usage.yaml`. Each template's
`needs` lists its own ([chapter 9](09-administration.md#templates)).

The setup packages also ship roles (`zypp-reader`, `systemd-operator`,
…) for people. Agents do not use them: bind those roles to people only.
What an agent may call comes from its permissions
([chapter 6](06-permissions-and-modes.md)).

Two lists decide what reaches an agent:

| Where | Decides |
|---|---|
| `gateway.servers` (and `gateway.tools`) in the agent's `agent.yaml` | which tools the model is *shown* |
| the agent's permissions (`agent-admin permissions`) | what the gateway *allows* |

A tool the agent may not call is also left out of the tool list the
gateway gives the agent, so the model does not see it.

## Who may approve an agent's calls {#approvers}

The gateway's approver rules say who may decide a server's approvals.
They are part of the gateway's role data, `/etc/mcp-gateway/policy/rbac/data.yaml`
(until you write one, the shipped default `/usr/share/mcp-gateway/rbac/data.yaml`):

```yaml
# /etc/mcp-gateway/policy/rbac/data.yaml (excerpt)
approvers:
  default: [self, 'role:admin']
  zypp: ['role:admin', 'group:agent-approvers']
  systemd: ['role:admin', 'group:agent-approvers']
```

- In the shipped default, `wheel` is bound to the role `admin`, so
  members of `wheel` may approve everything.
- `self` (a user approving their own request) never applies to agents:
  agent users have no password. Name approvers explicitly for every
  server agents use.
- Start from a copy of the shipped default so its roles and bindings
  stay: `cp /usr/share/mcp-gateway/rbac/data.yaml /etc/mcp-gateway/policy/rbac/data.yaml`.
- Check what is in force with `mcp-gateway-admin policy show`.

## Links to the Custos page in approval notices {#approval-links}

The gateway names a page where an approval is decided: in the notice
it sends the agent ("Waiting for approval of … at <URL> …") and in its
notifications to approvers (desktop notification, mail, webhook; the
gateway's `oob` channel). To send people to the Custos page, set its URL
template in the gateway's configuration (a drop-in in
`/etc/mcp-gateway/gateway.d/`):

```yaml
# /etc/mcp-gateway/gateway.d/40-custos.yaml
approvals:
  url_template: https://host.example.com:9090/custos#/approvals/{id}
```

Then `systemctl reload mcp-gateway.service`. Inside the Custos page,
activities and the chat link to the Approvals tab themselves, whatever
the template says.

## Delegation between agents {#delegation}

Agents delegate tasks to each other through the gateway, as the tool
`agents__delegate`. The package ships the server definition; enable it
by copying it:

```bash
cp /usr/share/custos/gateway/servers.d/agents.yaml /etc/mcp-gateway/servers.d/agents.yaml
systemctl reload mcp-gateway.service
```

The gateway runs the bridge `agent-delegate` as the system account
`custos-delegate` (SELinux `mcpsrv_agents_t`) for every delegating
agent, and tells it which agent that is (mcp-gateway 1.17.1). This
works for agents under systemd and in OpenShell alike, in both
directions. It posts the task to the other agent's socket, naming the
delegating agent, and waits for the result. The other agent believes
that name only from `custos-delegate`. A copy of `agents.yaml` from
before 0.9.17 runs the bridge as the delegating agent's user, which an
agent in OpenShell does not have: copy the file again after the update.
Allow it per agent, naming whom it may delegate to:

```yaml
- {server: agents, tool: delegate, read_only: true, args: {agent: '^patch-pilot$'}}
```

The delegating agent gets the other agent's full last response (up to
32 KiB), its diagnosis report if it wrote one, and the activity's state,
so it can judge whether the task was done. An agent from before 0.4.2
gives only its 500-character summary. The callee works under its own
mode, permissions and approvals: a delegation never lends one agent's
rights to another. In supervised mode
the delegation itself needs an approval unless the permission says
`read_only: true`. The Activity tab shows the chain from the first
activity to the last ([chapter 8](08-cockpit.md#activity)).

## Checking the connection {#check-connection}

After creating an agent ([chapter 4](04-first-agent.md)):

```bash
curl -s --unix-socket /run/agents/patch-pilot/api.sock http://agent/v1/status | jq .gateway
# {"socket": "/run/mcp-gateway/mcp.sock", "reachable": true}
journalctl -u mcp-gateway.service -o cat --since -10min | grep '"sub":"agent-patch-pilot"'
```

`reachable: false` or no gateway records: see
[chapter 12](12-troubleshooting.md#gateway).
