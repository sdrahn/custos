# Security review before 0.9.0

A focused review of what 0.9.0 changes since `v0.8.0`. It covers `main`
at `d675c48`, and the fixes of T1–T6 (#251):

- the relay's SELinux type for root's OpenShell registration
  (`agent_openshell_home_t`, #238), with its labelling by the package
  and by `agent-admin openshell-setup`;
- `openshell-setup` waiting for the systemd driver's helper (#243);
- the name of an OpenShell agent checked against OpenShell's limit
  (#245);
- `agent-admin idp-client NAME --keycloak --apply`, which logs in to
  Keycloak's admin API as an administrator and makes the agent's client
  (#247), and Cockpit's form for it (#249);
- every shipped template allowing OpenShell (#248);
- the guide (#240).

The threat model and the yardstick are those of
[the 0.5 review](security-review-0.5.md): a compromised or
prompt-injected agent or model, a malicious tool output, a local user
without privileges (OpenShell's gateway user among them), and root on
one host. New in 0.9 is a secret that is not the host's: the password of
a Keycloak administrator, typed for `--apply`, which can manage the
whole realm.

A reviewer who had not written the changes read the difference between
the two versions, and the code it calls; the author checked each finding
against the code before fixing it, with a test for each of T1–T4.

**Where it stands.** The two medium findings and the low ones were in
`--apply`, which no release has had; they are fixed before 0.9.0.

| Finding | Severity | Status |
|---|---|---|
| T1 | medium | fixed (#251) |
| T2 | medium | fixed (#251) |
| T3 | low | fixed (#251) |
| T4 | low | fixed (#251) |
| T5 | low | fixed (#251) |
| T6 | info | fixed (#251) |
| T7 | info | done on the release branch |

## T1 — medium — the administrator's password over plain HTTP

*Where:* `internal/admin/idp/apply.go`, the address check, at `d675c48`.

`--apply` allowed plain HTTP to this host's loopback, and the guide
said so. A local user who held that port (Keycloak stopped or
restarting, published on another port, or bound on IPv4 only while Go
dials `::1` for `localhost` first) received the form with the
administrator's user name and password, usually of the realm `master`:
all of Keycloak, including the power to mint `sub: agent-*` tokens for
any agent.

*Fix (#251):* the password goes over HTTPS only. The package's tests
set an unexported switch for their Keycloaks on loopback; `agent-admin`
never sets it. A test shows that plain HTTP to a loopback Keycloak is
refused before any request.

## T2 — medium — any client of the configured id was deleted

*Where:* `apply.go`, `makeClient`, at `d675c48`.

The client id came from `gateway.oauth.client_id`, which `create --from`
or an edit can set to anything; `--apply` deleted every client of that
id before making its own. With another agent's id, that agent lost its
tokens; with `account` or `admin-cli`, the realm lost its own client. The
delete came before the new client and the token check, so a failure
after it left the agent with neither.

*Fix (#251):* only `agent-NAME`, the agent's own client: another id is
refused before Keycloak is asked anything. A test shows the refusal, and
that nothing is deleted.

## T3 — low — redirects

*Where:* `apply.go`, the HTTP client.

Go sends a form on with a 307 or 308, to another host and from HTTPS
to HTTP; a misconfigured or compromised proxy in front of Keycloak could
have had the password. *Fix (#251):* the admin API's client follows no
redirect; a test redirects the login and the password does not arrive.

## T4 — low — the issuer listed for a token that fails

*Where:* `apply.go`.

Listing the gateway's own issuer for agents is not an escalation: the
operator chose it, the command says so, and a sealed host signs it. But
it stayed listed when the token check then failed. *Fix (#251):* it is
taken off again then; a test shows it.

## T5 — low — the terminal prompt

*Where:* `cmd/agent-admin/cmd_idp.go`.

If `stty -echo` failed, the password was read with echo on; Ctrl-C left
the terminal without echo. *Fix (#251):* it refuses to read the password
when echo cannot be turned off (the password can come on standard input
instead), and turns echo on again on SIGINT, SIGTERM and SIGHUP. No new
module. Not tested automatically: it needs a terminal.

## T6 — info — IDs in paths

IDs from Keycloak's answers went into URL paths unescaped; only below
Keycloak's own address, so no request could go elsewhere. *Fix (#251):*
escaped.

## T7 — info — the release branch

CI on `main` builds mcp-gateway from its `main`, and `_service` builds
from `main`. The release branch pins both, as for 0.8 (its first commit;
`tools/check-release` refuses it otherwise).

## Open, older than 0.9

- `sandbox.openshell_gateway` has no syntax check (review 0.8, S8).
- `gateway.oauth.token_url` may be plain HTTP on loopback, for tests;
  the agent's client secret goes there when OpenShell or `check-token`
  gets a token. The stakes are lower than T1's (one agent's client, not
  the realm's administrator), and it predates 0.9.

## Checked and found sound

- **Secrets in argv, environment and output:** the password comes on
  stdin, from the terminal and from Cockpit alike. Errors carry the user
  name and Keycloak's answer, never the password or the secret. The
  secret goes only to the token check and to `set-credential`, and
  through the environment to OpenShell.
- **Cockpit's form:** `cockpit.spawn` takes an argv, with no shell,
  through the root bridge. Each typed value follows a flag that takes
  one, so a value starting with `-` is not a flag. The password field is
  cleared on success and failure, and the secret never reaches the page.
- **SELinux:** `agent_relay_t` reads `agent_openshell_home_t`, and only
  searches `config_home_t` (review 0.8, S5).
  - An unlabelled registration fails closed: the relay cannot read it.
  - The package and `openshell-setup` restore the label, and
    `restorecon -R` follows no symbolic link.
- **The helper's wait:** a socket under root's `/run`, waited for a
  bounded time: a readiness check, not a trust decision.
- **The name check:** only for `kind: openshell`, before anything is
  made.
- **The templates:** only `backends` and `needs_openshell` changed.
  Modes, permissions, envelopes and tools stay. The gateway's policy is
  the agent's `sub`'s, so an OpenShell agent from a template is held to
  what the systemd one is.
