# CLAUDE.md

Guidance for Claude Code (and other agents) working in this repository.

## Workflow

- Never push to `main`. Put every change on its own branch, created
  from the latest `main` (for example `claude/<short-topic>`), push the
  branch and open a pull request against `main`.
- One topic per pull request. Describe what changed, why, and how it
  was tested.
- Run `make check` before pushing. If the change touches
  `gateway/policy`, also run
  `make policy-test GATEWAY_POLICY=<mcp-gateway checkout>/policy`.

## Tests

- Everything implemented comes with tests in the same pull request.
- Unit and end-to-end tests are Go tests (`make check`); they need no
  network, no model and no running gateway.
- Behaviour on a real system (systemd, mcp-gateway, OPA, SELinux) is
  covered by the functional tests in `test/vm/run-tests.sh`. They are
  shipped as the subpackage `custos-tests` and run by openQA
  (`test/openqa/`). A new feature that shows on an installed system
  gets a check there, too, in the file of its topic in
  `test/vm/sections/`: `check <description> <command…>` in a
  `section`, printing PASS, FAIL or SKIP, written as TAP and JUnit with
  `--results DIR`.
- The functional tests change the system they run on and refuse to
  start without `--disposable`.

## Where things are

- `CONTRACT.md` is the interface between all parts. Part I (the
  control-plane contract) must stay independent of any agent host.
  Within contract version 1, names, paths and fields are added, never
  renamed or removed.
- Code that changes a contract-owned interface (the control API,
  journal fields, AG-UI events, the gateway agent data) changes
  `CONTRACT.md` in the same pull request.
- Security properties (supervised by default, approvals, tool
  boundaries, the emergency stop) are enforced by mcp-gateway, systemd
  and SELinux, never only by code inside the agent process.
- Packaging for SLES 16 and Leap 16 is in `packaging/suse`. A new
  installed file needs an entry in the spec's file list.
