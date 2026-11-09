---
title: "9. Administering agents"
description: Day-to-day administration with agent-admin and systemd - creating, editing and deleting agents, credentials, pause and resume, stopping and starting, schedules, cancelling activities, the emergency stop, the units' sandbox, and backups.
tags: [administration, agent-admin, pause, stop, schedules, emergency stop, backup, credentials]
---

# 9. Administering agents

Administration is done as root, with `agent-admin` and `systemctl`, or
in Cockpit with administrative access ([chapter 8](08-cockpit.md#agents)),
which runs the same commands. `agent-admin` records who acted: the
person behind `sudo` (`SUDO_USER`), else `USER`.

## agent-admin at a glance {#agent-admin}

| Command | Does |
|---|---|
| `agent-admin list` | the agents (those with a configuration) |
| `agent-admin show [NAME…]` | their gateway data and the emergency stop, as JSON (no root needed) |
| `agent-admin check NAME\|FILE` | validate a configuration (a FILE needs no root; `-` is stdin) |
| `agent-admin create NAME --from FILE` | create an agent: supervised, no permissions |
| `agent-admin create NAME --template TEMPLATE [--from FILE]` | create an agent from a template, with its permissions and mode ([below](#templates)) |
| `agent-admin templates` | the agent templates, as JSON (no root needed) |
| `agent-admin playbooks` | the diagnostic playbooks, as JSON (no root needed; [below](#playbooks)) |
| `agent-admin feedback NAME ACTIVITY [--hypothesis ID=VERDICT…] [--plan useful\|not_useful] [--executed] [--comment TEXT]` | your feedback on a diagnosis report ([below](#reports)) |
| `agent-admin draft NAME [--template TEMPLATE]` | a new agent's starting configuration, with the model defaults (no root needed) |
| `agent-admin defaults [--from FILE \| --clear]` | the model for new agents ([chapter 5](05-configuration.md#model-defaults)) |
| `agent-admin edit NAME --from FILE` | replace its configuration and apply it |
| `agent-admin delete NAME [--keep-state]` | remove an agent |
| `agent-admin mode NAME MODE` | `advisory`, `supervised` or `bounded` |
| `agent-admin owners NAME [RULE…]` | who may approve its calls besides the servers' approvers ([chapter 7](07-approvals.md#who-may-decide)) |
| `agent-admin permissions NAME < list.yaml` | replace its permissions |
| `agent-admin categories` | the task categories, as JSON (no root needed) |
| `agent-admin review NAME [CATEGORY]`, `agent-admin review NAME CATEGORY ACTIVITY CALL ok\|incident [--reason TEXT]` | the actions of promoted categories to review, and a verdict ([chapter 6](06-permissions-and-modes.md#review)) |
| `agent-admin evaluate` | the demotion evaluator, as `custos-demote.timer` runs it ([chapter 6](06-permissions-and-modes.md#auto-demote)) |
| `agent-admin readiness NAME [CATEGORY]` | whether a category is ready to promote, as JSON ([chapter 6](06-permissions-and-modes.md#readiness)) |
| `agent-admin promote NAME CATEGORY --envelope FILE [--despite-readiness --reason TEXT]` | let a category act without approval within its windows ([chapter 6](06-permissions-and-modes.md#promote)) |
| `agent-admin demote NAME CATEGORY [--reason TEXT]` | back to supervised |
| `agent-admin enable CATEGORY… \| --all` | enable task categories on a diagnostics-only host ([chapter 6](06-permissions-and-modes.md#diagnostics-only)) |
| `agent-admin disable CATEGORY… \| --all` | disable task categories again; `--all`: back to diagnostics-only ([chapter 6](06-permissions-and-modes.md#diagnostics-only)) |
| `agent-admin diagnostics-only` | make a freshly installed host diagnostics-only (the installer's setup runs it) |
| `agent-admin seal --agents` | make a host sealed before 0.6 seal the agents' data and token issuers too ([chapter 6](06-permissions-and-modes.md#diagnostics-only)) |
| `agent-admin issuers [--add ISSUER] [--remove ISSUER]` | the token issuers trusted for agents over HTTPS ([chapter 11](11-openshell.md#issuers)) |
| `agent-admin set-credential NAME CRED < secret` | store a secret for it |
| `agent-admin idp-client NAME [--keycloak]` | the OAuth client an OpenShell agent needs at the identity provider; `--keycloak`: a script that creates it ([chapter 11](11-openshell.md#idp)) |
| `agent-admin check-token NAME < secret` | get a token with that client and check its claims as the gateway will ([chapter 11](11-openshell.md#keycloak)) |
| `agent-admin credentials [NAME…]` | its credentials' names, what uses them and the missing ones, as JSON; never the values ([below](#credentials)) |
| `agent-admin remove-credential NAME CRED [--force]` | remove a stored credential (`--force`: one its model uses) |
| `agent-admin pause NAME`, `resume NAME` | hold it, let it go on |
| `agent-admin stop-all`, `release-all` | the emergency stop and its end |
| `agent-admin trigger NAME_SCHEDULE` | start a schedule's run (what its timer runs) |

`create`, `mode`, `permissions`, `promote`, `demote`, `pause`, `resume`, `stop-all` and `release-all`
return once the gateway's policy engine has the change, so it is in
force when the command ends. If the engine does not load it within 20
seconds, the command says so and fails. If mcp-gateway's OPA is not
running at all, it says so and succeeds: the change is in force once
OPA starts
([chapter 12](12-troubleshooting.md#policy-not-loaded)). Its standard
output is its own messages; what the programs it runs print goes to
standard error.

## Creating an agent {#create}

```bash
agent-admin create NAME --from agent.yaml
agent-admin create NAME --from - < agent.yaml
```

`create` validates the configuration and creates the system user
`agent-<name>` (`/etc/sysusers.d/agent-<name>.conf`, `systemd-sysusers`).
It installs `/etc/custos/agents/<name>/agent.yaml` and writes the gateway
data (supervised, no permissions) and the schedules' timers. Then it
enables `agent@<name>.socket` and the timers. Next, give the agent
permissions ([chapter 6](06-permissions-and-modes.md)) and, if its model
needs one, a credential. In Cockpit, *Agents*, **Create agent**: a name,
the configuration, **Check**, **Create**. To start from a shipped
template instead, with its permissions, see [below](#templates).

## Creating an agent from a template {#templates}

Custos ships templates for agents that look after one system. Each is
a configuration, the permissions it needs with the tool names of the
gateway's servers, and, where it has task categories, a suggested
envelope for promoting them. Templates are in
`/usr/share/custos/templates/<template>/`:

| Template | Systems | Mode | Does | Changes, and what they need |
|---|---|---|---|---|
| `health-check` | any | supervised | the bundled diagnostics agent: runs the diagnostic playbooks ([below](#playbooks)) daily (failed units, disk pressure, journal errors) and weekly (update status), and any of them on request; reports findings, likely causes with its confidence and the output they rest on, and a remediation plan for you | none: read-only permissions in `diagnostics`, so it also runs on a diagnostics-only host ([chapter 6](06-permissions-and-modes.md#diagnostics-only)) |
| `triage` | any | advisory | looks into a problem you describe: failed units and their logs, disk space, updates, snapshots, the firewall; reports the likely cause and next steps | none |
| `exposure-watch` | any | advisory | reports the firewall's zones, the services they allow and the running services, weekly | none |
| `service-doctor` | any | supervised | finds failed or flapping units, works out why from their logs, restarts or reloads them; hourly report | start, restart, reload: `config-lifecycle`; stop, disable: an approval every time |
| `patch-pilot` | classic | supervised | lists the updates, takes a snapper snapshot, installs them with zypp one package at a time; a report on working days | install and snapshot: `patch-errata`; restart a service: `config-lifecycle` |
| `patch-pilot-transactional` | transactional | supervised | updates an immutable system into a new snapshot with mcp-server-tukit; a report on working days | refresh, update, clean up: `patch-errata`; reboot, apply, rollback: an approval every time |
| `disk-watch` | classic | supervised | reports file systems more than 85 % full and old snapper snapshots that could go; daily | deleting snapshots: an approval every time |
| `disk-watch-transactional` | transactional | supervised | the same on an immutable system; cleans up with transactional-update's rules | clean up: `patch-errata` |

*Classic* systems have a writable root file system; *transactional*
ones (SLE Micro, Leap Micro, a transactional SLES) have a read-only one
and are updated with transactional-update. `agent-admin templates`
says which this system is.

```bash
agent-admin templates                                   # the templates, as JSON
agent-admin create web-doctor --template service-doctor # its configuration, named web-doctor
agent-admin create web-doctor --template service-doctor --from agent.yaml   # your configuration
```

The agent gets the template's permissions and mode. Its task
categories stay supervised: every change waits for an approval until
you promote a category, after it has shown it is ready
([chapter 6](06-permissions-and-modes.md#promote)). The suggested
envelopes are in the template's `envelopes/`; set their time zone and
windows to yours:

```bash
agent-admin readiness web-doctor config-lifecycle
agent-admin promote web-doctor config-lifecycle \
    --envelope /usr/share/custos/templates/service-doctor/envelopes/config-lifecycle.yaml
```

Before you create one, check:

- **the model.** The templates use a local OpenAI-compatible server at
  `http://127.0.0.1:8000/v1` with `qwen3-32b`. Set your host's model
  once as the default for new agents
  ([chapter 5](05-configuration.md#model-defaults)) and every agent
  created from a template gets it; or change `model:` per agent, with
  `--from` and your own copy of `agent.yaml` (`agent-admin draft NAME
  --template T > agent.yaml`), or in Cockpit's editor;
- **the gateway's servers.** `template.yaml`'s `needs` lists them. A
  server the gateway does not have is simply not offered to the agent;
  the template still works with the others. `patch-pilot` needs
  mcp-server-zypp's privileged definition, since installing runs as
  root. The transactional templates need mcp-server-tukit: install
  `mcp-server-tukit-gateway` and link its definition
  `/usr/share/mcp-gateway/profiles/tukit.yaml` into
  `/etc/mcp-gateway/servers.d/` (mcp-gateway 1.8 or later);
- **the product documentation** (optional). Every template may read
  the documentation through the gateway's server `suse-docs`, mcp-docs
  serving its collections: SUSE's (`suse-docs-mcp-sles-16.0` and the
  other `suse-docs-mcp-*` packages), Custos's (`custos-docs`) and the
  others installed. The agent checks its findings and proposed steps
  against it and names the sections it used. Its permissions allow the
  reading tools `find_sections`, `search`, `outline` and
  `read_section`; in `health-check` they are in `diagnostics`. Without
  the server the agent works as before and says the documentation was
  not offered;
- **free disk space.** `triage`, `service-doctor` and the disk-watch
  templates read it through mcp-gateway-exec-server's `disk_usage`,
  a command you allow:

  ```bash
  zypper install mcp-gateway-exec-server
  cp /usr/share/custos/templates/exec.d-disk-usage.yaml /etc/mcp-gateway/exec.d/disk-usage.yaml
  ```

To change a template for this site, copy its directory to
`/etc/custos/templates/` and edit it there: it replaces the shipped one
of the same name, and survives updates. A directory there without
`template.yaml` hides the shipped template. A template that names a
category the catalog does not have is listed with the reason and
cannot be used.

In Cockpit: *Agents*, **Create agent**, **Start from** a template. The
form shows what the template does, its categories and what it needs at
the gateway, and fills the editor with its configuration; change the
name and the model, **Check**, **Create**. Templates for the other kind
of system are offered disabled.

## Editing an agent {#edit}

`agent-admin edit NAME --from FILE` validates the new configuration,
replaces it and syncs the schedules. It then restarts the agent if it is
running, which ends its activities in flight. For OpenShell agents,
`edit` may recreate the sandbox ([chapter 11](11-openshell.md#edit)). The
name and the sandbox kind cannot change. In Cockpit: the agent's
*Configuration*, **Edit configuration**, **Save and apply**.

## Deleting an agent {#delete}

```bash
agent-admin delete NAME              # also its state (/var/lib/agents/NAME)
agent-admin delete NAME --keep-state
```

`delete` disables and stops the agent's socket, service and timers. It
removes its configuration, gateway data, credentials, drop-ins and
timers, and, unless `--keep-state`, its sessions. The user
`agent-<name>` stays: uids are not reused while files may still belong
to it. In Cockpit, **Delete…** asks you to type the agent's name first.

## Credentials {#credentials}

```bash
agent-admin set-credential NAME model-key < key.txt
```

The secret is encrypted with `systemd-creds` (bound to this host) into
`/etc/credstore.encrypted/agent-<name>.<cred>`, root-only. The agent's
unit imports `agent-<name>.*` at start; restart a running agent to use a
new value (`systemctl restart agent@NAME.service`). For OpenShell agents,
`gateway-client-secret` and `gateway-token` create the sandbox's gateway
credential ([chapter 11](11-openshell.md#credential)).

```bash
agent-admin credentials NAME                 # names, what uses them, missing ones
agent-admin remove-credential NAME old-key   # one nothing uses
```

`credentials` lists names only: a stored value cannot be read back,
from agent-admin or from Cockpit; replace it with `set-credential`. A
credential the configuration names but that is not stored is listed as
missing (`"missing": ["model-key"]`): the agent does not start without
it. `remove-credential` refuses the credential its model uses unless you
add `--force`. An OpenShell agent's gateway credential is held by
OpenShell, not in the store: `set-credential` replaces it, and it goes
with the agent. Each agent has its own copy of a key: several agents
using one LiteLLM key each store it, and removing one agent's leaves the
others'.

In Cockpit, an agent's **Credentials** card does the same
([chapter 8](08-cockpit.md#agents)).

## Pausing and resuming an agent {#pause}

```bash
agent-admin pause NAME
agent-admin resume NAME
```

Pausing does two things. The gateway denies the agent every call
(`paused: true`, `paused_by: <person> <time>` in its data), whatever the
agent does. And the agent holds its activities before their next model or
tool call and refuses new ones (`409`). Resuming lets them go on. The
journal has `agent.paused` and `agent.resumed`.

## Stopping, starting and restarting an agent {#stop}

Restart an agent, for example after a credential changed. Its running
activities end. A stopped (idle) agent is started:

```bash
systemctl restart agent@NAME.service
systemctl restart agent-openshell@NAME.service   # an OpenShell agent, with its sandbox
```

In Cockpit, the agent's **Restart**. Stop an agent by its units; the
next request no longer starts it:

```bash
systemctl stop agent@NAME.socket agent@NAME.service
systemctl start agent@NAME.socket
```

For an OpenShell agent: `systemctl stop agent-openshell@NAME.service`,
which also stops its sandbox. A stopped agent's gateway data stays: the
Agents tab shows it as *stopped*. To keep it stopped across reboots,
`systemctl disable agent@NAME.socket`; `release-all` then leaves it
stopped too.

## Schedules {#schedules}

```bash
systemctl list-timers 'agent-schedule@*'
systemctl disable --now agent-schedule@NAME_SCHEDULE.timer     # pause one schedule
systemctl enable --now agent-schedule@NAME_SCHEDULE.timer
systemctl start agent-schedule@NAME_SCHEDULE.service           # run it now
```

To change a schedule's calendar or prompt, edit the configuration
([chapter 5](05-configuration.md#schedules)). `agent-admin edit` rewrites
the timers: it enables a new schedule's timer, and leaves one you
disabled disabled. A disabled timer stays disabled until you enable it,
`release-all` included.

## Events {#events}

An agent's events start its activities when a unit fails
([chapter 5](05-configuration.md#events)):

```bash
systemctl show -p OnFailure nginx.service                       # the events on a unit
journalctl -o json AGENT_NAME=NAME AGENT_EVENT=invocation.start \
  | jq -r 'select(.AGENT_ORIGIN == "event") | .AGENT_ORIGIN_BY'    # the activities events started
journalctl -o json AGENT_NAME=NAME AGENT_EVENT=event.skipped | jq -r .MESSAGE   # the skipped ones
agent-admin event NAME_EVENT_UNIT                                # start one by hand, damped like systemd's
```

To stop an event, remove it from the configuration and run
`agent-admin edit`. Pausing the agent, or the emergency stop, refuses
the event's activities like any other: the refusal is recorded
(`event.skipped`) and fails `agent-event@….service`.

## Diagnostic playbooks {#playbooks}

A playbook is a published diagnostic procedure: which read-only tools to
call, in which order, and what to look for. Custos ships eleven, in
`/usr/share/custos/playbooks/`:

| Playbook | Looks at |
|---|---|
| `failed-units` | failed systemd units and why they failed |
| `service-health` | one service (name it as `unit`): its state, exit status and restarts, its own log and systemd's account of it |
| `disk-pressure` | full file systems and inodes, snapshots, "No space left on device" |
| `memory-pressure` | memory and swap, pressure stalls, load, OOM kills |
| `journal-errors` | errors since the last boot, by source |
| `selinux-denials` | the SELinux mode and its denials |
| `time-sync` | whether NTP has synchronized the clock |
| `network-config` | interfaces, routes, name servers, listening ports (no reachability test: the tools have no network) |
| `boot-health` | the last boot's duration and slow or failed units |
| `package-consistency` | unsatisfied package dependencies |
| `update-status` | pending updates and patches, whether one waits for a reboot, and the services to restart after one (installs nothing) |

```bash
agent-admin playbooks | jq 'map_values(.title)'     # the playbooks
curl -s --unix-socket /run/agents/NAME/api.sock http://agent/v1/playbooks |
  jq '.playbooks | map_values(select(.runnable)) | keys'   # those NAME can run
curl --unix-socket /run/agents/NAME/api.sock http://agent/v1/activities \
  -d '{"playbook": "failed-units", "wait": true}'   # run one now
curl --unix-socket /run/agents/NAME/api.sock http://agent/v1/activities \
  -d '{"playbook": "service-health", "unit": "nginx.service"}'   # about one unit
```

Or from a schedule or an event, with `playbook:` instead of (or with)
`prompt:` ([chapter 5](05-configuration.md#schedules)); an event's
activity is about the unit that failed. The agent's host calls each
step's tool itself, in order and with the step's arguments, before the
model's first turn, so a model
that is unreliable at issuing tool calls still gets every output. Each
call goes through the gateway, which decides it as any other call: a
playbook grants nothing. The activity marks these calls as playbook
steps (`playbook_step`), with the step's *why* as their reason; the
model reads their outputs, may call the playbook's tools again (with
other arguments) and further tools the playbook names (`tools`), and
reports. In a playbook run the model is shown only those: a small model
reads every tool it is shown, each with its description, before it
answers. When the gateway does not offer the agent a step's tool,
the playbook is refused before anything runs (`422`, naming the tools);
steps marked `optional` are left out instead. The agent then reports
what it found, its hypotheses and a remediation plan for you; it changes
nothing.

Most playbooks need read-only commands for mcp-gateway's `exec` server
(`mcp-gateway-exec-server`):

```bash
cp /usr/share/custos/templates/exec.d-diagnostics.yaml /usr/share/custos/templates/exec.d-disk-usage.yaml /etc/mcp-gateway/exec.d/
/usr/libexec/mcp-servers/mcp-server-exec --check
```

The network commands of `network-config` (`network_addresses`,
`network_routes`, `listening_sockets`) are not the exec server's, whose
instances see a network namespace of their own and so none of the
host's interfaces. They are Custos's server `custos-net`, whose instances
see the host's network but cannot use it (mcp-gateway's `network_view:
host`: unix and netlink sockets only, no IP traffic):

```bash
cp /usr/share/custos/gateway/servers.d/custos-net.yaml /etc/mcp-gateway/servers.d/
```

`update-status` also lists the services that still run what an update
replaced, and so wait for a restart, when mcp-gateway's server
`restarts` is offered (mcp-gateway 1.16). Reading other users'
processes needs a capability (`CAP_SYS_PTRACE`), so its definition is a
profile you link yourself:

```bash
zypper install mcp-gateway-restarts-server
ln -s /usr/share/mcp-gateway/profiles/restarts.yaml /etc/mcp-gateway/servers.d/restarts.yaml
```

`custos-setup` does the first two on the diagnostics-only install, and
links `restarts` only with `--restarts`; without it, never again once
you removed the link.

Its answer goes to the model: each process that uses a replaced file,
with its id, name (not its command line), user, unit, files and
packages; after a glibc update, nearly all of them. Weigh that when the
model runs elsewhere ([chapter 14](14-network.md#inference)).
The agent needs `read_only` permissions for the tools, with
`category: diagnostics` on a diagnostics-only host
([chapter 6](06-permissions-and-modes.md#diagnostics-only)), for example
`- {server: exec, tool: memory_usage, read_only: true, category: diagnostics}`
or `- {server: custos-net, tool: network_addresses, read_only: true, category: diagnostics}`.
The gateway allows a call of `restarts` without an approval only by a
permission that names server and tool exactly, as the `health-check`
template's does: `- {server: restarts, tool: pending_restarts, read_only: true, category: diagnostics}`.
Each playbook's `needs` says what it uses.

Your own playbooks go in `/etc/custos/playbooks/`, one file per playbook,
named after it; a file with a shipped name replaces that playbook, and an
empty one hides it:

```yaml
# /etc/custos/playbooks/web-health.yaml
web-health:
  title: Web server health
  steps:
    - {tool: systemd__list_loaded_units, why: "The state of nginx and php-fpm.",
       args: {patterns: [nginx.service, php-fpm.service], state: all, properties: true}}
    - {tool: systemd__list_log, why: "Their recent errors.", args: {unit: ["nginx|php-fpm"], count: 50}}
    - {tool: systemd__list_log, why: "The unit's own journal.", args: {unit: ["{unit}"], exact_unit: true, count: 50}}
  tools: [systemd__get_file]   # optional: further tools the model may call
  look_for: Restarts, 5xx errors, a full upstream pool.
```

`args` are the call's arguments, as the tool takes them; a step without
them calls the tool without arguments, which for many tools means their
defaults: `systemd__list_loaded_units` without `state` lists only the
active units, and `systemd__list_log` without `unit` or `pattern` the
last 100 lines of the whole journal. Ask for what the playbook needs,
and little more: the model reads every output, and a small model reads
slowly and loses its way in a long one. `{unit}` in an argument is the
unit the activity is about (`"unit"` in the request, or the unit that
failed for an event); a step with it runs only when there is one. In an
argument that is a regular expression, such as `list_log`'s `pattern`,
write `{unit_re}`: the unit with its dots escaped.

`agent-admin playbooks` lists a file that does not parse with its error.

### Diagnosis reports and your feedback {#reports}

An agent ends a playbook with a **diagnosis report**: a summary, its
findings, its hypotheses about the cause, each with its confidence (low,
medium or high, the model's own estimate), the evidence each rests on,
and a remediation plan, step by step, for you to carry out. Each piece of
evidence is a quote from a tool's output. Custos checks it: a quote that
is not in what that tool returned in this activity is marked
**unverified**, whatever the model says. Trust a hypothesis by its
verified evidence, not by its confidence.

```bash
curl -s --unix-socket /run/agents/NAME/api.sock http://agent/v1/activities/ACTIVITY | jq .report
journalctl -o json AGENT_NAME=NAME AGENT_EVENT=diagnosis.report | jq -r .AGENT_REPORT | jq .
```

Tell Custos whether it was right; your feedback is the evidence for
letting the agent do more later
([chapter 6](06-permissions-and-modes.md#readiness)):

```bash
agent-admin feedback NAME ACTIVITY --hypothesis h1=confirmed --hypothesis h2=rejected \
  --plan useful --executed --comment "the journal had no size limit"
```

Per hypothesis `confirmed`, `rejected` or `unsure`; the plan `useful` or
`not_useful`; `--executed` when you carried it out. It is recorded with
your name (`diagnosis.feedback`), outside the agent, and refused for an
activity without a report or a hypothesis it does not have.

## Cancelling an activity {#cancel}

```bash
curl -s --unix-socket /run/agents/NAME/api.sock http://agent/v1/activities/ID/cancel -X POST
```

`204` when it was running, `409` when it was not. Root, the activity's
user and whoever started it may cancel. In Cockpit, **Cancel activity**.
The journal has `activity.cancel`.

## The emergency stop {#emergency-stop}

```bash
agent-admin stop-all
agent-admin release-all
```

`stop-all` writes `_control: {stopped: true, by, at}` to
`/etc/mcp-gateway/policy/agents/_control.yaml`: once the gateway has
it, it denies every agent everything, on every backend. Then it stops
every agent's socket and service, every OpenShell sandbox and every
schedule timer. An agent started by hand afterwards is still denied by
the gateway. `release-all` sets `stopped: false`, recording who released
it, and starts again every agent's socket and schedule timer that is
enabled: an agent or a schedule you disabled stays stopped. Paused
agents stay paused. In Cockpit: **Stop all agents** in the header, and
**Release…** in the banner.

## The agents' systemd sandbox {#sandbox}

`agent@.service` runs each agent as its own user with
`ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp`,
`PrivateDevices` (with `/dev/tpmrm0` allowed only for systemd to
decrypt TPM-sealed credentials), `NoNewPrivileges`, no capabilities, a system call
filter and `MemoryDenyWriteExecute`, without the gateway's control socket
and without `/run/agents`. Adjust it for one agent with a drop-in, for
example to set environment variables:

```bash
systemctl edit agent@NAME.service           # writes /etc/systemd/system/agent@NAME.service.d/override.conf
```

Do not open the control socket or `/run/agents` to an agent: that would
let it reach the gateway's approvals or other agents. The gateway's
policy still decides each call, but the sandbox is part of the design.
`systemd-analyze security agent@NAME.service` shows the exposure.

## Backing up and moving agents {#backup}

To restore a host's agents, keep:

| Path | What |
|---|---|
| `/etc/custos/agents/` | configurations |
| `/etc/custos/defaults.yaml`, `/etc/custos/promotion.yaml`, `/etc/custos/categories.d/`, `/etc/custos/templates/` | model defaults, readiness thresholds, your categories and templates |
| `/etc/mcp-gateway/policy/agents/` | modes, permissions, pause, the emergency stop, `_http.yaml` |
| `/etc/sysusers.d/agent-*.conf` | the agents' users |
| `/etc/systemd/system/agent-schedule@*.timer.d/`, `/etc/systemd/system/agent@*.service.d/`, `/etc/systemd/system/agent-openshell@*.service.d/` | schedules' calendars, drop-ins |
| `/var/lib/agents/` | chat sessions (optional) |

The credentials (`/etc/credstore.encrypted/agent-*`) are encrypted for
this host and cannot be read on another. On a new host, run
`agent-admin create` with each configuration, which writes it as
supervised without permissions. Then copy its saved
`/etc/mcp-gateway/policy/agents/NAME.yaml` back over the new one: it
holds the mode, the permissions and the categories' promotions, and the
gateway picks it up within seconds. Set the credentials again with
`agent-admin set-credential`.
