# Security review before 0.7.0

A focused review of what 0.7.0 changes since `v0.6.2`. It covers `main`
at `e306d23`:

- the Go modules that replace code Custos had written itself:
  go-systemd's journal writer (#192), go-udiff (#193), `x/oauth2` and
  golang-jwt (#194), and the OpenAI Go SDK (#195);
- `agent-admin`'s command table with the standard library's `flag`
  (#203);
- the split of `internal/admin` into packages, with `internal/host`,
  `internal/category`, `internal/gatewayconf` and `internal/gatewayctl`
  (#196–#202), and the split of the largest files (#204);
- the functional tests in one file per topic (#206), and the packaging.

0.7.0 adds no feature, so the question is narrower than before: did
moving the code, or a module in the place of Custos's own code, weaken
anything? The threat model and the yardstick are those of
[the 0.5 review](security-review-0.5.md): a compromised or
prompt-injected agent or model, a malicious tool output, a local user
without privileges, and root on one host. The reviewer wrote the
changes; the review went through the difference between the two
versions, not through the pull requests' descriptions, and read the
modules' code where a finding or a "sound" below depends on it.

**Where it stands.** No finding is high, medium or low. Two of the
three are fixed with this review; R2 needs the maintainer's decision:

| Finding | Severity | Status |
|---|---|---|
| R1 | info | fixed: the claim corrected (changelog, code comment) |
| R2 | info | open: the modules were not checked against a vulnerability database |
| R3 | info | fixed: `--` ends `agent-admin`'s options |

## R1 — info — a large record does not reach the journal as a record from an agent under SELinux

*Where:* `internal/journal/journal.go`: `native.Log`; go-systemd
v22.7.0, `journal/journal_unix.go`: `Send`, `tempFd`.

The changelog said that a record too large for a datagram now reaches
the journal, where it was kept as a JSON line on standard error
before. go-systemd sends such a record as a file: it creates one in
`/dev/shm`, unlinks it at once and passes its descriptor to journald.
(The code comment said "memfd"; it is a file in `/dev/shm`, mode 0600.)

`agent_t` (`selinux/custos.te`) has no rule to create files on a tmpfs,
and should not get one for this. Under SELinux in enforcing mode, which
is SLES 16's default, the file is refused, `Send` returns the error,
and the record is kept as a JSON line on standard error: exactly what
0.6 did. Nothing is lost against 0.6, and nothing is exposed. What was
wrong is the claim, and each such record leaves a denial in the audit
log, which the `selinux-denials` playbook would show.

This was read from the policy and the module's code, not measured on a
host: no functional test writes a record that large.

*Fix (with this review):* the changelog entry and the comment say what
happens. `agent-admin`'s records (root, not confined) do reach the
journal as records at any size.

*Left as it is:* a rule that lets `agent_t` create the file, and
journald read it, would widen the agent's domain for a rare case. A
`dontaudit` rule for the denial is possible if the denials turn out to
be noise.

## R2 — info — the new modules were not checked against a vulnerability database

*Where:* `go.mod`.

The build gains seven modules, and two become direct:

| Module | Version | In the build as |
|---|---|---|
| `github.com/openai/openai-go/v3` | v3.50.0 | the model client (the agent) |
| `github.com/tidwall/gjson`, `sjson`, `match`, `pretty` | v1.19.0, v1.2.5, v1.1.1, v1.2.1 | the SDK's JSON helpers (the agent) |
| `github.com/coreos/go-systemd/v22` | v22.7.0 | the journal writer (the agent, `agent-admin`) |
| `github.com/aymanbagabas/go-udiff` | v0.4.1 | `agent-admin compare` |
| `golang.org/x/oauth2` | v0.36.0 | `agent-admin check-token` (was indirect) |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | `agent-admin check-token` (was indirect) |

`go mod verify` passes: the modules are the ones `go.sum` names. None
of the SDK's Azure or AWS packages is compiled (`go list -deps`).

Not done: a check of these versions against the Go vulnerability
database. The review's environment could reach neither `vuln.go.dev`
nor `osv.dev`, `govulncheck` is not part of CI, and Dependabot alerts
are switched off for the repository. The reviewer knows of no open
advisory for these versions, which is a recollection and not a check.

*Proposed:* one of
- a CI job that runs `govulncheck ./...` (a pinned version), which
  also reports only the vulnerable functions the code reaches; or
- Dependabot alerts for the repository (a setting, no code).

Until one of them has run, 0.7.0's modules are unchecked in this
respect, as 0.6's were.

## R3 — info — `--` did not end `agent-admin`'s options

*Where:* `cmd/agent-admin/main.go`: `parse`.

`parse` reads options and arguments in any order by parsing again
after each argument. After `--` it went on doing so, so
`agent-admin delete -- --keep-state` still read `--keep-state` as the
option. A caller that puts a value it does not control after `--`, as
is the convention, would not have been protected.

No caller does: the units pass an instance name that starts with the
agent's name (`agent-admin trigger %i`, `event %i`, `fence %i`), and
Cockpit passes values of an administrator who may run any command.

*Fix (with this review):* everything after `--` is an argument.
`TestParse` covers it, and `--` as an option's own value
(`--from --`).

## Checked and sound

- **The seal (security review 0.6, N1, N2):**
  - Every write of an agent's gateway data still goes through
    `Admin.WriteGateway`, which verifies the entry before it signs
    (`gatewayYAML`). The packages on top (`admin/templates`,
    `admin/promotion`) can only say whether a change takes from the
    agent; they cannot reach the signing or the file another way.
  - The evaluator, now in `admin/promotion`, runs a rollback health
    check only from data the seal covers, as before.
- **The two sides:** `test/boundaries` holds that the agent's packages
  import nothing of `agent-admin`'s (`internal/admin`, `internal/host`,
  `internal/gatewayctl`, …) and the other way round. `agent-launcher`
  links `playbook`, `config`, `journal`, `clienttools`, `gateway`,
  `governance`, `peercred`, `agui`, `openai` and `launcher`, and nothing
  that talks to the gateway's control socket.
- **File modes:** every `WriteFile` and `MkdirAll` moved with its mode
  (the difference of the two versions shows each mode line once
  removed and once added, unchanged): `agent.yaml` 0640, the credential
  store 0700, an event's state 0600.
- **`agent-admin`'s options (#203):**
  - An option a command does not have is refused. Before, it was
    ignored: `delete NAME -h` deleted the agent and its state.
  - `flag` has no abbreviations, so no option can be reached by a
    prefix of its name.
  - `--accept`, `--add`, `--remove` take one value each; a second
    value without its option is refused, not silently dropped.
  - A value-taking option at the end is an error, not an empty value.
- **The journal (#192):**
  - Field names: Custos's own check (`ValidField`) is stricter than
    go-systemd's and runs first; every name sent starts with `AGENT_`
    or is one of `MESSAGE`, `MESSAGE_ID`, `PRIORITY` and
    `SYSLOG_IDENTIFIER`. A value with a newline is sent with its
    length, so it cannot start a field.
  - journald sets the trusted fields (`_PID`, `_UID`,
    `_SYSTEMD_UNIT`), as before.
  - A send that fails keeps the record as a JSON line.
- **`check-token` (#194):**
  - The secret goes in the `Authorization` header only
    (`AuthStyleInHeader`; no second attempt with the secret in the
    body), with the same HTTP client and its 30-second timeout.
    `x/oauth2` reads at most 1 MB of the answer.
  - `ParseUnverified` is used on purpose: `check-token` shows and
    compares claims and decides nothing. The gateway verifies.
  - The secret is not in any error text.
- **`compare` (#193):** go-udiff gets two texts Custos made itself
  (canonical YAML) and returns edits; nothing of it is executed or
  written. It has no dependencies.
- **The model client (#195):**
  - Not from the environment: the service is made from options alone.
    The SDK reads `OPENAI_*` only in `NewClient`'s defaults, which is
    not called; a test sets `OPENAI_API_KEY` and `OPENAI_BASE_URL` and
    sees neither used.
  - No retries (`WithMaxRetries(0)`): one request per call, so the
    activity's limits hold. A test counts the requests.
  - No `X-Stainless-*` header: the model server learns the SDK's name
    and version (`User-Agent: OpenAI/Go 3.50.0`), not the host's
    system, architecture or Go version.
  - The API key is sent as before; Go's HTTP client drops
    `Authorization` on a redirect to another host.
  - The answer is read up to 32 MB, as before.
  - The tools' schemas are put in with `WithJSONSet` at
    `tools.N.function.parameters`, N a number Custos counts; the
    value is `json.Marshal`'s output, so a tool's schema cannot leave
    its place in the request.
  - What a tool returns still reaches the model through the host's
    notice (#172); the client only carries it.
- **The functional tests (#206):** `run-tests.sh` loads `lib.sh` and
  `sections/` from its own directory, which the package installs as
  root's (0644), not from the working directory.
- **Packaging:** the spec gains the two entries of the tests'
  files; nothing in `/etc` is touched on update. The build's other
  architectures (arm64, s390x, ppc64le) compile and pass `go vet`.
