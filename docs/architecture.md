# Architecture

How the code is divided, for people who work on it. What the parts
promise each other is in [CONTRACT.md](../CONTRACT.md); this document
says which package implements which part of it, what a package may
import, and where a change of a given kind goes.

The rules below are checked by `test/boundaries` (part of `make
check`). A new package, or a new third-party module, fails that test
until it has its place in the test's tables and its line here.

## The sides

Custos is several programs that meet at interfaces the contract owns,
never in shared code paths:

| Side | Runs as | Programs | Meets the others at |
| --- | --- | --- | --- |
| The agent's process | the agent's user, in its unit or sandbox | `agent-launcher` | its socket `/run/agents/<name>/api.sock` (§5, §11), the journal (§10), mcp-gateway's socket (§6) |
| Root's tools | root | `agent-admin`, `custos-setup` | the agents' sockets, the journal, the gateway's data and control socket (§6, §13) |
| The OpenShell relay | root, one per sandboxed agent | `agent-openshell-relay` | the agent's socket on the host, OpenShell's port forward (§16a) |
| The delegation server | mcp-gateway's server user | `agent-delegate` | MCP (to the gateway), the agents' sockets (§9) |

Security properties are enforced by mcp-gateway, systemd and SELinux
(§1), so no side trusts code of another side by linking it: the agent's
process does not import root's packages, and root's tools do not import
the agent's.

The Cockpit page (`cockpit/custos`) is a fifth part. It uses the control
API, the journal and `agent-admin`, and no Go package.

## Packages

A package imports only packages from rows above its own layer.

| Layer | Package | Side | What it owns | Contract |
| --- | --- | --- | --- | --- |
| 0 | `internal/journal` | both | writing audit records to journald (through go-systemd), the record names and fields | §10 |
| 0 | `internal/peercred` | both | who is calling on a unix socket (SO_PEERCRED) and what they may reach | §2, §5 |
| 0 | `internal/playbook` | both | reading diagnostic playbooks | §14c |
| 0 | `internal/gateway` | agent | the connection to mcp-gateway's MCP endpoint, its approval notices | §6, §7 |
| 0 | `internal/clienttools` | agent | tools an AG-UI frontend defines, per session | §11 |
| 0 | `internal/openai` | agent | the model client for OpenAI-compatible endpoints | §14 |
| 0 | `internal/gatewayctl` | root | mcp-gateway's control API: notices to approvers, a server's instances | §7, §8 |
| 0 | `internal/readiness` | root | promotion readiness measured from records and the gateway's audit log; the review sample; the diagnostics history | §6a, §4a |
| 0 | `internal/testutil` | tests | a scripted model | |
| 1 | `internal/config` | both | `agent.yaml`: reading and checking | §14 |
| 2 | `internal/governance` | agent | the plugin every agent loads: local limits, records, activities, pause, diagnosis reports, marking tool output as data | §4, §4a, §8, §10 |
| 2 | `internal/delegate` | delegation | the `agents` MCP server's one tool | §9 |
| 2 | `internal/openshell` | root | an agent's configuration as OpenShell's policy, provider and arguments | §3a, §16a |
| 2 | `internal/relay` | relay | the host side of a sandboxed agent | §16a |
| 2 | `internal/category` | root | the task categories' data: catalog, state, envelope | §6a |
| 2 | `internal/host` | root | the system root's tools work on: files below a root, commands, clock, person, root's records | |
| 3 | `internal/agui` | agent | the AG-UI endpoint: runs in, events out | §11 |
| 3 | `internal/gatewayconf` | root | what Custos reads of mcp-gateway's configuration (its HTTPS listener, the name agents reach it by) | §16a |
| 3 | `internal/host/hosttest` | tests | a host for tests: temporary files, recorded commands, fixed clock | |
| 4 | `internal/launcher` | agent | assembling one agent; the control API's handlers; sessions | §5, §16 |
| 4 | `internal/admin` | root | the core of `agent-admin`: an agent's lifecycle, its gateway data, mode, permissions, owners, credentials, schedules and events, the network fence, the emergency stop, category enablement and the seal | §6, §6b, §8, §13, §15, §17 |
| 5 | `internal/admin/templates` | root | agent templates, model defaults, compare and apply | §14a, §14b |
| 5 | `internal/admin/promotion` | root | readiness, promote, demote, review, the demotion evaluator, feedback | §6a, §4a |
| 5 | `internal/admin/idp` | root | the OAuth client of an OpenShell agent at the site's identity provider; checking a token as the gateway will | §16a |
| 5 | `internal/admin/admintest` | tests | `agent-admin` on a test host, with the shipped categories and an agent | |
| 6 | `internal/admin/setup` | root | `custos-setup`: the bundled diagnostics install | §17a |
| 7 | `cmd/agent-launcher`, `cmd/agent-delegate`, `cmd/agent-openshell-relay`, `cmd/agent-admin`, `cmd/custos-setup` | | the programs: arguments, environment, exit status | |

The packages on top of `internal/admin` (layers 5 and 6) are functions
that take the `*admin.Admin` first. They use its exported API and never
reach into its files, so each can be worked on by itself. What several
of them need of the core is exported there (`ReadGateway`,
`WriteGateway`, `CheckCategoryEnabled`, …); adding to that API is a
change of `internal/admin`.

## Where a change goes

