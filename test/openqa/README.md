# Custos in openQA

The functional tests come as the package `custos-tests`. They
are meant for openQA and other test systems that test a product with
the packages from its repositories. They follow mcp-gateway's
`mcp-gateway-tests`, so both can run in one job.

## What the package holds

| Path | What |
|---|---|
| `/usr/libexec/custos/tests/run-tests.sh` | the tests (`test/vm/run-tests.sh` in the source), with `lib.sh` and the checks by topic in `sections/` next to it |
| `/usr/libexec/custos/tests/fakemodel` | a scripted OpenAI-compatible model server on `127.0.0.1:8008`, so no model is needed and every run gives the same answers |
| `/usr/libexec/custos/tests/testsrv` | the MCP server `custos-test`: `probe` (declared read-only by the tests) and `change` (declared as changing the system) |

It requires `custos` and `mcp-gateway`, plus what the tests
use: `curl`, `jq`, `util-linux` (nsenter, runuser), `shadow`
(useradd), `audit` and `policycoreutils`.

## Running them

As root, on a system that is there for testing:

```bash
zypper -n in custos-tests custos-selinux
/usr/libexec/custos/tests/run-tests.sh --disposable --results /var/tmp/custos-tests
```

With a model server on the system (or the local network), add
`--model URL NAME`, such as `--model http://127.0.0.1:11434/v1
qwen3:0.6b`: an agent on that real model then has to call a read-only
tool and ask for a change, which the tests approve. On a system without
network this is the air-gapped gate of a release (`test/airgap`).

- **They change the system for good.** They create the agents `t1` and
  `t2` with their users, and the user `custos-tester`. They register
  the MCP servers `custos-test` and `agents` with the gateway, and
  trigger and release the emergency stop. They delete the agents again,
  but the users stay. Without `--disposable` they refuse to start (exit
  status 2).
- **Results:** every check prints `PASS:`, `FAIL:` or `SKIP:`, in
  sections (`=== Approval: approve`). With `--results DIR`, the script
  writes `results.tap` (TAP) and `results.xml` (JUnit, one test suite
  per section) there. Exit status is 0 when nothing failed, 1 otherwise.
- **SELinux:** with SELinux enforcing, the tests check that agents run
  in `agent_t` and that no denials were logged for the module's types.
  Otherwise those checks are `SKIP`.
- **Time:** a few minutes. A timeout of 30 minutes leaves room.

## What they cover

| Section | Checks |
|---|---|
| Preconditions, Setup | packages; gateway running; the agents' policy module installed; the fake model and the test server registered |
| Creating an agent | `agent-admin check`/`create`, refusals; the user, its groups, file modes; socket unit; supervised with no permissions; schedule timer |
| Discovery | `/v1/status` (contract 1, units); socket owner and mode; runs as `agent-t1`, in `agent_t` |
| Sandbox | no control socket and no other agents' sockets in the agent's namespace; `/etc` read-only; unit properties |
| Supervised by default | no permissions → no tools; a read-only tool runs without approval; journal records, arguments only as a digest |
| Approval: approve / deny | the call waits; the gateway lists the approval for `agent-t1`; journal `approval.pending` with the id and the rationale; decided through the gateway's control API; the activity's call `approved`/`declined`; the change ran or did not |
| Autonomy modes | advisory hides the change; bounded runs an autonomous change without an approval, audited by the gateway as `agent-t1` |
| Pause, Cancel | gateway data and status; new activities refused (409); resume; cancel of a waiting activity, journaled |
| Schedules | the timer's run is an activity with origin `schedule` |
| AG-UI | the event stream of a run; a client tool not allowed is journaled |
| Access to the socket | outsiders refused; members of `agent-clients` read the status but not other users' sessions, cannot pause; ADK's builder endpoint 404 |
| Delegation | `t2` delegates to `t1` through the gateway; `t1` records origin and parent; the gateway audits the delegation; a target outside the permission is denied |
| Emergency stop | `stop-all`: gateway flag, all units and timers stopped, an agent started by hand still denied; `release-all` |
| SELinux | no denials for the agents' types |
| Removing | `agent-admin delete` removes configuration, gateway data, state, schedule |

## An openQA test module

[custos.pm](custos.pm) is meant for
[os-autoinst-distri-opensuse](https://github.com/os-autoinst/os-autoinst-distri-opensuse)
as `tests/console/custos.pm`. It installs the package, runs
the tests, and hands the JUnit results to openQA (`parse_extra_log`),
so each check is a result of its own. On failure it uploads the
journal of the gateway and the agents, the agents' records as JSON, and
the AVC records. Schedule it after the system is installed and
registered, with the repositories that hold the `mcp-gateway` and
`custos` packages. It can run in the same job as
`mcp_gateway.pm`, after it.

## Adding tests

A feature that shows on an installed system gets checks here, in the
pull request that adds it (CLAUDE.md):

```bash
section "My feature"
check "what must hold, in words" some_command args   # PASS or FAIL
check "it settles within 30 s" eventually 30 some_command args
skip "why this cannot be checked here"               # SKIP
```

Helpers: `api <agent> <path>` (the agent's socket), `gw <path>` (the
gateway's control API), `run_activity`, `start_activity`, `activity`,
`journal_has <agent> <event> <jq condition>`, `gw_audit_has`, `not`,
`json_ok`, `code_is`.
