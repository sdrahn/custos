# Security review before 0.6.0

A focused review of what 0.6.0 adds since `v0.5.0`. It covers `main` at
`d3c6f7c`, and the then unmerged report nudge (#165, at `5083047`):

- the seal of the agents' data and the token issuers (#158/#159);
- playbook step arguments and `{unit}` (#156);
- the report nudge (#165);
- `model.max_output_tokens` and `model.reasoning_effort`, with the
  model's thinking read apart from its answer (#157, #161);
- the `reboot_status` command (#162);
- the Cockpit promote form (#164).

The threat model and the yardstick are those of
[the 0.5 review](security-review-0.5.md): a compromised or
prompt-injected agent or model, a malicious tool output, a local user
without privileges, and root on one host. The security properties are
enforced by mcp-gateway, systemd and SELinux, never only inside the
agent process. Each finding was traced through the code; N1 and N2
were also confirmed with throwaway tests on a copy of `main`.

**Where it stands.** No finding is high. Every finding is fixed, as the
maintainer decided (2026-11-09: fix N1–N3 now, fix N4 too, fix N5 and
N6):

| Finding | Severity | Status |
|---|---|---|
| N1 | medium (needs root) | fixed in #170 |
| N2 | medium (needs root) | fixed in #170 |
| N3 | low | fixed in #170 |
| N4 | info (needs root) | fixed in #170: reported by `show`; the gateway cannot tell |
| N5 | info | fixed in #169 |
| N6 | info | fixed in #165 (the nudge) and #170 (the issuer hints) |

## N1 — medium (needs root) — agent-admin signed hand-edited agent data as it was

*Where:*
- `internal/admin/admin.go`: `readGateway`, `writeGateway`;
- `internal/admin/sealagents.go`: `gatewayYAML`, `sealAgentsWith`, `Issuers`;
- `internal/admin/evaluate.go`: the rollback health check.

Every command that writes an agent's data read the file without
checking its signature, changed a field, and signed the whole entry
with a new `gateway.data` record. Those commands are `mode`, `pause`,
`resume`, `owners`, `permissions`, `promote`, `demote` (also run by
`custos-demote`) and the templates. `seal --agents` did the same for
every agent, and `issuers` for `_http.yaml`.

*Scenario:*
1. Root, or a configuration-management run, adds
   `{server: '*', tool: '*', autonomous: true}` to an agent's file.
2. The gateway rightly denies the agent everything.
3. Any routine write then made the edit trusted, and `show` went
   clean. That could be another administrator's pause, the evaluator's
   demotion, or the `seal --agents` the denial reason itself suggested.

The evaluator also ran the file's `rollback.health_check` as root,
without checking it.

*Fix (#170):*
- On a host whose seal covers agent data, `agent-admin` builds only on
  data that verifies.
- Other changes are refused. A change that only takes from the agent
  (a pause, a demotion, a lower mode) is written unsigned, which the
  gateway denies everything. For issuers, adding is refused and removing
  is written unsigned.
- `seal --agents` signs a file that does not verify only when
  `--accept NAME` names it, once the host has sealed agent data.
- The evaluator runs no health check from data that does not verify.
- The denial reasons no longer suggest signing as the fix.

## N2 — medium (needs root) — a missing key made a new one and signed everything on disk

*Where:* `internal/admin/enablement.go` (`writeEnablement` → `sealKey`),
`internal/admin/seal.go` (`writeSeal`).

When the key credential was missing, `sealKey` made a new key. `writeSeal`
then saw a new public key and signed every agent's data and
`_http.yaml` as they were. Root could delete the credential and edit an
agent's data; the next routine `enable`, `disable` or `custos-setup` made
the edits count, and `show` went clean. A lost key (a reset TPM) had the
same effect without anyone intending it. This contradicted CONTRACT.md
§6b ("a missing key credential is an error, never a new key").

*Fix (#170):*
- A host with a seal signs its state only with the existing key; a
  missing or mismatched key is an error.
- `seal --rekey` makes a new key. It signs again what verifies against
  the old public key (all it needs), and what `--accept` names.
- A host without a seal still gets a new one, signing what is on disk;
  a seal that was removed is reported by `show`, as the 0.5 review
  accepted (M5).

## N3 — low — a pause or a demotion failed when the key could not be used

*Where:* `Pause`, `Demote`, `gatewayYAML`.

When `systemd-creds decrypt` failed (a cleared TPM, a moved disk), an
administrator's pause held only on the agent's host, and the evaluator
could not demote: the gateway's data was not written.

*Fix (#170):* a change that only takes from the agent is written
unsigned when the key cannot be used, with the error named. The gateway
denies an agent with unsigned data everything, which is stricter.

## N4 — info (needs root) — an earlier signed copy can be put back

This is M5's "no freshness", widened by #158. A file signed before a
demotion or a pause verifies, and the gateway counts it. Before, only
the journal's digest made `show` report it. The payload named the agent
but not the host, so a cloned image that shares the key accepts
another host's files.

*Fix (#170):*
- Each signature names a `record`, the time (`at`) and the host's
  machine-id (`host`). The record is journaled (`AGENT_DATA_RECORD`).
- `show` reports a signed file whose record is not the latest, and one
  signed on another host.
- The gateway cannot tell either from the latest: that needs state
  outside the host (the fleet, #113). CONTRACT.md §6b says so.

## N5 — info — `{unit}` went into a regular expression unescaped

*Where:* `packaging/playbooks/service-health.yaml` (`list_log`'s
`pattern`), `internal/playbook/playbook.go`.

`ValidUnit` admits only `[A-Za-z0-9:_.@-]`, so the only metacharacter
that can get in is `.`. The effect is over-matching (`nginx.service`
also matches `nginxXservice`); there is no injection and no ReDoS.

*Fix (#169):* `{unit_re}` is the unit escaped with `regexp.QuoteMeta`,
and `service-health`'s pattern uses it.

## N6 — info — loose ends

- The report nudge left its entry in the governor's maps when an
  activity ended before the nudge's call ran. No security impact.
  Fixed in #165.
- `idp-client` and `check-token` still told the person to add an issuer
  to `_http.yaml` by hand, which on a sealed host makes the gateway
  trust no issuer. They also reported "listed" without checking the
  file's signature. Fixed in #170: they name `agent-admin issuers`, and
  say when `_http.yaml` does not match its seal.

## Checked and sound

- **Policy coverage** (`gateway/policy/agents.rego`, `owners.rego`):
  - `data_signed` checks ES256 with the seal's key, that the payload
    names the agent, and the data's deep equality with what the module
    reads.
  - Extra keys, an edited value, another agent's signature, and an
    enablement or `_http` signature all fail closed. So does a
    difference between agent-admin's YAML and the gateway's loader.
  - Owners, `owned_agent` and the HTTP issuers all count only data the
    seal covers.
  - The emergency stop does not depend on the seal.
- **Signing order and the journal:** files are signed before `_seal.yaml`
  says `agents: true`. `gateway.data` records count only as root's
  (`_UID=0`, `readiness.ByRoot`), and the OpenShell relay does not pass
  that event on.
- **Playbook units:**
  - `{unit}` is a unit name (`ValidUnit`), comes only with a playbook,
    and from an event only for a unit the event names.
  - The substitution touches values only.
  - The host's step calls go through the gateway, each with its own copy
    of the arguments.
- **The report nudge:**
  - It happens at most once per activity, only after an answer with no
    tool call and after the steps, never when the activity was stopped.
  - It runs no tool and records no report.
  - The model cannot spoof its call id.
- **Model settings:**
  - `max_output_tokens` is bounded.
  - `reasoning_effort` is one of four values, and is accepted only with
    provider `openai`.
  - The thinking stays out of the summary, the response, the rationale
    and AG-UI, is never sent back to the model, and only its size is
    journaled.
- **`reboot_status`:** a fixed argv with no arguments, read only.
- **Cockpit:**
  - The promote form builds everything with `el` and `textContent`.
  - The root bridge gets argument arrays.
  - The envelope goes to `promote --envelope -` as JSON, which
    `agent-admin` checks again.

## Addendum: pending restarts (#171) and tool output as data (#172)

Two changes came after the review: `update-status` with mcp-gateway
1.16's server `restarts` (#171, merged), and the host's notice that
tool outputs are data (#172). They were reviewed the same way, by a
reviewer who had not written them. Each finding was traced through the
code. P4, P5 and P6 were also confirmed with throwaway tests, and P7
was timed on a copy.

**Where it stands.** No finding is high or medium. Every finding is
fixed, as the maintainer decided (2026-11-09: `--restarts` as an
explicit option; fix all):

| Finding | Severity | Status |
|---|---|---|
| P1 | low | fixed in #174 |
| P2 | info | documented in #174 |
| P3 | info | fixed in #174 |
| P4 | low (P4a predates 0.6) | fixed in #172 |
| P5 | low | fixed in #172 |
| P6 | info | fixed in #172 |
| P7 | info | fixed in #172 |
| P8 | info | fixed in #172 |
| P9 | medium (a local user) | fixed in #176 |
| P10 | medium (a local user) | fixed in 0.6.2 |

### P1 — low — custos-setup offered a server that holds a capability, and offered it again after it was removed

*Where:* `internal/admin/setup.go`, `setupRestartsServer`.

mcp-gateway ships `restarts`, which holds `CAP_SYS_PTRACE`, as a
profile on purpose: offering it is meant to be the administrator's own
step. `custos-setup` linked it whenever the package was installed, and
`mcp-gateway-tests` requires the package. The scenario:

1. The administrator removes the link, having decided against the
   capability.
2. A later `custos-setup` run (a new key, the troubleshooting advice)
   makes the link again.
3. `health-check` restarts with the server offered, and so does every
   other principal's gateway.

Only root can influence the link, so this is about consent, not
escalation.

*Fix (#174):* `custos-setup` links the profile only with `--restarts`.
Without it, the step says how to offer the server. A link that is there
is kept, and one the administrator removed is not made again.

### P2 — info — `pending_restarts` shows the model other users' processes

The agent's own unit hides other processes (`ProtectProc=invisible`).
For every process that maps a replaced file, the tool returns:
- its id and name (`comm`, 15 bytes, never the command line);
- its user and unit;
- the files and the packages.

After a glibc update that is nearly every process, and with a remote
backend it goes to the model's provider. A local user can set its own
process name, which gives that user 15 bytes in the model's context:
the ordinary case of a malicious tool output, which the approvals bound.
Nothing of the output reaches the journal.

*Status (#174):* documented in chapter 9, next to how to offer the
server, so that the administrator weighs it against the backend.

### P3 — info — "no reboot is pending" when the flag could not be seen

`test -e /run/reboot-needed` is false when the file is missing and also
when the command may not see it. That happens with an older
`mcp-gateway-selinux`, which nothing pinned, or with a flag labelled
otherwise. The answer then sounded certain and was wrong.

*Fix (#174):* `reboot_status` reads the flag with `stat`. It says that
no reboot is pending only for "No such file". Otherwise it says that
whether an update asked for one cannot be seen, and why.

### P4 — low — an output returned as an error got no notice; its text went into the journal

ADK turns an MCP result with `isError` into an error carrying the whole
content. The exec server returns one for every non-zero exit, with
stdout and stderr. Such an output:
- reached the model without the host's notice;
- was not checked for signs before the next call's approval;
- went verbatim into the journal as `AGENT_REASON` on `tool.result`
  (P4a). That predates 0.6, and contradicts "outputs stay out of the
  journal".

*Fix (#172):*
- Error outputs are checked like any other: they get the notice, and
  the approval item's signs.
- The journal's reason for a tool error is fixed text with the error's
  size, never its content.

### P5 — low — the notice could be imitated

An MCP tool cannot set the top-level `custos_notice`, since ADK wraps
its result under `output`. It can nest one, though. A nested
`custos_notice` that vouches for an output has no sign of an injection
and reached the model unchanged. A client tool's result (AG-UI) never
passed the host's check at all.

*Fix (#172):*
- `DataNotice` says that the host never calls an output safe or
  approved, and that a `custos_notice` inside what a tool returned is
  the tool's text.
- A nested `custos_notice` is a sign of its own.
- A client tool's result gets the same check, and a top-level
  `custos_notice` in it is replaced.

### P6 — info — easy ways past the signs

The check missed:
- a real newline inside a phrase (`IGNORE\nALL PREVIOUS INSTRUCTIONS`);
- text in a map's keys;
- zero-width characters.

*Fix (#172):* the check reads keys as well as values, matches on the
text with its whitespace collapsed as well as as it is, and drops
invisible format characters first. It stays advisory: the gateway is
the boundary.

### P7 — info — the check's cost grows with the output, without a cap

RE2 runs in linear time, but a 14 MB output took 5.5 s of the agent's
CPU per result.

*Fix (#172):* the check reads at most the first 256 KiB of an output.

### P8 — info — AG-UI clients see `custos_notice`, which the contract did not say

`TOOL_CALL_RESULT` carries the response the model was given, which is
the copy with the notice.

*Fix (#172):* CONTRACT.md's AG-UI section says so.

### P9 — medium (a local user) — the relay's owner check could read a closed connection's entry

Found by OBS, not by the review: 0.5.0's `%check` failed on aarch64 for
SLE 16.0 and 16.1 (`TestRelayToAgent`, `TestRelayOnlyToTheForward`).

*Where:* `internal/relay/peer.go`, `peerUID`.

The relay sends the agent's secret, and every prompt, only to a socket
root owns: OpenShell's port forward (the 0.5 review's H3). It reads the
owner from `/proc/net/tcp`, from the first entry with the connection's
addresses. That entry can be an earlier connection's: after the server
side closed it, it stays as TIME_WAIT, which the kernel lists with uid
0, and on loopback a new connection may reuse its ports
(`tcp_tw_reuse`). The scenario:

1. A local user binds the forward's port while no forward runs.
2. The user's listener closes the relay's first connection itself.
3. The relay's next connection reuses those ports and reads the
   TIME_WAIT entry: uid 0, root's.
4. The relay sends the secret to that user.

The same entry made the tests fail where the listener is not root's,
which is what OBS showed.

*Fix (#176):* only an entry in a connected state counts (ESTABLISHED,
or SYN_RECV until the handshake ends), never TIME_WAIT or CLOSE.
`TestPeerUIDSkipsTimeWait` puts a TIME_WAIT entry with uid 0 before the
connected one; it fails without the fix.

### P10 — medium (a local user) — the relay's owner check read a connection not yet accepted

Found by OBS again: 0.6.1's `%check` failed on aarch64 for SLE 16
(`TestRelayToAgent`: "held by uid 0, not by OpenShell's forward (uid
399)"). P9's fix was right but not enough.

*Where:* `internal/relay/peer.go`, `peerUID`.

The kernel lists a socket's owner from its file (`sock_i_uid`; newer
kernels take the owner kept in the socket). The other end's
entry of a connection the listener has not accepted yet has no file, so
those kernels list it with uid 0, in a connected state. SLE 16 runs such
a kernel; Tumbleweed's newer one does not, which is why only SLE 16
failed, and only where the test's listener accepted later than the check
ran. The scenario:

1. A local user binds the forward's port while no forward runs.
2. The user's listener does not call `accept()` until the relay has
   checked the connection.
3. The relay reads the connection's entry: uid 0, root's.
4. The relay sends the secret to that user.

*Fix (0.6.2):* the owner is the listening socket's. That entry is in
state LISTEN, on the connection's address and port or on the wildcard
address and that port, and it always has its file. The kernel refuses a
second listener on the same port unless both use SO_REUSEPORT, which it
allows only to the same user. The connection's own entry must still be
there, in a connected state. `TestPeerUIDIsTheListeners` gives the
connection's entry uid 0 and the listener another; `TestPeerUIDBeforeAccept`
checks a listener that never accepts against the kernel's real tables.
Both fail without the fix on such kernels.

### Checked and sound

- **The link (#171):**
  - Only root can influence it: both directories are root's, and
    `custos-setup` runs as root.
  - `os.Symlink` never overwrites. A file or another link there is
    kept, and the target is the absolute profile path.
- **The permission (#171):** `{server: restarts, tool: pending_restarts,
  read_only: true, category: diagnostics}` names server and tool
  exactly, as the gateway requires for a server with a capability. It
  stays valid on a diagnostics-only host. The tool takes no arguments
  and changes nothing.
- **Where the privilege lives (#171):** the capability is held by a
  dynamic user in `mcpsrv_restarts_t`, which reads process state, not
  memory, and never by the agent.
- **`shippedBefore` (#171):** the digests are 0.3.0's and 0.4.0's to
  0.5.0's files. A file replaced is byte-identical to one Custos
  shipped, and the replacement is atomic.
- **The output itself (#172):** the tool's result is never changed. The
  model gets a copy, while the report's quotes and the approval items
  use the output as the tool gave it.
- **The journal (#172):** `AGENT_INJECTION_SIGNS` holds sign names only.
- **The instruction (#172):** `DataNotice` has no braces, so ADK's
  placeholders cannot reach into it. Every agent is built in one place,
  so every agent gets it.
- **Regular expressions (#172):** RE2, without backtracking.
