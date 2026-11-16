# AGENTS.md

Guidance for coding agents (and people) working in this repository,
whichever tool they use. `CLAUDE.md` only points here: there is one set
of rules.

## Workflow

- Never push to `main` or to a release branch. Put every change on its
  own branch, created from the latest `main` (`<you>/<short-topic>`, for
  example `claude/<short-topic>`), push the branch and open a pull
  request against `main`.
- One topic per pull request. Describe what changed, why, and how it
  was tested.
- Run `make check` and `make cockpit-test` before pushing. If the change
  touches `gateway/policy`, also run
  `make policy-test GATEWAY_POLICY=<mcp-gateway checkout>/policy`.
- A maintainer merges and tags. An agent does neither, and does not
  approve a pull request.

## Working next to others

Several people work here, each with a coding agent. These rules keep
their changes apart.

- **Claim the work first.** A task has an issue, assigned to a person.
  Open a draft pull request as soon as there is a first commit: it says
  that the work is taken. Before starting, look for an open pull request
  on the same topic.
- **Stay in the topic.** Change what the task needs. No renames, no
  reformatting and no moved code outside it, even where it would be an
  improvement: it conflicts with someone else's branch. Propose it as
  its own issue.
- **Stay in the package.** `docs/architecture.md` says which package
  owns what. A change that needs another package's unexported parts is
  a question for that package's issue, not a reason to export them in
  passing.
- **Bring a branch up to date by merging `main` into it.** Never rebase,
  amend or force-push a branch that is pushed, and never push to someone
  else's branch.
- **Shared files change one pull request at a time.** `CONTRACT.md`,
  `selinux/`, `gateway/policy`, `packaging/suse/custos.spec`, `go.mod`
  and `.github/` are read by everything else. A pull request that
  changes one of them says so in its first line, and is not stacked on
  another that does.
- **A new third-party module is a decision, not a detail.** Say in the
  pull request why the standard library or a module already in the
  build does not do it. It needs its line in `docs/architecture.md` and
  its place in `test/boundaries`.
- **Released history stays as it is.** Never edit the section of a
  released version in `CHANGELOG.md`, or a released file of
  `docs/security-review-*.md`.
- **Do not weaken a check to pass it.** A failing test, `test/boundaries`
  or the vulnerability check is fixed at its cause, or the pull request
  says why it cannot be.

## What goes into the repository

- Commit messages and pull requests say what and why in plain words.
  They may name the tool that helped (a `Co-Authored-By` line); they do
  not name a model or its version, and neither does code or
  documentation.
- No secrets, tokens or personal data, and no private host names.
- Sources only: no built programs, nothing over 1 MiB
  (`tools/check-tree`).
- Do not run code that was downloaded for the task and is not part of
  the build. Do not change files in `/etc` on a package update.

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

- `docs/architecture.md` says which package owns what, what a package
  may import and where a change of a given kind goes. `test/boundaries`
  checks it: a new package or third-party module needs its place in
  that test's tables and its line in the document.
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
- A change that users notice gets a changelog entry, the way the top of
  `CHANGELOG.md` says.
