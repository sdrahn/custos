---
title: "11. Agents in OpenShell sandboxes, the gateway's HTTPS listener and the identity provider"
description: Running an agent in an NVIDIA OpenShell sandbox step by step - OpenShell with rootless Podman, the agent image, mcp-gateway's HTTPS listener, trusting its certificate, the identity provider (Keycloak) and the agent's OAuth client, the issuer list, the agent's configuration, the credential, renewal, editing and checking.
tags: [openshell, sandbox, podman, https, oauth, idp, keycloak, tokens, client credentials]
---

# 11. Agents in OpenShell sandboxes, the gateway's HTTPS listener and the identity provider

An agent with `sandbox: {kind: openshell}` runs inside an NVIDIA OpenShell
sandbox instead of a systemd unit. The sandbox confines its files,
processes and network: its only ways out are the model endpoint and
mcp-gateway. It reaches the gateway over HTTPS with an OAuth token that
the sandbox's supervisor adds; the agent never holds the token. This
backend is a draft in contract version 1 (`CONTRACT.md` §16a):
production use of an identity provider other than the test issuer is
not yet verified by this project's tests ([below](#idp-status)).

## How an OpenShell agent is put together {#architecture}

- **The sandbox** `agent-<name>` runs the image `custos-agent` with
  `agent-launcher` as OpenShell's `sandbox` user. It is created with a
  policy (`/etc/custos/agents/<name>/openshell-policy.yaml`) that allows
  the model endpoint and, through the provider, the gateway, and nothing
  else.
- **The provider** `agent-<name>-gateway` (profile `custos-gateway-<name>`)
  holds the agent's gateway token. The agent sends
  `Authorization: Bearer $AGENT_GATEWAY_TOKEN`, a placeholder; OpenShell's
  proxy replaces it with the real token on the way out. With
  `gateway.oauth`, OpenShell's gateway gets and renews the token itself
  with the client credentials grant.
- **mcp-gateway's HTTPS listener**, reached by the host's own name as
  people reach it (`gw.example.com`), checks the token. The token's subject
  `agent-<name>` is the principal, under the same mode, permissions and
  approvals as an agent on the socket.
- **The relay** `agent-openshell@<name>.service` owns the agent's socket
  `/run/agents/<name>/api.sock` on the host. It starts the sandbox and a
  port forward, passes callers on with a shared secret, and copies the
  agent's journal records from the container's output into the host's
  journal. Cockpit and `agent-admin` work the same as for any agent.

The steps below are in order. Each names what it needs from the earlier
ones.

## Step 1: OpenShell on the host {#install-openshell}

OpenShell 0.1.7 or later, with rootless Podman (OpenShell's RPM default)
or Docker. With rootless Podman, OpenShell's gateway is a
`systemctl --user` service of an ordinary user, here `osh`, whose Podman
holds the sandboxes. As root:

```bash
zypper install podman
# Rootless Podman sets the sandboxes' memory limits: delegate the controllers.
mkdir -p /etc/systemd/system/user@.service.d
printf '[Service]\nDelegate=cpu cpuset io memory pids\n' >/etc/systemd/system/user@.service.d/delegate.conf
systemctl daemon-reload
useradd -m osh
usermod --add-subuids 200000-265535 --add-subgids 200000-265535 osh
loginctl enable-linger osh
systemctl restart user@$(id -u osh).service      # a user manager started before the drop-in lacks the delegation
```

Install OpenShell for `osh` as OpenShell's documentation says (its
installer, or its package). Then register its gateway for root too:
`agent-admin` and the relays run the `openshell` command as root:

```bash
mkdir -p /root/.config && cp -a /home/osh/.config/openshell /root/.config/
openshell gateway list           # the gateway's name, by default "openshell"
openshell status
```

Tell `agent-admin` which user's Podman holds the sandboxes. It writes
that into each agent's `openshell.env` and gives the relay a drop-in that
lets it read that user's containers:

```bash
export CUSTOS_OPENSHELL_USER=osh
```

With Docker, OpenShell's gateway runs as root's, and
`CUSTOS_OPENSHELL_USER` stays unset.

