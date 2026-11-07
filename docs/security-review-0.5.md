# Security review before 0.5.0

The full security review of #125: the whole system, at `main` after
#133 (commit `1ce30e2`), with extra attention to what 0.5.0 adds (event
triggers #118, the full response in delegation #119, the way back from
enabling categories #110, the full response and its Markdown rendering
in 0.4.2, host-run playbook steps in 0.4.1, the site's IdP #111, the
gateway's host name #131, the templates' documentation access #130).

Its yardstick is CLAUDE.md's rule: the security properties (supervised
by default, approvals, tool boundaries, the emergency stop) are
enforced by mcp-gateway, systemd and SELinux, never only by code inside
the agent process. Each finding below was confirmed in the code (file
and line), most of them with a test or a probe; none is a guess.

Status of each finding: **open** (to be fixed before `release-0.5`),
**fixed in #N**, or **accepted** (in writing, by the maintainer, with
the reason). Findings of severity *low* and *info* may stay open.

## How it was reviewed

| Area (#125) | How |
|---|---|
| 1. Privilege boundaries, control plane | code review of `agent-admin` (every command Cockpit's root bridge runs), the control API's guard, the relay, the delegation trace, pause, the emergency stop, the evaluator; throwaway tests on a copy |
| 2. The gateway's policy | `make policy-test` against mcp-gateway v1.15.0 (192/192), and 18 probe tests for bypasses (210/210, each asserting what the policy does today) |
| 3. systemd, SELinux | `systemd-analyze security --offline=yes` on each unit; each rule of `selinux/custos.te` traced to what the code does; the CI's denial checks read |
| 4. Prompt injection, model output | the injection signs probed with evasions and normal admin output; a fuzzer of the Markdown renderer in headless Chrome with the real `common.js` and `markdown.js` (20,107 inputs, every output DOM checked against an allowlist of elements and attributes); every sink of the Cockpit page read |
| 5. Data at rest, records | every file the packages and `agent-admin` create (owner, mode, contents, who can read it); every credential flow; the journal fields |
| 6. OpenShell, HTTPS | the relay, the forward, the sandbox's creation, egress, the issuer (#111), TLS verification |
| 7. Supply chain, build | `go mod verify` (all verified); `go vet` (clean); `staticcheck` 2025.1.1 (3 findings, below); the release workflow; the spec's scriptlets, `%files` and rpmlint filters. `govulncheck` needs vuln.go.dev, which the review's environment cannot reach: it runs in CI instead (S2) |
| 8. Network | egress of both backends; chapter 14; the air-gapped gate runs on this review's pull request (its branch name contains `airgap`) |

## Threat model

**Assets.** The host (root); what the agents may do at the gateway
(their permissions, modes, categories, envelopes); the approvals and
the people who give them; the emergency stop and pause; the records
promotion and demotion are decided on (readiness, reviews, feedback,
the gateway's decisions); the agents' credentials (model keys, gateway
tokens, OAuth client secrets, the relay secret); what agents have read
and answered (activities, responses, sessions).

**Actors.**

| Actor | Has | Trusted for |
|---|---|---|
| root, an administrator | everything | everything |
| a supervisor (member of `agent-clients`, Cockpit user) | the agents' sockets: start, read, cancel own activities | its own activities; reading all activities (by design, §4) |
| an approver (gateway rules, owners) | the gateway's control socket or approval page | deciding calls |
| an agent process, possibly compromised (prompt injection, a malicious model) | its own user `agent-<name>` in `agent_t`, or an OpenShell sandbox; the gateway's socket or HTTPS listener | nothing: the rules must hold against it (§1) |
| a local user without a role | a shell on the host | nothing |
| the OpenShell user (rootless Podman) | every OpenShell sandbox's container | root-equivalent for the OpenShell agents (C9, to be stated in §16a) |
| a tool's output, a log line, a document | text the model reads | nothing: data, never instructions |
| the network | the model endpoint, the IdP, the HTTPS listener | nothing without TLS and a valid token |

**Trust boundaries.** The agent process and everything it reads ↔ the
gateway's policy (each call); the agent ↔ systemd and SELinux (files,
processes, sockets, network); a supervisor ↔ root (Cockpit's root
bridge, `agent-admin`); the sandbox ↔ the host (the relay, its secret,
the log bridge); records written by an agent ↔ records root writes
(journald's trusted fields).

## Findings

Severity: **high** breaks a stated boundary for an actor in the threat
model; **medium** weakens one, or needs a mistake or a second step;
**low** limited impact, or defence in depth; **info** a note.

### H1 — high — An OpenShell agent forges root's records through the relay

*Found by three reviewers independently (A1, C1, E3).*

`agent-openshell-relay` runs as root and turns the agent container's
output into journal records (`internal/relay/relay.go:51-89`). It
accepts **every** event name `journal.MessageIDs` knows, including
those only root writes (`category.review`, `diagnosis.feedback`,
`permissions.changed`, `agents.stopped`, `category.demoted`,
`categories.*`), and copies every key as `AGENT_<KEY>` without checking
it. `native.Log` (`internal/journal/journal.go:125-149`) writes a key
as given, so a key with a newline adds fields of the agent's choosing
(`SYSLOG_IDENTIFIER=custos-promotion`, `OBJECT_SYSTEMD_UNIT=…`,
`AGENT_NAME=<another agent>`). Journald stamps them `_UID=0`, which is
all `readiness.ByRoot` (`internal/readiness/readiness.go:658`) checks.

*Path.* Code running as the agent in an OpenShell sandbox (§1: the
rules must hold against it) prints one JSON line, e.g.
`{"contract":"1","name":"sb","event":"category.review","category":"patch-errata","invocation":"e-1","call_id":"c1","verdict":"ok","by":"alice"}`.
The OpenShell user can do the same by starting a container whose name
`agent-openshell-logs` matches.

*Impact.* Forged "ok" reviews take its autonomous actions out of the
human review sample (§6a) and outweigh an "incident"; forged feedback
and permission history raise its readiness; with
`OBJECT_SYSTEMD_UNIT=mcp-gateway.service` and an `mcp-approval` message,
`journalctl -u mcp-gateway.service` returns the record as the gateway's
own, forging approval decisions; forged `agents.stopped` records turn
other agents' approved calls into incidents, and `custos-demote`
demotes their bounded categories. Promotion still needs `agent-admin
promote`; the gateway still decides every call. The systemd backend is
not affected: there the agent writes with its own UID.

*Fix.* (1) The bridge passes only the events the agent host itself
writes (`agent.*`, `invocation.*`, `model.call`, `tool.*`,
`approval.*`, `limit.exceeded`, `activity.cancel`, `diagnosis.report`)
and only keys matching `^[a-z][a-z0-9_]{0,63}$`; (2) `native.Log`
refuses a field name outside `[A-Z0-9_]`; (3) root's records count only
with `_COMM=agent-admin` (or `custos-demote`'s unit), not `_UID=0`
alone; (4) the gateway's records are read by
`_SYSTEMD_UNIT=mcp-gateway.service`, not `-u`. Status: **open**.

### H2 — high — The relay secret and the configuration on `openshell`'s command line

*A2, E2.* `agent-admin` creates an OpenShell sandbox with
`--env CUSTOS_RELAY_SECRET=<secret>` and
`--env CUSTOS_CONFIG=<base64 of agent.yaml>`
(`internal/openshell/openshell.go:194-198`, run by
`internal/admin/admin.go:430`).

*Path.* Any local user reads `/proc/*/cmdline` while `agent-admin
set-credential` or `edit` runs (SLES mounts `/proc` without `hidepid`),
then connects to `127.0.0.1:<forward port>` (derived from the name,
`openshell.Port`) with the secret and `X-Custos-Caller: 0:root`.

*Impact.* Root on that agent's control API: activities with any `user`
and `origin` (a forged schedule or event runs within bounded
categories without approval), every activity, response and session,
cancel, pause and resume at the host. The agent's configuration
(`root:agent-<name> 0640`) leaks as well. §16a: "host users who cannot
reach the agent's socket must not [know the secret]".

*Fix.* OpenShell 0.1.7's `sandbox create --env-from KEY[=ENVVAR]` takes
a value from the CLI's environment ("the value does not appear in the
CLI process arguments"): pass both that way, as the OAuth client secret
already is; a test that no argument holds the secret. Status: **open**.

### H3 — high — The relay hands its secret to whatever listens on the forward port

*E1.* `agent-openshell@.service` starts the forward with
`ExecStartPre=-…forward start` (a failure is ignored) and the relay
sends `X-Custos-Relay-Secret` with every request to
`127.0.0.1:${FORWARD_PORT}`. The ports (47000–47999) are in the
kernel's ephemeral range and reserved for nothing.

*Path.* A local user binds `127.0.0.1:Port(name)` while no forward
exists (before the first connection after boot, or after any stop or
restart: `ExecStopPost` removes the forward). The forward fails, the
relay connects to the user's listener and sends the secret, the
caller and the prompt.

*Impact.* The user poses as the agent towards Cockpit and root, sees
every prompt, and with the secret gets H2's access once the real
forward is back. E5 (two agents with the same port) leads to the same
silent misrouting without an attacker.

*Fix.* The forward's failure is fatal; the relay checks before it
sends anything that the listener on the port belongs to the forward
(the socket's owner, from `/proc/net/tcp` and the process), and the
secret is no longer a bearer value: each request carries an HMAC over
its method, path, caller and a timestamp, which a listener cannot
replay elsewhere. `agent-admin create` refuses a port another agent
has (E5). Status: **open**.

### M1 — medium — What an approver reads can differ from what runs

*D1.* The approval item shows the arguments as `JSON.stringify` output
(`cockpit/custos/approvals.js:64`), which leaves bidi controls
(U+202A–202E, U+2066–2069), zero-width characters and line separators
as they are; a report's plan command is a `<code>` with a Copy button
(`report.js:84`) and its CSS (`custos.css:208`) does not keep line
breaks. In headless Chrome, `"echo cleanup‮⁦; rm -rf /srv⁩⁦ #⁩"` shows
as `"echo cleanup" #; rm -rf /srv` in the approval and as a commented
out `rm` in the plan; Copy gives the real string. A two-line command
shows as one line.

*Who.* The model, after an injection. *Impact.* An approver approves a
call other than the one they read; an administrator runs a plan step as
root by hand. *Fix.* Show format and control characters as visible
`\uXXXX` with a "hidden characters" mark in approval arguments, plan
commands and quoted evidence; `white-space: pre-wrap` for commands and a
"several lines" mark. mcp-gateway's own approval page and CLI have the
same issue (reported there). Status: **open**.

### M2 — medium — An agent counts as an approver

*B1, A6.* CONTRACT.md §7 recommends
`approvers: {zypp: ['role:admin', 'group:agent-clients']}`, and every
agent's user is in `agent-clients` (§2). `agent-admin owners` refuses
only `user:agent-*` and `group:agent-*`
(`internal/admin/admin.go:897-903`), not `group:agent-clients`,
`group:mcp-users` or `remote-user:agent-*`; `owners.rego` never checks
that the approver is not an agent. With such a rule, the policy allows
an agent to decide calls, its own included; as the grant is not by
`self`, the gateway's password step does not apply.

*What stops it today.* Only that an agent cannot reach the gateway's
control socket (`InaccessiblePaths=`, SELinux): the policy adds
nothing. *Fix.* Correct the example; `owners` refuses those rules;
`owners.rego` grants nothing to an approver named `agent-*`; ask
mcp-gateway for a way to deny an approver. Status: **open**.

### M3 — medium — The emergency stop stops nothing when OPA is slow

*A3.* `StopAll` (`internal/admin/admin.go:1028-1045`) returns as soon
as `writeStop` fails, which it does when the gateway's OPA has not
loaded the flag within 20 s: no `systemctl stop`, no sandbox stop, no
`agents.stopped` record. Exactly when the gateway may serve stale
policy, the processes keep running. *Fix.* Stop the units and sandboxes
first, then write and wait for the policy, and report its error; the
same for pause's host part. Status: **open**.

### M4 — medium — A systemd agent's network is not fenced to its model

*C2.* `agent@.service` allows `AF_INET`/`AF_INET6` with no
`IPAddressDeny=`; SELinux limits `agent_t` to HTTP ports
(`custos.te:85-86`), not hosts, and UDP not at all. A compromised agent
reaches any host on port 80, 443, 8008, 8080, … (the cloud metadata
service at 169.254.169.254 included) and can send what it read
elsewhere. §3a: the backend must allow "the gateway and the model
endpoint, and nothing else". Chapter 14 documents a drop-in, by hand.
*Fix.* `IPAddressDeny=link-local multicast` in the template, and
`agent-admin` writes `IPAddressDeny=any` with `IPAddressAllow=` the
model endpoint's addresses (and localhost) for each agent, refreshed by
`edit`. Status: **open**.

### M5 — medium (needs root) — The seal covers less than §6b says

*B2.* Removing `_seal.yaml` lets an edited or removed
`_enablement.yaml` count as it is; the agents' data is not sealed, so
`{server: "*", tool: "*", category: diagnostics, read_only: true}` in
`agents/<name>.yaml` allows any call on a diagnostics-only host; an
older signed state can be copied back (no freshness). Each needs root:
the seal makes a change past `agent-admin` deliberate, it cannot stop
root. *Fix.* Say so in §6b; `agent-admin show` and Cockpit report a
missing seal on a host whose journal has one, and a state older than
the latest record; refuse permissions with a wildcard server or tool in
`diagnostics`. Sealing the agents' data is for a later release. Status:
**open** (the documentation and the checks); the rest proposed to be
**accepted**.

### M6 — medium — The override rate depends on the agent's own records

*A4.* Readiness and `custos-demote` count proposals from the agent's
`approval.pending` records, joined with the gateway's `mcp-approval`,
which names no tool. A compromised agent that omits the record for a
declined call keeps its override rate at 0, so the evaluator never
demotes it for overrides (rate caps, tool errors and incident reviews
still work). §6a documents the join. *Fix.* mcp-gateway names the
server and tool in `mcp-approval`, and the count comes from the
gateway's records alone: a gateway change. Proposed: **accepted** for
0.5.0, with a note in §6a and an issue for mcp-gateway.

### Low

| ID | Finding | Where | Fix |
|---|---|---|---|
| L1 (D2) | A tool's error text, a failed delegation's answer included, skips the injection signs and the "preceding" view | `internal/governance/governance.go:744` | keep the error text for `suspect` and `preceding` |
| L2 (D3) | The signs run on text with its line breaks removed: line-based signs miss, `directedValue` fires on ordinary logs | `internal/governance/report.go:238` | signs on the raw text, normalise only for quote checks |
| L3 (D4) | After 1 MiB of output in an activity, later outputs are kept empty (no signs, empty excerpt) | `report.go:239-240` | keep the last part of each output for the signs |
| L4 (D6) | Deep nesting overflows the Markdown renderer's stack: the response dialog does not open | `cockpit/custos/markdown.js:63-66,121-122` | cap the depth, fall back to text |
| L5 (E4) | The log source can follow another agent's container (`diag` and `diag-2`) | `packaging/openshell/agent-openshell-logs` | anchor the pattern on the full UUID |
| L6 (E5) | Two agents can get the same forward port | `internal/openshell/openshell.go:37-44` | with H3 |
| L7 (E7) | A Google API key as `model.credential` is not recognised and is echoed in the error | `internal/config/config.go:53-76,373` | `aiza` prefix; never echo a refused credential |
| L8 (A5) | Activity metadata keyed by session id alone, across users | `internal/governance/governance.go:300` | key by user and session |
| L9 (B3, B4) | Hand-edited agent data: a category that is not a string bypasses enablement; a bounded category without windows is always autonomous | `gateway/policy/agents.rego:176,227-229` | fail closed in both |
| L10 (C3, C4) | The root helper units (`custos-demote`, `agent-schedule@`, `agent-event@`, `agent-openshell@`) have little systemd hardening | `systemd/` | the directives `systemd-analyze` names |
| L11 (C5) | `agent_relay_t` has rules it does not need, and can run container runtimes | `selinux/custos.te:164-232` | remove the unneeded rules; the log source as the OpenShell user |
| L12 (C6) | CI's denial check misses later sections, and the spike checks only source contexts | `test/vm/run-tests.sh:864-875`, `test/openshell/run-spike.sh:507` | check again at the end; source or target |
| L13 (S1) | In `--listen-tcp` mode a failed listen is ignored (`err` shadowed) and the launcher panics instead of saying why | `cmd/agent-launcher/main.go:88-103` | do not shadow `err` |
| L14 (E6) | Every supervisor reads every chat answer and tool excerpt | `internal/launcher/launcher.go:405-416` | by design (§4); proposed: chat responses only for their user and root |

### Info

- **I1 (D5).** The injection signs miss easy evasions (zero-width
  characters, homoglyphs, paraphrase) and fire on some admin output.
  They are a signal for the approver, never the boundary (§4).
- **I2 (D7).** A delegation the caller may make without approval lets
  it drive the callee's autonomous permissions; by design (§9). The
  `agents` server's definition should say: never mark `delegate`
  read-only towards an agent with autonomous permissions.
- **I3 (C7, C8).** A local user who can crash a watched service starts
  an event activity at most every `min_interval_seconds` (as low as 1);
  a very long unit name makes `OnFailure=` invalid. A floor of 60 s and
  a length check.
- **I4 (C9).** The OpenShell user can read every OpenShell agent's
  relay secret (`podman inspect`) and can enter every sandbox: it is
  root-equivalent for them. To be stated in §16a.
- **I5 (B5, B6).** A window in the hour DST repeats is open twice;
  `session` approval scope for agents; argument patterns without
  anchors; the `agent-` prefix is not reserved against directory users.
- **I6 (A7, A8).** A delegation's root is self-asserted (labelled so);
  `instruction_file` of an OpenShell agent is read as root.
- **I7 (E8).** `model.endpoint` may be `http://` to any host (the model
  key in clear text); a tool's error text unbounded in `AGENT_REASON`;
  `systemd-creds` creates its file 0644 before the chmod (encrypted).
- **I8 (S2).** `govulncheck` in CI; actions pinned to commits, not tags.
- **I9.** `staticcheck`: a test assertion that cannot fail
  (`internal/openshell/openshell_test.go:107`); a deprecated call in a
  test.

## Checked and sound

- **Names and paths.** Every name `agent-admin` takes (agent, schedule,
  event, unit, credential, template, playbook, category) is a strict
  pattern; every `filepath.Join` and systemd instance name traced uses
  checked parts; prefixes (`foo`, `foo-bar`) and `_` splitting are
  safe; commands get argument lists, never a shell.
- **Root's writes.** `writeFile` creates a temporary file 0600, then
  renames; root never writes into a directory another user can write.
- **YAML from Cockpit.** Known fields only; the configuration cannot set
  modes, permissions or categories; templates only advisory or
  supervised.
- **The control API.** Agents reach only `POST /v1/activities` and
  `GET /v1/activities/{id}`; `origin` and `user` are root's; sessions
  per user; pause and resume root's; path tricks rejected.
- **The relay's caller.** Client `X-Custos-*` headers dropped; the agent
  checks a secret of at least 32 characters in constant time; loopback
  only.
- **The policy.** Modes, read-only, categories, rates (malformed: fail
  closed), windows (malformed: fail closed), pause and the stop over
  grants, roles bound to agents deny, unknown agents deny, issuers for
  agents exact, the seal's cryptography (`HS256`, `none`, another key,
  edited fields: all fail closed), owners never `self`, arguments
  anchored by `$` at the end of text.
- **`agent@.service`.** Exposure 1.3: no capabilities, `ProtectSystem=strict`,
  `ProtectProc=invisible`, a system-call filter, the sockets and the
  gateway's control socket hidden; `ImportCredential=agent-%i.*` cannot
  match another agent's.
- **Events.** No loop on Custos's own units; instances and drop-ins
  from checked names; `agent-event@` re-checks against `agent.yaml`;
  the damping state is root's.
- **SELinux.** `agent_t` executes no `bin_t`, reaches no agent socket
  and no control socket, writes only its state; no type a less trusted
  domain writes is executed or trusted by a more trusted one.
- **The Cockpit page.** No HTML from data anywhere (no `innerHTML`,
  `insertAdjacentHTML`, `document.write`, `eval`); no `href` or `src`
  from data; Markdown links are text; Cockpit's default CSP; 20,107
  fuzzed inputs with no element or attribute outside the allowlist.
- **Approvals.** Arguments from the gateway, in full; the rationale
  labelled self-asserted, after the gateway's part; a tool not offered
  is refused before the gateway; client tools never reach it; the
  delegation trace's parent checked against `SO_PEERCRED`.
- **Credentials.** Gateway tokens and client secrets reach `openshell`
  through the environment; the model key never in a log, an error or
  the status; no `InsecureSkipVerify`; https for the gateway and token
  URLs (http only on loopback); the journal has only a digest of tool
  arguments.
- **Supply chain.** All modules verified; `go vet` clean; the release
  checks the tag against the spec, CHANGELOG and `_service`, runs the
  checks, and publishes the sources with `SHA256SUMS`; the spec's
  scriptlets restart nothing that could cut off an approval; rpmlint's
  filters hide no permission or capability warning.
