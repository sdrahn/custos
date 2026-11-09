---
title: "2. Installing Custos"
description: Requirements, the packages, installing them with zypper on SLES 16 or Leap 16, SELinux, Cockpit, and checking the installation.
tags: [installation, packages, zypper, selinux]
---

# 2. Installing Custos

This chapter installs Custos and mcp-gateway on one host and checks that
the parts found each other. Creating the first agent is
[chapter 4](04-first-agent.md).

## Requirements {#requirements}

| What | Version or note |
|---|---|
| operating system | SUSE Linux Enterprise Server 16.0 or 16.1, openSUSE Leap 16.0 or 16.1 (x86_64, aarch64; SLES also s390x, ppc64le) |
| mcp-gateway | 1.16 or later, with its SELinux module (`mcp-gateway-selinux`) where SELinux runs |
| SELinux | enforcing is supported and tested; `custos-selinux` is required where `selinux-policy-targeted` is installed |
| systemd | the system's; agents use `systemd-creds` for secrets |
| a model | an OpenAI-compatible server (for example a local vLLM or llama.cpp server), Google Gemini, or Vertex AI ([chapter 5](05-configuration.md#models)) |
| Cockpit | for the web page: `cockpit` (Cockpit 137 or later) |
| NVIDIA OpenShell | only for agents in OpenShell sandboxes: 0.1.7 or later ([chapter 11](11-openshell.md)) |

With a model server on the host or the local network, an agent needs
nothing from the internet: Custos works air-gapped.

## The packages {#packages}

| Package | Installs | Install it |
|---|---|---|
| `custos` | `agent-launcher`, `agent-admin`, the units (with `custos-demote.timer`, the demotion evaluator), the gateway policy module, the `agents` server definition, the task categories' catalog, the agent templates, the example agent, the OpenShell relay and image files | always |
| `custos-selinux` | the SELinux module `custos` | where SELinux runs (pulled in automatically with `selinux-policy-targeted`) |
| `custos-cockpit` | the Cockpit page *Custos* (`/usr/share/cockpit/custos`) | on hosts people supervise through Cockpit |
| `custos-docs` | this guide and the contract, with a collection file for mcp-docs | where people or agents read the documentation on the host (pulled in automatically where `custos` and `mcp-docs` are both installed) |
| `custos-diagnostics` | `custos-setup`, the bundled health-check and diagnostics install ([below](#diagnostics)) | when you choose the diagnostics-only install; the installer offers it |
| `custos-tests` | the functional tests (openQA) | test systems only: they change the system for good |

`custos` requires `mcp-gateway`. `custos-cockpit` requires `custos` of
the same version and `cockpit-bridge`, and recommends `mcp-gateway-cockpit`, mcp-gateway's own
page, which is useful for the gateway's settings.

## Adding the repository {#repository}

The packages are built on the Open Build Service (OBS). Add the
repository your organization or project publishes them in, for the
distribution of the host, together with mcp-gateway's if that is a
different one. For example:

```bash
zypper addrepo --refresh https://download.opensuse.org/repositories/<project>/<distribution>/ custos
zypper refresh
```

How to build them in your own OBS project is in `packaging/suse/README.md`
of the source repository.

## Installing the packages {#install}

As root, for a host that runs agents and is supervised in Cockpit:

```bash
zypper install mcp-gateway mcp-gateway-selinux custos custos-selinux custos-cockpit cockpit custos-docs
systemctl enable --now mcp-gateway.service
systemctl enable --now cockpit.socket
```

Without Cockpit, leave out `custos-cockpit` and `cockpit`. On a host
without SELinux, leave out the two `-selinux` packages.

The packages create the group `agent-clients` (people who may supervise
agents) and the directories `/etc/custos/agents`,
`/etc/custos/categories.d`, `/etc/custos/templates` (your own
categories and templates), `/var/lib/agents` and `/run/agents`. They install the autonomy policy as
`/usr/share/mcp-gateway/policy/custos/agents.rego`, which mcp-gateway's
policy engine loads together with the gateway's own policy, and the
files `/etc/mcp-gateway/policy/agents/_control.yaml` (the emergency
stop, off) and `_http.yaml` (no token issuers for agents).

## The bundled diagnostics install {#diagnostics}

The installer offers a health-check and diagnostics agent as an opt-in
choice. It installs `custos-diagnostics` and runs `custos-setup`, which
you can also run yourself on a freshly installed host:

```bash
zypper install custos-diagnostics
read -rs KEY && printf '%s' "$KEY" >/root/model-key; unset KEY     # if the model needs a key
custos-setup --diagnostics-only --backend local-network \
  --endpoint http://ai.example.com:8000/v1 --model qwen3-32b --credential-file /root/model-key
shred -u /root/model-key
```

`--backend` is where the model runs, in this order of preference
([chapter 14](14-network.md#inference)):

| Backend | Means | Needs |
|---|---|---|
| `on-host` | a model server on this host (OpenAI-compatible) | `--endpoint` if not `http://127.0.0.1:8000/v1` |
| `local-network` | a model server on your network (OpenAI-compatible) | `--endpoint` |
| `customer-cloud` | your own cloud account | `--provider vertex`, or `--provider openai` with `--endpoint` |
| `vendor` | a vendor-hosted service | `--credential-file`; `--provider gemini` by default |

It then:

1. makes the host **diagnostics-only**: only the read-only
   `diagnostics` category is usable until you enable more
   ([chapter 6](06-permissions-and-modes.md#diagnostics-only));
2. sets the model defaults;
3. creates the agent `health-check` from its template
   ([chapter 9](09-administration.md#templates)): it runs the diagnostic
   playbooks daily and weekly, and you run any of them from Cockpit;
4. stores the key as the agent's `model-key`;
5. copies the playbooks' commands to `/etc/mcp-gateway/exec.d/`, and the
   server `custos-net` to `/etc/mcp-gateway/servers.d/`; with
   `--restarts`, and `mcp-gateway-restarts-server` installed, it links
   that server's profile there too, for the services `update-status`
   finds waiting for a restart. The server holds `CAP_SYS_PTRACE`, so
   offering it is a choice of its own ([chapter 9](09-administration.md#playbooks)).

Each step prints `done`, `already` or `skipped` with why: running it
again changes only what differs, and never goes back to diagnostics-only
once you enabled more. It refuses a host that already has agents with
other permissions, writing nothing. Without `--diagnostics-only` it does
nothing: the full install needs no setup.

## SELinux {#selinux}

SLES 16 and Leap 16 run SELinux enforcing. `custos-selinux` loads the
module `custos` and labels the programs: `agent-launcher` runs as
`agent_t`, the delegation bridge `agent-delegate` as `mcpsrv_agents_t`.
Installed before the module, files and directories are relabelled by
the package's scripts.

Check that the module is loaded and the labels are right:

```bash
semodule -l | grep -x custos
matchpathcon /usr/libexec/custos/agent-launcher     # …agent_exec_t
ls -Zd /run/agents /var/lib/agents /etc/custos     # agent_runtime_t, agent_var_lib_t, agent_conf_t
```

What an agent may connect to (its model's port) is in
[chapter 5](05-configuration.md#selinux-and-the-models-port).

## Checking the installation {#check-installation}

```bash
rpm -q mcp-gateway custos custos-selinux
systemctl is-active mcp-gateway.service             # active
test -S /run/mcp-gateway/mcp.sock && echo gateway socket ok
getent group agent-clients
agent-admin check /usr/share/custos/examples/patch-pilot/agent.yaml && echo configuration ok
agent-admin show                                    # {"agents": {}, "control": {"stopped": false}}
mcp-gateway-admin doctor                            # the gateway's own self-check
```

`agent-admin show` reads the agents' gateway data. On a fresh host it
lists no agents, and the emergency stop is off. If any command fails,
see [chapter 12](12-troubleshooting.md#installation).

## Giving people access {#people}

Supervisors are people in `agent-clients`: they see the agents, their
activities and their journal records, and can chat with them. Approvers
and administrators need more ([chapter 8](08-cockpit.md#roles)):

```bash
usermod -aG agent-clients alice          # sees and chats with agents
usermod -aG wheel alice                  # Cockpit's administrative access (sudo) and, by the
                                         # gateway's default role data, the right to approve
```

A person logs in to Cockpit with their own account. Never log in as root
to approve: the gateway would record root as the approver
([chapter 7](07-approvals.md#who-is-recorded)).

## Upgrading {#upgrading}

```bash
zypper update custos custos-selinux custos-cockpit custos-docs
systemctl daemon-reload
```

A running agent keeps the old program until it exits: agents exit after
10 minutes without work, and the next request starts the new one. To
switch at once, `systemctl restart agent@<name>.service` (it ends the
activities in flight). Configuration and gateway data in `/etc` are kept.
Within contract version 1, every name, path and field stays: upgrades
do not break the Cockpit page or scripts that use the control API.

## Removing Custos {#removing}

Remove the agents first (each `agent-admin delete NAME`,
[chapter 9](09-administration.md#delete)), then the packages:

```bash
zypper remove custos-cockpit custos-docs custos custos-selinux
```

The agents' system users stay (uids are not reused while files may
belong to them). Changed `/etc/mcp-gateway/policy/agents/_control.yaml`
and `_http.yaml` are kept as `.rpmsave`. Your own files stay where they
are: `/etc/custos/defaults.yaml`, `/etc/custos/promotion.yaml` and what
you put in `/etc/custos/categories.d` and `/etc/custos/templates`.
