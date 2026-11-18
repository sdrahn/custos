OpenShell agents on OpenShell's systemd compute driver, and the
OpenShell side set up by `agent-admin`. An agent in an OpenShell
sandbox can now run on the OpenShell systemd compute driver as well as
on Docker or Podman (`sandbox.openshell_driver: systemd`). Its sandbox
is then systemd units on the host, with no image to build: the
workload runs the host's `agent-launcher` as a user of its own and
reads its records from the journal. `agent-admin openshell-setup`
checks what OpenShell needs on the host and configures it, for either
driver, where chapter 11 had steps by hand. Agent templates choose
where they run at creation (`--sandbox openshell
--openshell-driver …`, and in Cockpit's new-agent form). The shipped
playbooks are tested on every backend and driver, which found that the
OpenShell agent image lacked them. Every SELinux check of the
functional tests now reads the audit log itself: `ausearch` had found
nothing on Leap 16, and the denials it had hidden are fixed. The
security review before 0.8 (`docs/security-review-0.8.md`) went
through what changed since 0.7.0; it found that `openshell-setup`, as
root, followed links in the OpenShell user's home, which is fixed
before this release. The systemd driver is a technology
preview: new, and not yet tested on SLES 16 itself.

Upgrading from 0.7.x needs no change to configurations or gateway
data; the contract gains names and fields (`sandbox.openshell_driver`,
`driver` in `/v1/status`'s `lifecycle`, `backends` in a template),
renames none, and mcp-gateway 1.16 stays the minimum. To know:

- The OpenShell backend needs OpenShell 0.1.8 or later, and the systemd
  driver 0.1.4 or later.
- OpenShell agents in containers: rebuild the agent image with the
  playbooks in its build context (chapter 11, step 2), then set each
  agent's gateway credential again, so that its sandbox has them.
- An OpenShell agent's `gateway.oauth.refresh_before_seconds` should be
  at least 90; `agent-admin check` notes a shorter one.
- With the systemd driver, the relay's `journalctl` may read the whole
  journal, not only the sandbox's unit (security review 0.8, S6), and
  the CA given to `openshell-setup --ca` is trusted host-wide.
- Known and accepted, as in 0.7.0: five vulnerabilities in the HTTP/2
  of `golang.org/x/net` v0.58.0 (GO-2026-6603, -6610, -6611, -6612,
  -6617), whose fix needs Go 1.26. The agent serves no HTTP/2
  (security review 0.7, R2).
