---
title: "12. Troubleshooting"
description: Symptoms with their causes and fixes - installation, configuration errors, agents that do not start, the gateway connection, tools not offered or denied, models, approvals, schedules, Cockpit, OpenShell and tokens, SELinux - and a table of error messages.
tags: [troubleshooting, errors, problems, faq, debugging]
---

# 12. Troubleshooting

Each section starts from what you see and gives the cause and the fix.
The table of [error messages](#error-messages) maps each message, as
the programs print it, to its section. Two commands tell most of the
story:

```bash
journalctl -u agent@NAME.service --since -30min                  # the agent: its unit, its errors and its records
journalctl -u mcp-gateway.service --since -30min                 # the gateway: decisions and refusals
```

## Installation problems {#installation}

| You see | Cause | Fix |
|---|---|---|
| `semodule -l` has no `custos` | `custos-selinux` not installed, or installed before `mcp-gateway-selinux` | `zypper install custos-selinux`; or `zypper install -f custos-selinux` |
| `getent group agent-clients` prints nothing | sysusers not applied | `systemd-sysusers /usr/lib/sysusers.d/custos.conf` |
| `/run/mcp-gateway/mcp.sock` missing | mcp-gateway not running | `systemctl enable --now mcp-gateway.service`; `mcp-gateway-admin doctor` |
| labels of `/run/agents`, `/var/lib/agents` or `/etc/custos` are generic (`var_run_t`, `etc_t`) | created before the module was loaded | `restorecon -R /run/agents /var/lib/agents /etc/custos /usr/libexec/custos` |

## Configuration errors {#configuration-errors}

`agent-admin check` and `create` print every problem at once:

| Message | Fix |
|---|---|
| `version: N: this launcher implements version 1` | `version: 1` |
| `name: "…": want lower-case letters, digits and '-', 2-32 characters, starting with a letter` | rename the agent |
| `name "…" does not match the directory "…"`, `the file is for agent "…", not "…"` | the file's `name` must be the `NAME` given to `agent-admin` |
| `model.provider: missing`, `model.provider: "…": want gemini, vertex or openai` | set the provider ([chapter 5](05-configuration.md#models)) |
| `model.endpoint: "…": want an http(s) URL` | a full URL, `http://127.0.0.1:8000/v1` |
| `model.endpoint: only for provider openai` | remove it for `gemini` and `vertex` |
| `model.name: missing` | name the model, `model.name: qwen3-32b` |
| `model.credential: "…": want a credential name such as model-key` | `credential` is the name of a stored secret, not the key: `set-credential NAME model-key` and `model.credential: model-key` |
| `model.credential looks like a key, not a name: …` | the key was given where its name goes. Store the key, `set-credential NAME model-key`, and set `model.credential: model-key` (in the model defaults too, if they hold it) |
| `model.credential: provider gemini needs an API key credential` | `set-credential NAME model-key` and `model.credential: model-key` |
| `instruction and instruction_file: set one of them` | keep one |
| `gateway.servers: name at least one gateway server` | list the servers the model uses |
| `gateway.tools: "…": want <server>__<tool>` | fix the tool name: `<server>__<tool>`, as the gateway names it |
| `gateway.tools: "agents__delegate": its server "agents" is not in gateway.servers: add agents to gateway.servers` | an agent is shown a tool only from a server it names: add the server to `gateway.servers`. In Cockpit, **Add agents to gateway.servers** next to the message does it and checks again |
| `gateway: set socket or url, not both` | `url` for OpenShell agents, `socket` (or nothing) otherwise |
| `gateway.url: "…": want an https URL (http only on the host's loopback, for tests)` | the gateway's `http.audience`, e.g. `https://gw.example.com:8443/mcp` |
| `gateway.url: an openshell agent reaches the gateway over HTTPS (CONTRACT.md §3a)` | add `gateway.url` |
| `gateway.oauth: only with sandbox kind openshell (OpenShell's gateway mints the tokens)` | remove `oauth`, or use the `openshell` backend |
| `gateway.oauth.token_url: …`, `gateway.oauth.client_id: …`, `gateway.oauth.scopes: …`, `gateway.oauth.refresh_before_seconds: want 0 or more` | the agent's OAuth client ([chapter 11](11-openshell.md#configuration)) |
| `schedules[N].name: "…": want a unique lower-case name` | rename the schedule |
| `schedules[N].on_calendar: want a systemd calendar expression (systemd.time(7))` | test it with `systemd-analyze calendar '…'` |
| `schedules[N].prompt: missing` | give the schedule its prompt |
| `limits.…: N: must be positive`, `sessions.retention_days: must be positive` | a positive number, or leave it out for the default |
| `client_tools: "…": want deny or {allow: [...]}` | `client_tools: deny` |
| `sandbox.kind: "…": want systemd or openshell` | fix the kind |
| `sandbox.openshell_gateway: only for kind openshell` | remove it for a systemd agent |
| an unknown key (`field … not found`) | remove or correct the key: unknown keys are errors |

## An agent does not start or answer {#agent-start}

- **`curl` to its socket hangs or fails, the unit failed.**
  `systemctl status agent@NAME.service` and its journal name the error.
  `config.invalid` in the journal means the installed configuration is
  wrong: `agent-admin check NAME`.
- **The Cockpit page shows the agent as "not answering (no answer
  within 8 s): its service may be failing to start"**: its socket is
  there, but its service fails at start and systemd restarts it every 5
  seconds, so the connection is never answered. Its **Logs**, or
  `journalctl -u agent@NAME.service`, say why: often one of the next
  two.
- **`Failed to set up credentials: Protocol error`** and
  **`status=243/CREDENTIALS`** in the unit's journal, on a host with a
  TPM2 (`systemd-analyze has-tpm2`): `set-credential` sealed the secret
  to the TPM, and before 0.2.0 the agent's unit (`PrivateDevices=yes`)
  kept systemd from reaching it. Allow it in a drop-in:
  `mkdir -p /etc/systemd/system/agent@NAME.service.d`,
  `printf '[Service]\nDeviceAllow=/dev/tpmrm0 rw\n' > /etc/systemd/system/agent@NAME.service.d/60-tpm.conf`,
  `systemctl daemon-reload`, `systemctl restart agent@NAME.service`.
  Later units allow it themselves; remove the drop-in then.
- **`model.credential: open /run/credentials/agent@NAME.service/…:
  permission denied`**, with SELinux enforcing and nothing in the audit
  log: before 0.2.0 the `custos` module did not let agents read their
  unit's credentials (`init_var_run_t`), and the base policy does not
  log that denial. Add the permission in a local module:
  `printf '(allow agent_t init_var_run_t (dir (getattr search open read)))\n(allow agent_t init_var_run_t (file (getattr open read)))\n' > /root/custos-credentials.cil`,
  `semodule -i /root/custos-credentials.cil`,
  `systemctl restart agent@NAME.service`. Remove it once 0.2.0 is
  installed: `semodule -r custos-credentials`.
- **`model.credential CRED is not stored for agent NAME: …`** (before
  0.2.1: **`model.credential "…": no credentials directory`** or
  **`model.credential: open …: no such file or directory`**): the
  credential is not set, has another name, or went with an earlier agent
  of the same name (`delete` removes its credentials).
  `agent-admin set-credential NAME CRED < secret`, then
  `systemctl restart agent@NAME.service`. If `CRED` is the key itself,
  see `looks like a key` [above](#configuration-errors).
- **`model.credential looks like a key, not a name`**: the agent's
  `model.credential` holds the key. Store it under a name and set that
  name ([chapter 5](05-configuration.md#model-credentials)).
- **`connect: permission denied` from a member of `agent-clients`**:
  the session predates the group membership. Log in again.
- **The agent answers `409` to a new activity**: it is paused
  (`agent-admin show NAME`); `agent-admin resume NAME`.

## The agent cannot reach the gateway {#gateway}

`/v1/status` says `"reachable": false`, or the agent's answers say its
tools failed:

- mcp-gateway is not running: `systemctl status mcp-gateway.service`.
- The socket is not the gateway's (`gateway.socket`), or the agent's
  user lost `mcp-users`: `id agent-NAME`.
- The gateway was restarted: the agent reconnects at its next call.

## A tool is not offered or always denied {#tool-denied}

The gateway hides what an agent may not call, and denies what it calls
anyway. Check in this order:

1. The server is registered with the gateway
   ([chapter 3](03-gateway.md#servers)) and named in the agent's
   `gateway.servers`.
2. A permission names the tool (`agent-admin show NAME`). In Cockpit,
   *Policy*, **What would the gateway decide?**
3. The mode allows it: `advisory` denies every change
   ([chapter 6](06-permissions-and-modes.md#modes)).
4. The agent is not paused and the emergency stop is off
   (`agent-admin show`).
5. No gateway role is bound to `agent-NAME` or a group it is in: that
   denies the agent everything. Check the role data
   (`mcp-gateway-admin policy show`).
6. The gateway's audit says why: `journalctl -u mcp-gateway.service -o cat | grep '"sub":"agent-NAME"' | grep deny`.
   Its `reason`, which the agent also receives as its error:

| `reason` | Cause | Fix |
|---|---|---|
| `the agent is paused (agent-admin resume)` | paused | `agent-admin resume NAME` |
| `the emergency stop is in effect (agent-admin resume-all)` | the emergency stop | `agent-admin resume-all` ([chapter 9](09-administration.md)) |
| `task category C is not enabled on this host (diagnostics-only): agent-admin enable C` | a permission of a disabled category ([chapter 6](06-permissions-and-modes.md)) | `agent-admin enable C`, if the host should do this |
| `permissions without a task category are not enabled on this host (…)` | on a host that started diagnostics-only, a permission has no category | give it a category, or `agent-admin enable --all` |
| `a diagnostics permission that is not read-only is no permission (diagnostics only reads)` | `category: diagnostics` without `read_only: true` | mark it read-only, or move it to another category |
| `the agent is advisory: it only reads (agent-admin mode)` | advisory mode | `agent-admin mode NAME supervised` |
| `the agent has no agent data (agent-admin create)` | the agent's gateway data is missing | `agent-admin permissions NAME`, or create the agent again |
| `the agent's mode M is unknown`, `the mode M of task category C is unknown` | the agent data was edited by hand | `agent-admin mode`, `agent-admin promote`/`demote` |
| `a role is bound to the agent's user: …` | step 5 | remove the binding |
| `an agent's subject from an issuer not trusted for agents` | an OpenShell agent's token from another issuer ([chapter 11](11-openshell.md#issuers)) | `agent-admin issuers --add ISSUER` |
| `the agent's data does not match its seal (changed outside agent-admin): …` | the agent's gateway data was edited by hand on a sealed host ([chapter 6](06-permissions-and-modes.md#diagnostics-only)) | `agent-admin show NAME` says what; write it again with `agent-admin` (`permissions`, `mode`, `owners`), or `agent-admin seal --agents` to sign it as it is |
| `the agents' token issuers (_http.yaml) do not match their seal …` | `_http.yaml` was edited by hand on a sealed host | `agent-admin issuers --add`/`--remove` |
| `no matching permission` | no permission names the tool | add the permission (step 2) |


## The model is not reached {#model}

- **`connect: permission denied` to the model's port**: SELinux allows
  `agent_t` only HTTP ports. Label the port, or turn on
  `agent_connect_any` ([chapter 5](05-configuration.md#selinux-and-the-models-port)).
- **`connection refused`**: the server is not listening on that address.
  For an OpenShell agent, the host's server must listen where the
  sandbox reaches it (`host.openshell.internal`).
- **`401` or `403` from the model's API**: the key is wrong or missing
  (`model.credential`).
- **`no answer within model.timeout_seconds`** or **`no answer after …,
  when the activity's time limit (limits.max_invocation_seconds) ran
  out`**: the model took longer than the limit named. Each model call of
  an activity sends the whole conversation so far, so the prompt grows
  with every tool result, and a local model on a CPU or a small GPU takes
  longer for each. The agent's `model.call` records show it: tokens in
  (`AGENT_TOKENS_IN`) and how long each call took (`AGENT_DURATION_MS`);
  the failed call's record has `AGENT_OUTCOME=error` and the size it
  sent (`AGENT_PROMPT_BYTES`):

  ```bash
  journalctl -o json -t agent-health-check AGENT_EVENT=model.call --since today |
    jq -r '[.AGENT_TOKENS_IN, .AGENT_DURATION_MS, .AGENT_OUTCOME, .AGENT_PROMPT_BYTES] | @tsv'
  ```

  Raise the limit (`model.timeout_seconds`, or remove it;
  `limits.max_invocation_seconds`), let the model server keep the prompt's
  common start between calls (prompt caching, such as llama.cpp's
  `--cache-reuse`, or vLLM's prefix caching), or give the model a faster
  machine.

## Approvals {#approvals}

| You see | Cause | Fix |
|---|---|---|
| a change is denied instead of waiting | the agent is in `advisory` mode, or no permission names the tool | `agent-admin mode NAME supervised`; add the permission |
| a change runs without an approval | the permission says `read_only: true`, or `autonomous: true` in `bounded` mode, or a grant covers it | correct the permission; revoke the grant ([chapter 7](07-approvals.md#grants)) |
| an approval in the list, but the agent's activity ended | the call waited longer than `approval_timeout` | approving it lets the agent's next identical call through; or decline it |
| `You may not decide approval …: the gateway's approver rules do not name you.` | not an approver for that server | add yourself to the approver rules ([chapter 3](03-gateway.md#approvers)) |
| the decision is recorded as root's | decided as root | decide as yourself ([chapter 7](07-approvals.md#who-is-recorded)) |

## Schedules do not run {#schedules}

- `systemctl list-timers 'agent-schedule@*'`: the timer is missing or
  disabled. `systemctl enable --now agent-schedule@NAME_SCHED.timer`.
- After `stop-all`, timers stay stopped until `release-all`.
- `agent-admin trigger NAME_SCHED` runs it by hand and prints why it
  fails (`agent … has no schedule "…"`: the schedule's name in
  `agent.yaml` differs).

## Cockpit problems {#cockpit}

| You see | Cause | Fix |
|---|---|---|
| no *Custos* in Cockpit's menu | `custos-cockpit` not installed | `zypper install custos-cockpit`, then reload the browser page |
| `…: no access to its socket (members of agent-clients have it)` | the person is not in `agent-clients` | `usermod -aG agent-clients USER`, log in again |
| `This needs administrative access (turn it on in Cockpit's top bar).` | an administrative action without it | turn on *Administrative access* (sudo) |
| `Reading the journal needs access to it (systemd-journal or wheel).` | no journal access | add the person to `systemd-journal` |
| *The gateway's records are not readable here* (Audit) | no access to the gateway's journal | as above |
| `Approving your own request needs your password: …` | the approval is the person's own, which only `self` allows | use mcp-gateway's page or its command line, which ask for the password |
| an agent shows *unreachable* | its socket refused the person, or the agent failed | the row says why; see [above](#agent-start) |
| `mcp-gateway is not running (/run/mcp-gateway/control.sock not found).`, `No access to mcp-gateway's control socket (…).` | the gateway is down, or the person may not use its control socket | `systemctl status mcp-gateway.service`; mcp-gateway's documentation for its socket's group |
| `Approval … is no longer pending (decided elsewhere, or timed out).` | someone else decided it, or it expired | none: the list refreshes |
| `Cancelling … failed: only its user, who started it, or an administrator may` | the activity is someone else's | ask its user, or turn on administrative access |
| `Reading it needs administrative access (turn it on in Cockpit's top bar).` (Edit configuration) | `agent.yaml` is readable by root and the agent only | turn on administrative access |
| `This agent does not offer chat (interaction.agui is off).` | the agent's configuration | `interaction: {agui: true}` ([chapter 5](05-configuration.md#interaction)) |
| `The agent did not answer: …` (chat) | the agent failed or does not answer | the text says why; see [above](#agent-start) |
| a template listed as `(cannot be used: …)` in **Start from** | the template names a category the catalog lacks, or a file of it is wrong | the text says which; fix or hide the template in `/etc/custos/templates` ([chapter 9](09-administration.md#templates)) |

## A promoted category still asks for an approval {#promotion}

| You see | Cause | Fix |
|---|---|---|
| the call waits for an approval although `show` says `bounded` | it is outside the envelope's windows: check `days`, `from`, `to`, and `tz` (without `tz` the window is UTC) | `agent-admin promote` again with the right window |
| | the permission that matches the call has no `category`, or another one | `agent-admin show NAME`; give the permission the category |
| every call of the agent is denied | a category's `mode` in its gateway data is neither `supervised` nor `bounded` (edited by hand) | `agent-admin demote`, or promote again |
| `agent … is advisory, which caps every category` | advisory allows no change, promoted or not | `agent-admin mode NAME supervised`, then promote |

## OpenShell agents and tokens {#openshell}

| You see | Cause | Fix |
|---|---|---|
| `importing the provider profile: …`, or `Unknown gateway` from `openshell` | OpenShell's gateway is not registered for root, or has another name | copy the registration to `/root/.config/openshell` ([chapter 11](11-openshell.md#install-openshell)); `sandbox.openshell_gateway` |
| `configuring the token's renewal: …` | OpenShell refused the renewal settings for the provider | the text after the colon is OpenShell's; check `gateway.oauth` and the OpenShell version ([chapter 11](11-openshell.md#oauth)) |
| `minting the first token (the issuer refused the client?): …` | wrong client id or secret, or the client lacks the client credentials grant | fix the client at the identity provider; `set-credential` again |
| a certificate error from the agent's calls | OpenShell does not trust the gateway's CA | `additional_ca_bundle` ([chapter 11](11-openshell.md#trust)) |
| `agent-admin check`: `gateway.url …: … resolves to 127.0.0.2 on this host, which OpenShell's proxy never connects to`; the agent's calls are refused by OpenShell's proxy | the name's DNS answer is a loopback address | make DNS answer the host's network address ([chapter 11](11-openshell.md#listener)) |
| `agent-admin check`: `gateway.url …: …: not in DNS …; OpenShell resolves the name by DNS, not /etc/hosts`; with Podman the agent's calls fail with `connect: permission denied` | the name is only in `/etc/hosts` (or a nameserver that does not know it is first in `/etc/resolv.conf`) | put the name into DNS, or use a name that is ([chapter 11](11-openshell.md#listener)) |
| `agent-admin check`: `gateway.url …: the sandbox reaches the gateway as host.openshell.internal, a name only a private CA's certificate can carry` | the agent's `gateway.url` is the loopback alias | fine on a test system with a private CA; otherwise use the host's name, with the gateway's official certificate ([chapter 11](11-openshell.md#listener)) |
| `agent-admin check`: `gateway.url … is not the gateway's http.audience …` | the agent's URL and the gateway's audience differ | make them the same: the URL the agent uses, whose host the certificate names |
| `401 invalid token` in the gateway's journal (`token rejected`) | wrong issuer or audience, expired, bad signature | the token's `iss` and `aud` must be `http.issuer` and `http.audience`; check with `mcp-gateway-admin setup http --token` |
| `403 insufficient scope` | the token lacks a scope of `http.scopes` | add the scope to the client (as *Default* in Keycloak) and to `gateway.oauth.scopes` |
| every call denied, the audit shows the agent's subject | its issuer is not in `_http.yaml`, or `sub` is not `agent-NAME` | [chapter 11](11-openshell.md#issuers), [step 5](11-openshell.md#idp) |
| the agent's records missing from the host's journal | the relay cannot read the sandbox's container: `CUSTOS_OPENSHELL_USER` not set at `create` for rootless Podman | `journalctl -u agent-openshell@NAME.service` names it (`agent-openshell-logs: …`); recreate the agent with the variable set |
| sandboxes fail to start with rootless Podman (`memory.swap.max`) | cgroup controllers not delegated to the user | the `user@.service` drop-in, then restart the user's manager ([chapter 11](11-openshell.md#install-openshell)) |
| brief `500` errors, refused connections (`connect: permission denied`) or dropped ones (`EOF`) around a token renewal | OpenShell's proxy while it renews | none: the agent retries them, and reconnects; a dropped `tools/call` is not sent again (it may have run), so that activity fails and is started again |
| `agent-openshell-start: sandbox agent-NAME is not Ready after 120s: …` in `journalctl -u agent-openshell@NAME.service`; the agent's socket does not answer | OpenShell did not get the sandbox *Ready* within 2 minutes. The text after the colon says why: `sandbox agent-NAME is Stopping` (a stop that does not finish), OpenShell's refusal of the start (`previous driver operation is still pending`, `sandbox must be Stopped…`), or `openshell does not know sandbox agent-NAME` | the unit tries again after 5 seconds (`Restart=on-failure`). If it keeps failing: `openshell sandbox get agent-NAME` and `openshell logs agent-NAME`. A sandbox OpenShell no longer knows is made again by `agent-admin set-credential` |
| `sandbox … is still there … after its deletion` (edit, set-credential) | OpenShell did not delete the old sandbox in 2 minutes | `openshell sandbox list`; delete it by hand, then `agent-admin edit` again |

## agent-admin says the gateway's policy did not load the change {#policy-not-loaded}

`the gateway's policy did not load the change within 20s` means
mcp-gateway's policy engine did not read the agent's data in time. Check
`systemctl status mcp-opa.service` and its journal: a YAML error in a
file of `/etc/mcp-gateway/policy/agents/` stops it from loading.
`mcp-gateway's OPA is not running; the change is in force when it loads …`
is a warning: the file is written, and the engine reads it at its start.

## SELinux denials {#selinux}

```bash
ausearch -m AVC,USER_AVC -ts recent | grep -E 'agent_t|agent_relay_t|mcpsrv_agents_t|mcpsrv_exec_t'
```

| Denial | Cause | Fix |
|---|---|---|
| `agent_t` → `name_connect` on a port | the model's port is not an HTTP port | [chapter 5](05-configuration.md#selinux-and-the-models-port) |
| `agent_t` → files of a generic type | files restored or created before the module | `restorecon -R /etc/custos /var/lib/agents /run/agents` |
| `mcpsrv_exec_t` → `init_t:system status`, or `proc_psi_t:dir search`; the diagnostic tools `boot_time`, `boot_blame` or `pressure_stall` end in a tool error | Custos 0.3.0's module lacks the rules for these commands of `exec.d-diagnostics.yaml` | update Custos; its module allows them |
| `network-config` reports no interfaces, no routes or no listening sockets, or `exec__network_addresses` fails (0.3.0) | the exec server's instances see a network namespace of their own | update Custos and run `custos-setup` again (or copy `custos-net.yaml`, [chapter 9](09-administration.md)); an agent made by 0.3.0 needs the `custos-net` permissions of the `health-check` template |
| `mcpsrv_exec_t` → anything else, for a command you added to `/etc/mcp-gateway/exec.d` | the exec server's domain covers the shipped commands only | a local module for your command: `mcp-gateway-admin profile` drafts one |
| anything else in these domains | unexpected | report it with the `ausearch` output |

## Error messages {#error-messages}

| Message (as printed) | Section |
|---|---|
| any `version:`, `name:`, `model.`, `gateway.`, `schedules[…]`, `client_tools:`, `sandbox.kind:` message | [Configuration errors](#configuration-errors) |
| `no task category "…" (the catalog has: …)` | the name is not in `agent-admin categories` ([chapter 6](06-permissions-and-modes.md#categories)) |
| `agent … category … is not ready (…); see agent-admin readiness …, or promote with --despite-readiness --reason TEXT` | the measures are below the thresholds ([chapter 6](06-permissions-and-modes.md#readiness)): wait and keep approving, adjust `/etc/custos/promotion.yaml`, or promote with a reason |
| `/etc/custos/promotion.yaml: …` | a key that is not `observation_days`, `proposals`, `override_rate`, `incident_rate` or `window_days`; a rate outside 0 to 1; a negative `observation_days` or `proposals`; `window_days` below 1 |
| `reading agent …'s journal`, `reading mcp-gateway's journal` | `agent-admin readiness` needs root or the `systemd-journal` group |
| `agent … has no permission in category …` | give a permission `category:` first |
| `agent … is advisory, which caps every category` | [A promoted category still asks](#promotion) |
| `envelope: hosts needs the fleet`, `envelope: blast_radius needs the fleet` | leave the part out; `windows`, `rate` and `rollback` are enforced so far |
| `envelope: rate: per "1d": want 1s, 1m or 1h` | the gateway counts per second, minute or hour: `{calls: 100, per: 1h}` rather than a day |
| an approval of a promoted category's call, within its windows, "Rate 20/h reached" | the envelope's `rate` (chapter 6): over it, each call asks. Three in a day demote the category |
| `rate limit exceeded` | a `rate_limit` obligation in a permission (a hard cap: it denies); an envelope's `rate` asks instead |
| `envelope: rollback: …` | `on` lists at least one of `tool_error`, `limit_exceeded`, `health_check_failed`; `health_check` (a command) is given exactly when `health_check_failed` is ([chapter 6](06-permissions-and-modes.md#auto-demote)) |
| `agent … category … demoted: …` (from `agent-admin evaluate` or `custos-demote.service`) | the demotion evaluator demoted it, for the reason given; promote it again once that is resolved |
| `an incident needs --reason TEXT: why it should not have run` | give the reason |
| `not answering (no answer within 8 s): its service may be failing to start; see its Logs` (Cockpit) | [An agent does not start or answer](#agent-start) |
| `agent …: no action … of activity … in category … ran without an approval in the last week` | the activity and call are not in `agent-admin review NAME CATEGORY`'s list: it had an approval, is older than a week, or belongs to another category |
| `agent …: category … has not been promoted: its calls all had approvals` | only promoted categories have actions to review |
| `agent-admin: custos-demote.timer could not be enabled …` | nothing demotes automatically: `systemctl enable --now custos-demote.timer` |
| `envelope: no windows`, `envelope: windows[…]: …` | write at least one window: `days`, `from`, `to` (`HH:MM`, `from` before `to`), `tz` (an IANA zone such as `Europe/Berlin`) |
| `envelope: unknown key "…"` | an envelope has `windows` and `rollback` only ([chapter 6](06-permissions-and-modes.md#promote)) |
| `--despite-readiness needs --reason TEXT` | say why, with `--reason` |
| `agent … category … is already bounded with this envelope` | nothing to do; give another envelope to change it |
| `verdict "…": want ok or incident` | `ok` or `incident` |
| `agent …: demoting …: …` (from `agent-admin evaluate`) | the evaluator could not demote the category: the text says why, often [the policy not loading](#policy-not-loaded) |
| `no template "…" (agent-admin templates lists them)`, `"…" is not a valid template name` | `agent-admin templates` lists them ([chapter 9](09-administration.md#templates)) |
| `template …: …` | a file of that template is wrong (in `/etc/custos/templates` your own): the text names the file and the problem |
| `agent … was created, but setting the template's permissions failed (agent-admin permissions … < …): …` | the agent exists without permissions: run the `agent-admin permissions` command the message gives, once the cause is fixed |
| `defaults: …`, `/etc/custos/defaults.yaml: …` | the model defaults are checked like `model:` ([chapter 5](05-configuration.md#model-defaults)) |
| `the starting configuration is not valid: …` (`draft`, `create --template`) | the template's configuration with the model defaults does not check: fix the defaults or the template |
| `"…" is not a valid credential name` | lower-case letters, digits, `-` and `_`, such as `model-key` ([chapter 5](05-configuration.md#model-credentials)) |
| `…: empty` (`set-credential`) | nothing on stdin: pipe the secret in |
| `sandbox.openshell_gateway: agent …'s sandbox is on OpenShell gateway "…"; delete and create it to move it` | the OpenShell gateway is fixed at `create` |
| `permissions[…]: autonomous is for permissions without a category` | drop `autonomous`; promote the category instead |
| `agent …: category … is not promoted` (demote) | it is supervised already |
| `agent … exists` | the agent exists: `edit`, or `delete` first |
| `"…" is not a valid agent name` | [Configuration errors](#configuration-errors) |
| `no configuration on stdin` | `--from -` with nothing piped in |
| `mode "…": want advisory, supervised or bounded` | [chapter 6](06-permissions-and-modes.md#modes) |
| `permissions[N]: server missing`, `permissions[N]: unknown key "…"` | [chapter 6](06-permissions-and-modes.md#permissions) |
| `…: no data for agent …` | the agent has no gateway data: recreate it, or `agent-admin delete` and `create` |
| `the gateway's policy did not load the change within 20s` | [agent-admin says the gateway's policy did not load the change](#policy-not-loaded) |
| `agent … reaches the gateway by its socket; … is for OpenShell agents` | `gateway-token` and `gateway-client-secret` are for OpenShell agents |
| `agent … has an OAuth client (gateway.oauth): set gateway-client-secret, OpenShell mints its tokens` | use `gateway-client-secret` |
| `agent … has no OAuth client (gateway.oauth): set gateway-token` | use `gateway-token`, or add `gateway.oauth` |
| `sandbox.kind: agent … runs with …, not …; delete and create it to change the backend` | the backend is fixed at `create` |
| `CUSTOS_OPENSHELL_USER: …` | the variable names no user ([chapter 11](11-openshell.md#install-openshell)) |
| `importing the provider profile`, `creating the provider`, `minting the first token`, `creating the sandbox` | [OpenShell agents and tokens](#openshell) |
| `model.credential … is not stored for agent …`, `model.credential "…": no credentials directory` | [An agent does not start](#agent-start) |
| `401 invalid token`, `403 insufficient scope` | [OpenShell agents and tokens](#openshell) |
| `agent-openshell-start: sandbox … is not Ready after 120s` | [OpenShell agents and tokens](#openshell) |
| Cockpit: `no access to its socket`, `This needs administrative access`, `Reading the journal needs access`, `You may not decide approval` | [Cockpit problems](#cockpit) |
