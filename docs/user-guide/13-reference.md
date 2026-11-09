---
title: "13. Reference"
description: Programs, files and directories, systemd units, users and groups, environment variables, SELinux types and booleans, ports, and where the interfaces are defined.
tags: [reference, files, units, paths, selinux, environment]
---

# 13. Reference

Lists for looking things up. The interfaces (control API, journal
fields, gateway data, AG-UI events) are defined in the contract,
`CONTRACT.md`, installed beside this guide.

## Programs {#programs}

| Program | Runs as | Does |
|---|---|---|
| `/usr/sbin/agent-admin` (also `/usr/libexec/custos/agent-admin`) | root; anyone for `check FILE`, `show`, `categories`, `templates`, `draft`, reading `defaults` and listing `issuers`; root or `systemd-journal` for `readiness` and the review queue | administration ([chapter 9](09-administration.md#agent-admin)) |
| `/usr/libexec/custos/agent-launcher` | `agent-<name>` (`agent_t`) | the agent: `agent-launcher serve` |
| `/usr/libexec/custos/agent-delegate` | the delegating agent's user (`mcpsrv_agents_t`), started by mcp-gateway | the `agents` server: delegation |
| `/usr/libexec/custos/agent-openshell-relay` | root (`agent_relay_t`) | an OpenShell agent's socket and journal |
| `/usr/libexec/custos/agent-openshell-logs` | the relay | follows the sandbox container's output |
| `/usr/libexec/custos/agent-openshell-start` | root, before the relay (`agent-openshell@.service`) | starts the agent's sandbox and waits until it is ready |

## Files and directories {#files}

| Path | Owner, mode | Contents |
|---|---|---|
| `/etc/custos/agents/<name>/` | root:agent-`<name>` 0750 | the agent's configuration directory |
| `/etc/custos/agents/<name>/agent.yaml` | root:agent-`<name>` 0640 | the configuration ([chapter 5](05-configuration.md)) |
| `/etc/custos/agents/<name>/openshell.env`, `openshell-policy.yaml`, `openshell-profile.yaml`, `relay.secret` | root | OpenShell agents: relay settings, sandbox policy, provider profile, the relay's secret (0600) |
| `/etc/mcp-gateway/policy/agents/<name>.yaml` | root 0644 | the agent's mode, pause and permissions ([chapter 6](06-permissions-and-modes.md)), signed on a sealed host |
| `/etc/mcp-gateway/policy/agents/_control.yaml` | root 0644 | the emergency stop |
| `/etc/mcp-gateway/policy/agents/_http.yaml` | root 0644 | token issuers trusted for agents ([chapter 11](11-openshell.md#issuers)), written by `agent-admin issuers`, signed on a sealed host |
| `/etc/mcp-gateway/policy/agents/_seal.yaml` | root 0644 | the seal's public key, and whether it covers the agents' data ([chapter 6](06-permissions-and-modes.md#diagnostics-only)) |
| `/etc/credstore.encrypted/agent-<name>.<cred>` | root 0600 | credentials (`systemd-creds`) |
| `/etc/sysusers.d/agent-<name>.conf` | root 0644 | the agent's user |
| `/etc/systemd/system/agent-schedule@<name>_<schedule>.timer.d/schedule.conf` | root | a schedule's calendar |
| `/etc/systemd/system/agent@<name>.service.d/` | root | drop-ins for the agent's unit (Cockpit writes only `50-cockpit.conf`) |
| `/etc/systemd/system/agent-openshell@<name>.service.d/openshell-user.conf` | root | OpenShell agents: the OpenShell user's Podman directories for the relay's log source |
| `/usr/lib/systemd/system/agent@.*`, `agent-schedule@.*`, `agent-openshell@.*`, `custos-demote.*` | root 0644 | the unit templates ([below](#units)) |
| `/usr/lib/sysusers.d/custos.conf`, `/usr/lib/tmpfiles.d/custos.conf` | root 0644 | the group `agent-clients`; the directories |
| `/var/lib/agents/<name>/sessions.db` | agent-`<name>` 0600 | chat sessions |
| `/run/agents/<name>/api.sock` | root:agent-clients 0660 | the agent's control API |
| `/usr/share/mcp-gateway/policy/custos/agents.rego` | root 0644 | the autonomy policy module |
| `/usr/share/custos/gateway/servers.d/agents.yaml` | root 0644 | the `agents` server, to copy to `/etc/mcp-gateway/servers.d/` |
| `/usr/share/custos/categories.d/*.yaml` | root 0644 | the task categories ([chapter 6](06-permissions-and-modes.md#categories)) |
| `/etc/custos/defaults.yaml` | root 0644 | the model for new agents ([chapter 5](05-configuration.md#model-defaults)); none: the templates' |
| `/etc/custos/promotion.yaml` | root 0644 | readiness thresholds ([chapter 6](06-permissions-and-modes.md#readiness)); none: the defaults |
| `/etc/custos/categories.d/*.yaml` | root 0644 | your categories: a shipped name replaces, an empty file disables |
| `/usr/share/custos/examples/patch-pilot/` | root 0644 | the example agent and permissions |
| `/usr/share/custos/templates/<template>/` | root 0644 | the agent templates ([chapter 9](09-administration.md#templates)), and `exec.d-disk-usage.yaml` |
| `/etc/custos/templates/<template>/` | root 0644 | your templates: a shipped name replaces, one without `template.yaml` hides |
| `/usr/share/custos/openshell/Containerfile` | root 0644 | the OpenShell agent image |
| `/usr/share/cockpit/custos/` | root 0644 | the Cockpit page |
| `/usr/share/custos/docs/` | root 0644 | this guide and the contract (`custos-docs`) |
| `/usr/share/mcp-docs/collections.d/custos.yaml` | root 0644 | the collection file for mcp-docs (`custos-docs`) |

## systemd units {#units}

| Unit | Does |
|---|---|
| `agent@<name>.socket` | listens on the agent's socket; starts the service on a request |
| `agent@<name>.service` | runs the agent; exits after 10 minutes idle |
| `agent-schedule@<name>_<schedule>.timer` | starts the schedule's service at its calendar |
| `agent-schedule@<name>_<schedule>.service` | runs `agent-admin trigger <name>_<schedule>` |
| `custos-demote.timer`, `custos-demote.service` | the demotion evaluator (`agent-admin evaluate`), every 5 minutes; enabled by `agent-admin promote` |
| `agent-openshell@<name>.socket`, `.service` | an OpenShell agent's socket and relay |

## Users and groups {#users}

| Name | What |
|---|---|
| `agent-<name>` | an agent's system user; no password, no login shell |
| `agent-clients` | people who supervise agents; agents are members too, for delegation |
| `mcp-users` | the gateway's socket group; agents are members |
| `systemd-journal` | reading the journal: lets a person run `agent-admin readiness` and list the review queue without root |
| `wheel` | in the gateway's default role data, the role `admin` (may approve); in Cockpit, sudo for administrative access |

## Environment variables {#environment}

| Variable | Read by | Meaning |
|---|---|---|
| `CUSTOS_OPENSHELL_IMAGE` | `agent-admin` | the image for new sandboxes (default `localhost/custos-agent:latest`) |
| `CUSTOS_OPENSHELL_USER` | `agent-admin` | the user whose rootless Podman holds the sandboxes |
| `SUDO_USER`, `USER` | `agent-admin` | who acted: `paused_by`, the emergency stop's `by`, `promoted_by`, `demoted_by` and the review's verdicts |
| `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`, `GOOGLE_APPLICATION_CREDENTIALS` | the agent (a unit drop-in) | Vertex AI ([chapter 5](05-configuration.md#vertex)) |

## SELinux {#selinux}

| Type or boolean | What |
|---|---|
| `agent_t` (`agent_exec_t`) | the agents; reach the gateway's MCP socket and HTTP ports, read their own credentials (`init_var_run_t` in `/run/credentials/<unit>`) |
| `mcpsrv_agents_t` | the delegation bridge; reaches the agents' sockets |
| `agent_relay_t` | the OpenShell relay |
| `agent_conf_t`, `agent_var_lib_t`, `agent_runtime_t`, `agent_sock_t` | `/etc/custos`, `/var/lib/agents`, `/run/agents`, the sockets |
| `agent_connect_any` (off) | lets `agent_t` connect to any port |

## Ports {#ports}

| Port | Who | Note |
|---|---|---|
| 9090 | Cockpit | the web console |
| the model server's | the agents | must be an HTTP port for SELinux, or `agent_connect_any` |
| 8443 (your choice) | mcp-gateway's HTTPS listener | only for OpenShell agents; label `mcp_port_t` |
| 47000–47999 | OpenShell agents, inside the sandbox | the agent's control API behind the relay's forward |

## Where the interfaces are defined {#contract}

| Interface | `CONTRACT.md` |
|---|---|
| identity, people and the control plane | §2 |
| discovery, sandbox backends | §3, §3a |
| activities, the control API | §4, §5 |
| autonomy modes, the agents' gateway data | §6 |
| task categories, promotion, readiness, review, demotion | §6a |
| approvals, intervention, delegation | §7, §8, §9 |
| journal fields and `MESSAGE_ID`s | §10 |
| AG-UI (chat) | §11 |
| files, configuration, templates, defaults for new agents, units, launcher, OpenShell, `agent-admin`, SELinux, packages | §13 to §19 (templates §14a, defaults §14b) |
