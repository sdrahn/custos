OpenShell agents easier to set up, from the experience of the first
deployment on SLES 16.1. An agent's OAuth client in Keycloak is one
command, `agent-admin idp-client NAME --keycloak --apply`, or one form
in the agent's Credentials section in Cockpit: no `kcadm.sh`, no
container commands and no secret in a file. Every shipped template can
run in an OpenShell sandbox, so a transactional host has templates for
it. `agent-admin openshell-setup` names the helper's journal when the
systemd compute driver's helper does not start, an OpenShell agent's
name is checked against OpenShell's limit when it is created, and the
guide gives the systemd driver's setup in order, explains which
certificate `--ca` takes and covers a Keycloak in a container. The
relay's SELinux domain reads root's OpenShell registration only, no
longer every user's `~/.config` (security review 0.8, S5). The security
review before 0.9 (`docs/security-review-0.9.md`) went through what
changed since 0.8.0; it found that `--apply` allowed the Keycloak
administrator's password over plain HTTP to this host's loopback and
could replace another client than the agent's, which is fixed before
this release.

Upgrading from 0.8.x needs no change to configurations or gateway data;
mcp-gateway 1.16 stays the minimum. To know:

- An OpenShell agent's name has at most 13 characters. One with a
  longer name never got a sandbox (OpenShell refused it) and its
  configuration is now refused: delete it and create it again with a
  shorter name.
- The package restores the label of `/root/.config/openshell` on
  update. After copying OpenShell's registration there by hand, run
  `restorecon -R /root/.config/openshell`, or `agent-admin
  openshell-setup`.
- `idp-client --keycloak --apply` logs in as a Keycloak user that may
  manage the realm's clients, over HTTPS; this host must trust
  Keycloak's certificate. It replaces a client of the agent's id, with a
  new secret.
- On a transactional system the systemd compute driver 0.1.4 needs its
  mount points made once (the driver's guide, troubleshooting), until a
  driver release that ships them.
- Known and accepted, as in 0.8.0: five vulnerabilities in the HTTP/2
  of `golang.org/x/net` v0.58.0 (GO-2026-6603, -6610, -6611, -6612,
  -6617), whose fix needs Go 1.26. The agent serves no HTTP/2
  (security review 0.7, R2).
