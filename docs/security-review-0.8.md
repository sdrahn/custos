# Security review before 0.8.0

A focused review of what 0.8.0 changes since `v0.7.0`. It covers `main`
at `642eb69`:

- OpenShell agents on OpenShell's systemd compute driver (#216): the
  setting, `agent-openshell-logs` reading the workload unit's journal,
  and `agent_relay_t`'s new rules;
- `agent-admin openshell-setup` (#228), which runs as root and
  configures OpenShell in its gateway user's home;
- templates on the OpenShell backend (#229), with Cockpit's choice;
- the playbooks in the OpenShell agent image (#230);
- the relay's stop of its log command and the journal rules (#231);
- the `dontaudit` for `ss`'s module request (#233).

The threat model and the yardstick are those of
[the 0.5 review](security-review-0.5.md): a compromised or
prompt-injected agent or model, a malicious tool output, a local user
without privileges, and root on one host. New in 0.8 is a local user
who matters more than before: OpenShell's gateway user, an
unprivileged account whose home `openshell-setup` works in as root.
Anything that runs as that user (the OpenShell gateway, or a process
that escaped a rootless Podman container) is that user.

A reviewer who had not written the changes read the difference between
the two versions, not the pull requests' descriptions. The author
checked each finding against the code before fixing it, and showed the
two high ones with tests that fail on `642eb69`.

**Where it stands.** The two high findings and the medium one were in
`openshell-setup`, which no release has had; they are fixed before
0.8.0. What is open is low or info. For S5 the maintainer decided
(2026-11-18): 0.8.0 is released with it open, and it is fixed in 0.9.



| Finding | Severity | Status |
|---|---|---|
| S1 | high | fixed (#235) |
| S2 | high | fixed (#235) |
| S3 | medium | fixed (#235) |
| S4 | low | fixed (#235) |
| S5 | low | open, for 0.9 (maintainer's decision): a type of its own for root's OpenShell registration |
| S6 | info | accepted: the relay reads the whole journal |
| S7 | low | fixed with this review |
| S8 | info | template values quoted with this review; the gateway name's syntax open |

## S1 — high — root's writes in the gateway user's home followed links

*Where:* `internal/admin/oshsetup/oshsetup.go`, `driver()` and `write()`
at `642eb69`.

The setup wrote the systemd driver's drop-in to
`~/.config/systemd/user/openshell-driver-systemd.service.d/custos.conf`
and then ran `chown USER: <that directory>`. Both follow a symbolic
link the user put in the directory's place. With a link to
`/etc/systemd/system`, root wrote `custos.conf` there and gave the
directory to the user, who could then add a unit or a drop-in to a
root service: root at the next reload. The setup says it is safe to run
again, so an administrator runs it more than once, and the user can
prepare for the next run.

*Fix (#235):* every access in the home goes through an `os.Root` opened
on it, so a path cannot leave the home. A file is written as a new one
(`O_EXCL`), given to the user with `fchown` on its descriptor, and then
renamed into place. Missing directories are made inside the root and
given to the user, and there is no `chown` of a path any more. A test
links the directory to a fake `/etc/systemd/system`: on `642eb69` the
setup writes into it; with the fix it refuses.

## S2 — high — root read files through a linked gateway.toml

*Where:* `oshsetup.go`, `gatewayConfig()` at `642eb69`.

`gateway.toml` was read with `os.ReadFile` and written back, changed,
as the user's file. With a link to `/etc/shadow` (or to root's SSH key,
or an agent's `relay.secret`), root read that file, added the TOML
tables (the version check passes on a file with no `[openshell]`
table), and put the result in the home as a regular file of the
user's. That is any file root can read, without a race.

*Fix (#235):* the same `os.Root`. A read takes a regular file only,
opened without blocking and checked on its descriptor, at most 1 MiB.
A link that leaves the home is refused, while one inside it still
works, since those are the user's own files. A test links `gateway.toml`
to a fake `/etc/shadow`: on `642eb69` the setup takes it; with the fix
it refuses, and the link stays.

## S3 — medium — the chown after the rename

*Where:* `oshsetup.go`, `write()` at `642eb69`.

The new file was renamed into place and then given to the user by a
`chown` process. The user owns the directory, so in between they could
replace the file with a link to a file of root's, and the `chown`
gave them that file. inotify tells them when.

*Fix (#235):* as S1: the owner is set on the descriptor before the
rename.

## S4 — low — root's copy of the registration

*Where:* `oshsetup.go`, `registration()` at `642eb69`.

The copy of `~/.config/openshell` to `/root/.config/openshell` followed
links, and a FIFO in the tree held the setup up. What a link led to
went into root's own directory, which the user cannot read, so nothing
was disclosed. The copy kept each file's mode.

*Fix (#235):* the walk goes through the `os.Root` and takes regular
files only. They are written as root's, with mode 0600.

## S5 — low — agent_relay_t may read every user's ~/.config

*Where:* `selinux/custos.te`, the `config_home_t` block (#216).

The relay reads root's OpenShell registration
(`/root/.config/openshell`) to ask the gateway for the sandbox's id.
`config_home_t` labels every user's `~/.config`, so the relay's
domain may read any of them. The relay runs as root without
`dac_override`, and `ProtectHome=read-only` shows the homes, so what
DAC lets it read are the world-readable files there. Any local user can
read those already, but they are outside what the relay needs.

*Open, to be fixed in 0.9:* a type of its own for `/root/.config/openshell(/.*)?` in
`custos.fc`, restored by `openshell-setup` and the guide's steps, and
allowed instead of `config_home_t`.

## S6 — info — the relay reads the whole journal

*Where:* `selinux/custos.te`, `logging_read_generic_logs` and
`syslogd_var_run_t` (#216, #231).

To follow the workload unit's journal, `journalctl` in the relay's
domain reads the journal files. The journal has no labels per unit, so
it may read all of them: the other agents' records, sshd and sudo. The
relay is a root service that parses only what the agent writes. A
journal namespace for the sandboxes' workload units (`LogNamespace=`)
would narrow it, but that is the driver's units, not Custos's.

*Accepted*, and said in the release notes.

## S7 — low — the sandbox id took several lines

*Where:* `packaging/openshell/agent-openshell-logs` (#216).

`awk '$1 == "Id:"'` printed every line that starts with `Id:`, nested
ones included, and the UUID check passed if any one line was a UUID.
A second line then reached the unit name given to `journalctl -u`. It
was quoted, so no shell injection, but the gateway's output decided the
match.

*Fix (with this review):* the first `Id:` only, and exactly one line.

## S8 — info — values in the YAML a template draft writes

*Where:* `internal/admin/templates/backend.go` (#229).

`--openshell-gateway` and mcp-gateway's scopes went into the draft's
YAML unquoted, so a value with `,` or `}` changed its structure. They
come from root's command line and root's mcp-gateway configuration,
and the result goes through `config.Parse`, which already refuses a
scope that is not a scope name.

*Fix (with this review):* the gateway's name, the driver and the scopes
are quoted; a test shows a name with `, openshell_driver: …` staying
a name.

*Open, older than 0.8:* `sandbox.openshell_gateway` has no syntax
check, and goes into the relay unit's `openshell.env`. A newline there
would add a variable to a root service's environment. Writing
`agent.yaml` takes root already, so it is info. A check like the user
name's is the fix.

## Checked and found sound

- **TOML editing (`toml.go`):** values come from `strconv.Quote` or
  constants only. The paths written into it come from `/etc/passwd`.
- **The units' drop-ins:** the helper's names a user that matched
  `/etc/passwd`. Root's files under `/etc` (the delegation drop-in,
  the helper's drop-in, the trust anchor) are root's paths, written by
  root.
- **`/etc/subuid` and `/etc/subgid`** go through `usermod`. Lingering
  goes through `loginctl`.
- **The CA:** `checkCA` requires a CA certificate. With the systemd
  driver it is trusted host-wide, for every name; the guide says it
  goes into the host's store.
- **`agent-openshell-logs`, systemd branch:**
  - the sandbox's name comes from the unit's instance name, which
    `agent-admin` checks;
  - `AGENT_LOGS_SINCE` only reaches `date -d` and an epoch;
  - `exec` makes `journalctl` the command the relay runs.
- **The relay's stop (#231):** the unit's kill mode is
  `control-group`, so `journalctl` or `podman logs` ends with the unit,
  on a stop and on a restart. No case was found where it outlives the
  unit.
- **SELinux:**
  - the `module_request` rule is a `dontaudit`: it hides the refusal
    and allows nothing;
  - the journal directories' `watch`, `getattr` on xattr file systems
    and tmpfs, running `journalctl` in the relay's own domain, and
    reading sysfs are each needed for their use.
- **The driver setting:** `edit` refuses to change it. "host" as the
  image only for the systemd driver. The rootless user is skipped for
  the systemd driver and checked otherwise.
- **Cockpit:** the backend's arguments are fixed arrays given to
  `cockpit.spawn` as argv, with no shell. Template text goes through
  `el()`.
- **The agent image:** the playbooks are the package's read-only
  files, copied from a fresh build context.