## Step 2: the agent image {#image}

The package ships the image's build file. Build it with the installed
launcher, with the runtime that holds the sandboxes (for rootless
Podman, as `osh`):

```bash
d=$(mktemp -d) && mkdir -p $d/bin && cp /usr/libexec/custos/agent-launcher $d/bin/ && chown -R osh $d
cd / && runuser -l osh -c "podman build -t localhost/custos-agent:latest -f /usr/share/custos/openshell/Containerfile $d"
```

`agent-admin` creates sandboxes from `localhost/custos-agent:latest`, or
from the image `CUSTOS_OPENSHELL_IMAGE` names. Rebuild the image after
updating `custos`. A running sandbox keeps its image: to move an agent
to the new one, set its gateway credential again
(`agent-admin set-credential NAME gateway-client-secret`, or
`gateway-token`), which deletes the sandbox and creates it anew from the
image. `agent-admin edit` recreates a sandbox only when the change
reaches it ([below](#edit)), from the image it was created with.

## Step 3: mcp-gateway's HTTPS listener {#listener}

The agents reach the gateway by the host's own DNS name, the one people
use, for example `gw.example.com`: so an official certificate (from a
public CA or your organization's) covers them. OpenShell's proxy, which
makes the connection for the agent, runs in the host's network: it
resolves the name and connects as the host would, and the connection to
the host's own address stays on the host. Three conditions:

- **The name is in DNS**: with Podman, the sandbox resolves it through
  OpenShell's own DNS, which asks the first nameserver of the host's
  `/etc/resolv.conf` and never reads `/etc/hosts`. A name only in
  `/etc/hosts` resolves for the host, not for the agent.
- **It resolves to the host's network address**, not to a loopback
  address: OpenShell never connects to `127.0.0.0/8`, `::1` or a
  link-local address.
- **The gateway listens on that address**: `listen: ":8443"` listens on
  every address.

Give the gateway the certificate for that name, a port and the listener.
As root, with `gw.crt` (the chain, server certificate first) and `gw.key`:

```bash
install -d -m0750 -o root -g mcp-gateway /etc/mcp-gateway/tls
install -m0640 -g mcp-gateway gw.crt /etc/mcp-gateway/tls/cert.pem
install -m0640 -g mcp-gateway gw.key /etc/mcp-gateway/tls/key.pem
restorecon -R /etc/mcp-gateway/tls
semanage port -a -t mcp_port_t -p tcp 8443 2>/dev/null || semanage port -m -t mcp_port_t -p tcp 8443
```

```yaml
# /etc/mcp-gateway/gateway.d/30-agents-https.yaml
http:
  listen: ":8443"
  cert_file: /etc/mcp-gateway/tls/cert.pem
  key_file: /etc/mcp-gateway/tls/key.pem
  issuer: https://idp.example.com/realms/mcp           # your identity provider (step 5)
  audience: https://gw.example.com:8443/mcp            # = the agents' gateway.url
  scopes: [mcp-agents]                                 # optional: a scope every token must have
```

```bash
mcp-gateway --check && systemctl restart mcp-gateway.service   # the listener starts with the gateway
mcp-gateway-admin doctor                                       # TLS, identity provider, firewall
```

The gateway fetches the identity provider's keys from
`<issuer>/.well-known/openid-configuration`. Its HTTPS chapter
(mcp-gateway user guide, chapter 5, "Remote agents (HTTPS)") explains
every key. The agents' connections stay on the host; with firewalld,
open the port only for people or remote agents that use it.

**Without a name for the host** (a test system): the sandbox also
reaches the host as `host.openshell.internal`, OpenShell's name for the
host's loopback. Only a private CA can issue a certificate for it
(`DNS:host.openshell.internal`), which OpenShell must then trust
([step 4](#trust)), and it becomes the gateway's `http.audience` and the
agents' `gateway.url`.

`agent-admin check NAME` (and `create`, `edit`) notes an OpenShell
agent by `host.openshell.internal`, and one whose name is not in DNS (it
asks the nameserver as OpenShell does) or resolves to a loopback
address.

## Step 4: OpenShell trusts the gateway's certificate {#trust}

OpenShell's proxy terminates the agent's TLS to swap the token in, then
connects to the gateway itself. It checks the gateway's certificate in
each sandbox's supervisor, not with the image's trust store: against the
public root CAs and the host's own trust store. A certificate from a
public CA needs nothing here. A CA that no public root signs (your
organization's, or a test system's for `host.openshell.internal`) goes
into OpenShell's gateway configuration
(`~osh/.config/openshell/gateway.toml`), which needs its format version:

```toml
[openshell]
version = 2

[openshell.supervisor.network]
additional_ca_bundle = "/home/osh/.config/openshell/mcp-gateway-ca.pem"
```

```bash
install -m0644 -o osh ca.pem /home/osh/.config/openshell/mcp-gateway-ca.pem
runuser -u osh -- openshell-gateway config preflight
systemctl --user --machine=osh@ restart openshell-gateway
```

Without it, the agent's calls fail with a certificate error in the
sandbox ([chapter 12](12-troubleshooting.md#openshell)).

## Step 5: the identity provider and the agent's OAuth client {#idp}

Each OpenShell agent is an OAuth client of your identity provider, using
the client credentials grant. Whatever the provider, its tokens for the
agent must have:

| Claim | Value |
|---|---|
| `iss` | the gateway's `http.issuer` |
| `sub` | `agent-<name>`: the principal the gateway uses. The gateway takes the principal from `sub` only |
| `aud` | the gateway's `http.audience`, e.g. `https://gw.example.com:8443/mcp` (the grant sends no audience: the provider must add it) |
| `scope` | every scope of `http.scopes`, if the gateway requires any |
| `exp` | a short lifetime (minutes): OpenShell renews the token before it expires |

The token must not map the agent to a person's local account: the
gateway maps a token to a local user only for persons' accounts and
refuses system accounts, so an agent's subject stays `agent-<name>`.

`agent-admin idp-client NAME` prints what the agent's client must be at
your provider: its client id (`gateway.oauth.client_id`, or
`agent-NAME`), the grant, and the claims above with the values of this
host's `/etc/mcp-gateway/gateway.yaml`. It also says whether the issuer
is listed for agents yet ([step 6](#issuers)).

### Keycloak {#keycloak}

In the realm the gateway's `http.issuer` names (mcp-gateway user guide,
chapter 15, sets one up for people), `agent-admin idp-client NAME
--keycloak` prints a script that creates the agent's client with
Keycloak's admin CLI, `kcadm.sh`. Run it where `kcadm.sh` is logged in
to the realm's Keycloak with a user that may manage clients:

```bash
agent-admin idp-client patch-pilot --keycloak >patch-pilot-client.sh
less patch-pilot-client.sh                     # read it first
kcadm.sh config credentials --server https://idp.example.com --realm master --user admin
bash patch-pilot-client.sh >client-secret.txt  # KCADM=/path/to/kcadm.sh if it is not in PATH
```

The script

1. creates a client scope for each scope of `http.scopes` that the realm
   lacks (*Include in token scope* on);
2. creates the client `agent-patch-pilot`: *Client authentication* and
   *Service accounts roles* on, *Standard flow*, *Direct access grants*
   and *Implicit flow* off, *Full scope allowed* off, *Access Token
   Lifespan* 5 minutes;
3. gives it two mappers: a *Hardcoded claim* `sub` = `agent-patch-pilot`
   (without it, Keycloak's `sub` is the service account's id) and an
   *Audience* with the gateway's `http.audience`;
4. removes the realm's other default client scopes from the client
   (Keycloak 26's `basic` maps the service account's id to `sub`) and
   adds the scopes of `http.scopes` as *Default*;
5. prints the client's secret, the only thing on its standard output:
   `client-secret.txt` is the secret for [step 8](#credential); keep it
root's (`chmod 600`) and delete it once it is set.

It stops at the first error; if the client exists already, `kcadm.sh`
refuses to create it again: delete it (`kcadm.sh delete clients/ID -r
REALM`) or change it in the admin console. The same settings by hand in
the admin console: *Clients* → *Create client* with the settings of 2,
then in *Client scopes* → `agent-patch-pilot-dedicated` the two mappers
of 3, and in *Client scopes* the scopes of 4.

Once the agent is created ([step 8](#credential)), check the client
with a real token before giving OpenShell its secret:

```console
# agent-admin check-token patch-pilot < client-secret.txt
ok   iss https://idp.example.com/realms/mcp
ok   the issuer is listed for agents (/etc/mcp-gateway/policy/agents/_http.yaml)
ok   sub agent-patch-pilot
ok   aud includes https://gw.example.com:8443/mcp
ok   scope mcp-agents
ok   exp in 4m59s
ok   no preferred_username (the gateway's local_user_claim)
agent patch-pilot's token is what the gateway expects
```

`check-token` gets a token as OpenShell will: the client credentials
grant at `gateway.oauth.token_url`, with its client id and scopes and
the secret from standard input. It checks the token's claims against the
gateway's `http` settings and `_http.yaml`. A `FAIL` line says what is
wrong and, where it knows, how to fix it at the provider; the command
then exits 1. It does not check the token's signature, which the gateway
does with the issuer's keys. It works with any provider, not only
Keycloak.

### Other identity providers and a static token {#other-idp}

Any OAuth 2.0 provider works whose client credentials tokens can carry
those claims. Without one, give the agent a static token instead
(`agent-admin set-credential NAME gateway-token`, and no `gateway.oauth`
in its configuration). OpenShell does not renew a static token: replace
it before it expires. `custos-tests` ships a test issuer
(`/usr/libexec/custos/tests/testidp`) for test systems; it is not for
production.

### What is verified {#idp-status}

CI runs this backend with real OpenShell, on Docker and on rootless
Podman with SELinux enforcing. It uses mcp-gateway's HTTPS listener and
the test issuer: tokens minted and renewed by OpenShell with the client
credentials grant, calls under the agent's mode and approvals. A further
CI job runs a real Keycloak (26): the script of `agent-admin idp-client
--keycloak` creates an agent's client in a new realm, and `agent-admin
check-token` gets a token with its secret that has every claim the
gateway relies on.

## Step 6: the gateway trusts the issuer for agents {#issuers}

The gateway accepts an `agent-*` subject over HTTPS only from an issuer
listed for agents. Any other issuer's `agent-*` subject is denied
everything, so a people's realm cannot mint agent identities by
accident:

```yaml
# /etc/mcp-gateway/policy/agents/_http.yaml
_http:
  issuers: ["https://idp.example.com/realms/mcp"]
```

The file is the package's (`%config(noreplace)`, empty list by
default). The gateway's policy engine reads it within seconds. Check:

```bash
curl -s --unix-socket /run/mcp-gateway/opa.sock http://opa/v1/data/mcp/agents/_http/issuers
```

Listing an issuer says that it gives `agent-*` subjects to agents only.
In the identity provider, keep people from having such names.

## Step 7: the agent's configuration {#configuration}

```yaml
version: 1
name: patch-pilot
description: Keeps the host patched, in a sandbox.
model:
  provider: openai
  name: qwen3-32b
  endpoint: http://host.openshell.internal:8000/v1   # the host's model server, seen from the sandbox
instruction: You keep this host patched. Look before you change anything.
gateway:
  url: https://gw.example.com:8443/mcp               # the host's name; = the gateway's http.audience
  servers: [zypp, systemd]
  oauth:
    token_url: https://idp.example.com/realms/mcp/protocol/openid-connect/token
    client_id: agent-patch-pilot
    scopes: [mcp-agents]
    refresh_before_seconds: 60
sandbox: {kind: openshell, openshell_gateway: openshell}
```

- `gateway.url` must be `https://` (only the host's loopback may be
  `http://`, for tests). `gateway.socket` and `gateway.url` exclude each
  other. A loopback URL (`localhost`, `127.0.0.1`) becomes
  `host.openshell.internal` in the sandbox ([step 3](#listener)).
- `gateway.oauth` is only for OpenShell agents: OpenShell's gateway gets
  the tokens. Without it, the agent uses a static token
  ([above](#other-idp)).
- `sandbox.openshell_gateway` names OpenShell's gateway as registered for
  root (default `openshell`).
- The sandbox's policy allows the model endpoint, for `agent-launcher`
  only.

## Step 8: creating the agent and its credential {#credential}

```bash
export CUSTOS_OPENSHELL_USER=osh                       # rootless Podman (step 1)
agent-admin create patch-pilot --from patch-pilot.yaml
agent-admin permissions patch-pilot < perms.yaml
agent-admin check-token patch-pilot < client-secret.txt   # step 5: the token has what the gateway needs
agent-admin set-credential patch-pilot gateway-client-secret < client-secret.txt
```

`create` writes the policy and the provider profile, imports the
profile into OpenShell, and says what to set next. `set-credential …
gateway-client-secret` then:

1. creates the provider `agent-patch-pilot-gateway` with runtime
   credentials;
2. gives OpenShell's gateway the OAuth client (the secret through the
   environment, never on a command line);
3. gets the first token: a refused client fails here;
4. creates the sandbox and enables the relay `agent-openshell@patch-pilot.socket`.

With a static token: `agent-admin set-credential patch-pilot gateway-token < token.txt`.
The secret is not kept by `agent-admin`: to recreate the provider later,
set it again.

## How the token is renewed {#oauth}

OpenShell's gateway renews the token `refresh_before_seconds` before it
expires (OpenShell's default if unset) and the sandbox's placeholder
always resolves to the current one, so the agent needs no restart.
While a renewal happens, OpenShell's proxy can answer a request with
`500`; the agent retries for a few seconds and reconnects. Check the
renewal:

```bash
openshell -g openshell provider refresh status agent-patch-pilot-gateway
```

## Step 9: checking the agent end to end {#check}

```bash
systemctl status agent-openshell@patch-pilot.service
curl -s --unix-socket /run/agents/patch-pilot/api.sock http://agent/v1/status | jq '{lifecycle, gateway}'
curl -s --unix-socket /run/agents/patch-pilot/api.sock http://agent/v1/activities \
  -X POST -d '{"prompt": "List the pending updates.", "wait": true}' | jq '{state, summary}'
journalctl -u mcp-gateway.service -o cat --since -10min | grep '"audit":true' | grep agent-patch-pilot
```

The gateway's records show `"sub":"agent-patch-pilot"` and the issuer.

`agent-admin check-token NAME < secret` ([step 5](#keycloak)) checks
the client's tokens without the gateway. To see what the gateway itself makes
of a token, fetch one with the client's secret and ask the gateway:

```bash
curl -s https://idp.example.com/realms/mcp/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=agent-patch-pilot \
  --data-urlencode client_secret@client-secret.txt -d scope=mcp-agents | jq -r .access_token >/root/agent.jwt
mcp-gateway-admin setup http --token /root/agent.jwt      # the principal it sees, or why it refuses
rm /root/agent.jwt
```

## Editing, stopping and deleting OpenShell agents {#edit}

`agent-admin edit` applies the new configuration to the sandbox:

| The edit changes | `edit` does |
|---|---|
| nothing the sandbox gets | restarts the relay (`try-restart agent-openshell@NAME.service`) |
| the configuration, the model endpoint, `gateway.url` or the OAuth token URL, scopes or `refresh_before_seconds` | rewrites the policy and profile, and recreates the sandbox with its provider and image (up to 2 minutes). The state inside it (sessions) goes. It then starts what ran before |
| `gateway.oauth` added or removed, or its `client_id` | stops the agent, deletes the sandbox and provider, and asks for `set-credential` again |
| `sandbox.kind` or `sandbox.openshell_gateway` | refused: delete and create the agent |

`systemctl stop agent-openshell@NAME.service` stops the relay and the
sandbox. `agent-admin stop-all` stops every relay and sandbox, and the
gateway denies them like any agent. `agent-admin delete NAME` removes the
sandbox, the provider and its profile, the relay's drop-in and the
agent's files.
