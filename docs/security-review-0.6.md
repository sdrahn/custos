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