| To change | Work in | Also touch |
| --- | --- | --- |
| an `agent-admin` command's options or output | `cmd/agent-admin/cmd_<group>.go` (one file per group; the table in `main.go` names the groups) | the usage text in `main.go`, CONTRACT.md §17 |
| what a command does | the group's package (see the head of `cmd/agent-admin/main.go`) | CONTRACT.md if it owns the behaviour |
| a field of `agent.yaml` | `internal/config` | CONTRACT.md §14, the user guide |
| an audit record or one of its fields | `internal/journal` (names), then the writer: `internal/governance` (`run.go`, `model.go`, `tool.go`, `activities.go`) for the agent's, `internal/admin` and the packages on it for root's | CONTRACT.md §10, `internal/readiness` if a measure reads it |
| the control API | `internal/launcher` (`control.go`, `activities.go`, `playbooks.go`) | CONTRACT.md §5, `test/contract` (the checks every agent host passes), `test/vm/secondhost` |
| AG-UI events | `internal/agui` | CONTRACT.md §11 |
| how readiness is measured | `internal/readiness` (`readiness.go` the measure, `records.go` the parsing, `review.go`, `diagnostics.go`, `history.go`) | CONTRACT.md §6a |
| the gateway's policy for agents | `gateway/policy` | `make policy-test`, CONTRACT.md §6 |
| a template, a category, a playbook | `templates/`, `packaging/categories`, `packaging/playbooks` | the spec's file list for a new file |
| the Cockpit page | `cockpit/custos` | `make cockpit-test` |
| units, SELinux, packaging | `systemd/`, `selinux/`, `packaging/suse` | `test/units`, the spec's file list |

Part I of the contract (the control plane) is independent of the agent
host. In code that means: the ADK (`google.golang.org/adk`,
`google.golang.org/genai`) is imported only by `internal/launcher`,
`internal/governance`, `internal/agui`, `internal/clienttools` and
`internal/openai` (and the scripted model for tests). Root's tools and
the Cockpit page know an agent only by its socket, its records and its
gateway data, which is what lets a second host (`test/vm/secondhost`)
pass the same checks (`test/contract`).

## Tests

| Kind | Where | Runs with |
| --- | --- | --- |
| Unit and end-to-end tests of a package | next to the code (`*_test.go`); a whole agent with a scripted model in `internal/launcher` | `make check`; no network, model or gateway |
| The contract's checks for any agent host | `test/contract`, used by `internal/launcher` and `test/vm/secondhost/host` | `make check` |
| The import rules of this document | `test/boundaries` | `make check` |
| The user guide and release files | `test/docs`, `test/tools` | `make check` |
| Units as systemd reads them | `test/units` | `make check` |
| The Cockpit page in a browser | `cockpit/test` | `make cockpit-test` |
| The gateway's policy | `gateway/policy` | `make policy-test GATEWAY_POLICY=…` |
| A real system: systemd, mcp-gateway, OPA, SELinux | `test/vm/run-tests.sh` (package `custos-tests`), with its fake model, MCP server and second host | CI's VM jobs, openQA (`test/openqa`) |
| OpenShell | `test/openshell` | CI's OpenShell jobs |

Tests of the packages on `internal/admin` start from
`admintest.New` or `admintest.WithAgent`; tests in `internal/admin`
itself use `hosttest` directly (a package cannot import a helper that
imports it).

## Third-party modules

Where a maintained module does a job, Custos uses it instead of its own
code. Each is imported by the packages named here and no other, so
replacing one is a change of one package.

| Module | For | Used in |
| --- | --- | --- |
| `google.golang.org/adk`, `google.golang.org/genai` | the agent host | the agent's packages (above) |
| `github.com/modelcontextprotocol/go-sdk` | MCP, to the gateway and as the delegation server | `internal/gateway`, `internal/governance`, `internal/launcher`, `internal/delegate` |
| `github.com/openai/openai-go/v3` | the requests and answers of OpenAI-compatible chat completions | `internal/openai` |
| `github.com/coreos/go-systemd/v22` | journald's protocol | `internal/journal` |
| `golang.org/x/oauth2`, `github.com/golang-jwt/jwt/v5` | the client credentials grant; reading a token's claims | `internal/admin/idp` |
| `github.com/aymanbagabas/go-udiff` | the differences `agent-admin compare` shows | `internal/admin/templates` |
| `gorm.io/gorm`, `github.com/glebarez/sqlite` | the sessions' store (the ADK's database session service, with SQLite in Go) | `internal/launcher` |
| `gopkg.in/yaml.v3` | configuration and gateway data | `internal/config`, `internal/category`, `internal/playbook`, `internal/openshell`, `internal/gatewayconf`, `internal/admin` and the packages on it |
| `golang.org/x/net` | DNS messages, to resolve the gateway's name as OpenShell does (by DNS, not `/etc/hosts`) | `internal/gatewayconf` |
| `github.com/google/uuid` | identifiers of runs and calls | `internal/agui`, `internal/openai` |

`agent-admin`'s commands use the standard library's `flag`, one flag
set per command; no command-line framework.

`openai-go` is pinned to v3.50.0, the last version that builds with Go
1.25. Later versions need Go 1.26 and bring the Azure and AWS SDKs
into the build; to be looked at again when Custos moves to Go 1.26.
`internal/openai` switches off what an on-premises agent must not do
by default: the SDK's retries, its `X-Stainless-*` headers, and reading
`OPENAI_API_KEY` and `OPENAI_BASE_URL` from the environment.

Considered and not used:

- **The AG-UI community Go SDK.** It has no tagged release, so a build
  would depend on a commit of a moving branch. `internal/agui` is the
  small part of the protocol the contract names (§11) and is checked by
  the Cockpit tests; to be looked at again when the SDK has releases.
- **Netlink `sock_diag` for the relay's listener check.** The relay
  reads a listening socket's owner from `/proc/net/tcp`. Netlink
  returns the same value from the same kernel structure, with more code
  and a dependency; the check stays as it is.
