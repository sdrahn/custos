---
title: "14. Network access and air-gapped operation"
description: Where inference runs (on-premises, the customer's own cloud account, vendor-hosted), every network flow that leaves a Custos host with the switch that turns it off, installing without a network, and checking that nothing leaves.
tags: [network, egress, air-gapped, offline, on-premises, models, firewall]
---

# 14. Network access and air-gapped operation

Custos needs nothing outside the host for its own work. An agent talks
to two things: mcp-gateway, on the host's Unix socket (or its HTTPS
listener for OpenShell agents), and its model. Where the model runs
decides whether anything leaves the host at all. With a model on the
host or the local network, a Custos host works without any connection
to the internet.

## Where inference runs {#inference}

In this order of preference:

| | Where | How | Leaves your network |
|---|---|---|---|
| 1 | **On-premises**: a model server on the host or the local network (vLLM, llama.cpp's server, Ollama, LiteLLM in front of them) | `provider: openai`, `endpoint:` the server's URL | nothing |
| 2 | **The customer's own cloud account**: a model served in your own tenancy, such as Vertex AI in your Google Cloud project or an OpenAI-compatible server you run there | `provider: vertex` (project and location from the agent's environment), or `provider: openai` with your endpoint | the prompts and tool results, to your own account |
| 3 | **Vendor-hosted**: a model provider's service, such as the Gemini API, or a hosted OpenAI-compatible service | `provider: gemini` with an API key, or `provider: openai` with the service's endpoint and a key | the prompts and tool results, to the vendor |

What goes to the model is what the agent sends: its instruction, the
conversation, the tool descriptions it is shown and the results of its
tool calls. Arguments and results are never in the journal
([chapter 10](10-monitoring-and-audit.md#journal)), but they reach the
model. Choose the backend by what the tools return: a model outside your
network sees it. The keys are in [chapter 5](05-configuration.md#models);
set the host's choice once as the default for new agents
([chapter 5](05-configuration.md#model-defaults)).

## Every flow that leaves the host {#egress}

| Flow | From, to | When | Switch |
|---|---|---|---|
| **inference** | the agent to its model endpoint | every model call | `model.provider` and `model.endpoint` ([above](#inference)). A systemd agent may reach any HTTP port SELinux allows; fence it to the model in a drop-in ([below](#fence)). An OpenShell agent's sandbox reaches only the model endpoint and the gateway (its policy, [chapter 11](11-openshell.md#architecture)) |
| **the managed system's repositories** | mcp-gateway's servers (zypp, tukit) to the system's repositories: SUSE Customer Center, RMT, SMT or a local mirror | when an agent refreshes repositories or installs updates through a tool (the `patch-pilot` templates) | the agent's permissions: without `refresh_repositories`, `update_system`, `confirm_install`, it never asks. The repositories themselves are the system's, as for zypper by hand: point them at a local mirror |
| **other MCP servers** | a server mcp-gateway runs, to whatever it serves | when an agent calls one of its tools | mcp-gateway's server definition (`network:`; without it, a server has no network) and the agent's permissions |
| **token issuing** (OpenShell agents) | OpenShell's gateway to the identity provider's token endpoint (`gateway.oauth.token_url`) | at `set-credential` and at each renewal | an identity provider on your network; or a static `gateway-token` instead of `gateway.oauth` ([chapter 11](11-openshell.md#credential)) |
| **building the agent image** (OpenShell agents) | Podman to `registry.opensuse.org` and the image's repositories | when you build the image | build from a local registry and mirror: `podman build --from <your Leap 16 image> …`, with the image's repositories pointing at your mirror |
| **package updates** of Custos itself | zypper to the configured repositories | when you update | your repositories, as for every package |

What Custos never does:

- **Model updates**: it never downloads a model or weights. The model
  server and its models are yours to install and update.
- **Telemetry**: none. Nothing about agents, activities or approvals is
  sent anywhere.
- **Incident reports**: none. The agents' records and the gateway's
  audit stay in the host's journal; forwarding them is your journal's
  configuration (`systemd-journal-upload`, rsyslog), not Custos's.
- **The Cockpit page** loads nothing from outside the host: its files
  are installed with `custos-cockpit`.
- **Approval notifications** are mcp-gateway's: they go where its
  notification settings send them ([chapter 7](07-approvals.md#notifications)).

## A systemd agent reaches its model, nothing else {#fence}

`agent@.service` denies every address but the host's own
(`IPAddressDeny=any`, `IPAddressAllow=localhost`). Each time the agent
starts, `agent-admin fence NAME` adds, for that start, the addresses of
its model endpoint's host (as the host resolves it now) and the
nameservers in `/etc/resolv.conf`. The gateway's socket is a Unix
socket and stays reachable. SELinux limits the agent's connections to
HTTP ports besides.

```bash
systemctl show -p IPAddressAllow agent@NAME.service   # what this start allows
```

A model endpoint whose addresses change while the agent runs (a cloud
service) gets its new addresses at the agent's next start; the agent
stops itself when idle. To allow more (a proxy, a network), add a
drop-in; it adds to what the unit allows:

```bash
systemctl edit agent@NAME.service
#   [Service]
#   IPAddressAllow=10.0.5.0/24
systemctl restart agent@NAME.service
```

A connection the unit blocks fails with *operation not permitted*, and
the agent's journal shows the model call's error. Should `agent-admin
fence` fail (the endpoint's name does not resolve), the agent reaches
only this host.

## Installing without a network {#offline}

1. Mirror the repositories with mcp-gateway, Custos and their
   dependencies (RMT, or a directory of RPMs with `createrepo_c`), and
   add it on the host (`zypper ar`).
2. Install the packages as in [chapter 2](02-installation.md#install).
3. Install the model server and its model from your media, and start it
   on the host or the local network.
4. Set the model defaults to it, or each agent's `model:`
   ([chapter 5](05-configuration.md#model-defaults)), and create the
   agents ([chapter 4](04-first-agent.md)).
5. For OpenShell agents, build the agent image from a local base image
   ([above](#egress)) and use an identity provider on your network.

## Checking that nothing leaves {#check}

```bash
ss -tnp | grep agent-launcher             # an agent's connections: the model only
journalctl -u 'agent@*' --since today | grep -i 'connect\|dial'
```

An agent's connections go to its model; the gateway is a Unix socket and
does not show. A firewall that drops everything outbound except the
model server's address is the strongest check: the agents work on, and
anything else that tries shows in the firewall's log.
