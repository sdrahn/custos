# Changelog

All notable changes to Custos. Versions follow
[Semantic Versioning](https://semver.org/). Within contract version 1
(CONTRACT.md), names, paths and fields are added, never renamed or
removed.

The changes since the last release are not in this file: each is a
file in `changes.d/` ([changes.d/README.md](changes.d/README.md)),
added by the pull request that makes the change. `tools/changelog show`
prints them as the next release's section; a release writes them here.
The sections below are released and stay as they are.

## v0.9.18 — 2026-11-26

Tool calls of agents in OpenShell survive OpenShell closing the
sandbox's connections: a call no longer fails at once on a connection
OpenShell had closed, and a call cut off while it runs is sent again
and answered by the gateway from the first, which runs once.

Upgrading from 0.9.17 needs mcp-gateway 1.17.2 or later (its
idempotency keys), and no change to configuration or gateway data.
Restart the gateway after updating it.

### Changed

- An agent in OpenShell whose tool call is cut off while it runs (the
  connection closed without an answer, as OpenShell does at every token
  renewal) asks the gateway for the call's outcome instead of reporting
  a failure that may in fact have run: each tool call carries an
  idempotency key, and the gateway answers a call sent again with it
  from the first one, which runs once (mcp-gateway 1.17.2, requirement
  R8). Custos needs mcp-gateway 1.17.2 or later now.

### Fixed

- An agent in OpenShell no longer has a tool call fail with `EOF` right
  after OpenShell closed the sandbox's connections (at a token renewal,
  or when another sandbox was set up): each tool call goes to the
  gateway on a connection of its own, never on an idle one OpenShell
  may have closed. A call cut off while it runs is sent again with its
  idempotency key (below).

## v0.9.17 — 2026-11-26

Agents in OpenShell can delegate and be delegated to: delegation from
one failed with "permission denied". And feedback on a diagnosis report
longer than 4 KB is recorded: it was refused as "no diagnosis report",
and readiness left such reports out of an agent's diagnosis history.

Upgrading from 0.9.16 needs mcp-gateway 1.17.1 or later, and no change
to configuration or gateway data, except one: copy
`/usr/share/custos/gateway/servers.d/agents.yaml` to
`/etc/mcp-gateway/servers.d/` again, since an earlier copy runs the
delegation bridge as the delegating agent's user, then reload the
gateway. Reports written before the update are in the journal: after
it, they take feedback and count towards readiness.

### Fixed

- Agents in OpenShell can delegate, and be delegated to. Delegation
  from one failed with `dial unix /run/agents/NAME/api.sock: connect:
  permission denied`: the gateway ran the bridge as the delegating
  agent's user, and an agent in OpenShell, which calls the gateway with
  a token, has none, so it got a throwaway account that may not reach
  the sockets. The bridge now runs as the system account
  `custos-delegate` for every agent. The gateway tells it which agent it
  serves (`MCP_GATEWAY_PRINCIPAL`, mcp-gateway 1.17.1); a token's agent
  counts only from an issuer trusted for agents. It names that agent to
  the other one, which believes the name from `custos-delegate` only.
  Custos needs mcp-gateway 1.17.1 or later now. After the update, copy
  `/usr/share/custos/gateway/servers.d/agents.yaml` to
  `/etc/mcp-gateway/servers.d/` again: an earlier copy still runs the
  bridge as the delegating agent's user.
- Feedback on a diagnosis report longer than 4 KB is recorded: it was
  refused with "agent … has no diagnosis report for activity …", in
  Cockpit and by `agent-admin feedback`, because `journalctl` gave the
  report's record with the report left out (a field over 4096 bytes is
  null in its JSON unless asked for `--all`). For the same reason
  `agent-admin readiness` left such reports out of an agent's diagnosis
  history (reports, verified quotes, feedback). Every reading of agent
  records by `agent-admin` now asks for `--all`. Reports written before
  the update are in the journal: they take feedback and count once it is
  installed.

## v0.9.16 — 2026-11-26

Custos's agents check the disks' health: the new playbook
`storage-health` reads each disk's SMART or NVMe health log, the btrfs
error counters and the last scrub with mcp-gateway's server `storage`
(mcp-gateway 1.17), and the kernel's I/O errors from the journal.

Upgrading from 0.9.15 needs no change to configuration or gateway data.
To use the new checks, install `mcp-gateway-storage-server` (on a
transactional system in the same transaction as `mcp-gateway-selinux`),
link its profile (`custos-setup --storage`, or by hand as chapter 9 of
the user guide says) and restart the gateway. An agent made before
0.9.16 gets the tools with `agent-admin apply-template NAME`; it keeps
its schedules, so add the weekly `storage-health` run to it yourself.

### Added

- The diagnostic playbook `storage-health` checks the disks' health with
  mcp-gateway's server `storage` (package `mcp-gateway-storage-server`,
  mcp-gateway 1.17): the disks, each one's SMART or NVMe health log with
  a verdict, the btrfs error counters and the last scrub, and the
  journal's I/O and btrfs errors. It reads only. Without the server it
  reads the journal alone and says so. The `health-check` template runs
  it every Monday, and it and the `disk-watch` templates may call the
  server's four tools, each named, without an approval.
  `custos-setup --storage` links the server's profile when the package
  is installed; the server holds capabilities and reads the disks, so
  offering it is your choice, as with `--restarts`. An agent made
  before this release gets the tools with `agent-admin apply-template`;
  it keeps its schedules, so add the weekly run to it yourself.

## v0.9.15 — 2026-11-25

`agent-admin help` is easier to read: the commands in groups of what
they are for, each with its synopsis and what it does, and help for one
command. Custos's tests now run against OpenShell 0.1.10 and the
OpenShell systemd compute driver 0.3.1.

Upgrading from 0.9.14 needs no change to configuration or gateway data.

### Changed

- `agent-admin help` lists the commands in groups of what they are for
  (agents, templates, what an agent may do, credentials, promotion, the
  host, OpenShell, the emergency stop, and what systemd units run), in
  alphabetical order within a group, each with its synopsis on a line of
  its own, what it does under it, and a blank line between two commands.
  `agent-admin help COMMAND` and `agent-admin COMMAND --help` show one
  command; a command line that is not understood shows that command's
  synopsis, not the whole help.

## v0.9.14 — 2026-11-24

`agent-admin chat` talks with an agent on the host's terminal, and the
SELinux package installs again where the build saw a newer snapshot of
the same SELinux policy than the host has.

Upgrading from 0.9.13 needs no change to configuration or gateway data.
Where `custos-selinux` could not be installed ("nothing provides
'selinux-policy >= …+git…'"), 0.9.14 installs.

### Added

- `agent-admin chat NAME` talks with an agent on the host's terminal,
  as Cockpit's chat does: each line is a message of one thread, and the
  answer is printed as it comes, with the agent's tool calls and the
  approvals it waits for. `--thread` continues a thread, `--message`
  sends one message and ends.

### Fixed

- `custos-selinux` requires `selinux-policy` by its date only
  (`>= 20260826`), where it required the build's version with its git
  snapshot (`>= 20260826+git190.1d6a6b164`): a build against a newer
  snapshot of the same policy, not yet released to hosts, could not be
  installed.

## v0.9.13 — 2026-11-24

One fix in Cockpit: the chat window opened from the list of agents keeps
the keyboard focus in its message field. Every few seconds the page's
refresh took it away.

Upgrading from 0.9.12 needs no change to configuration or gateway data.

### Fixed

- Cockpit: the chat window opened from the list of agents keeps the
  keyboard focus in its message field. Every few seconds the page's
  refresh took it away, and typing went nowhere until the field was
  clicked again.

## v0.9.12 — 2026-11-24

One fix for OpenShell agents with the systemd compute driver: `agent-admin
openshell-setup` makes sure the driver's helper and the driver's
service start at boot. A helper that ran without being enabled was gone
after the next reboot, and no OpenShell agent started.

Upgrading from 0.9.11 needs no change to configuration or gateway data.
Run `agent-admin openshell-setup --user <user> --driver systemd --check`
once: where it says "would not start at boot", run it without `--check`.

### Fixed

- `agent-admin openshell-setup` checks that the systemd compute driver's
  helper and the driver's user service are enabled to start at boot, not
  only that they run, and enables them where they are not. One that ran
  without being enabled was gone after the next boot, and OpenShell
  agents did not start; `--check` said it was fine.

## v0.9.11 — 2026-11-24

OpenShell agents get longer names and a repair for a second gateway.
An agent in an OpenShell sandbox may now have a name of up to 19
characters. `agent-admin openshell-setup` stops an OpenShell gateway
that also runs as root, which breaks root's registration so that no
OpenShell agent starts. Approval and limit records are in the journal
before their events, and `gorilla/websocket` has its fix of
GO-2026-6278.

Upgrading from 0.9.10 needs no change to configuration or gateway data;
existing OpenShell agents keep their sandboxes. If `openshell` as root
fails with `invalid peer certificate: BadSignature`, run `agent-admin
openshell-setup --user <user> --driver <driver>` once, then restart the
agents.

### Changed

- An agent in an OpenShell sandbox may have a name of up to 19
  characters, where it had 13: its sandbox is `agent-<name>` as before
  where that fits OpenShell's 19 characters, and `<name>` itself beyond.
  A name of 14 to 19 characters does not begin with `agent-` (its
  sandbox would be another agent's). Existing agents and their sandboxes
  stay as they are; `openshell.env` names the sandbox where it is not
  `agent-<name>`.

### Fixed

- An approval's pending and resolved records, and a limit's, are in the
  journal before the event that announces them reaches a chat or
  Cockpit: one that showed the event and then read the journal found no
  record, now and then.
- `agent-admin openshell-setup` stops and disables OpenShell's gateway
  and driver where they also run as another user than the gateway's,
  such as root after `systemctl --user` as root, before it writes root's
  registration again. A second gateway made a CA of its own and wrote
  its client certificates into root's registration: root's `openshell`
  then failed with `invalid peer certificate: BadSignature`, and
  OpenShell agents did not start. `--check` names it.

### Security

- `github.com/gorilla/websocket` (with `google.golang.org/genai`) at
  the commit that fixes GO-2026-6278, a weak random number generator for
  WebSocket mask keys, where it was 1.5.3; there is no release with the
  fix yet.

## v0.9.10 — 2026-11-23

Two fixes to Cockpit's pages. An agent's view no longer moves or
closes things under you every few seconds: it is redrawn only when what
it shows changed, and not while you edit in it. And the form for a new
agent no longer creates it from a configuration drafted for something
else, as when the name was too long for an OpenShell sandbox.

Upgrading from 0.9.9 needs no change to configuration or gateway data.
An agent created with "in an OpenShell sandbox" but without its sandbox
(its configuration has no sandbox block) is a systemd agent: make it
again with a name of at most 13 characters.

### Fixed

- An agent's view in Cockpit no longer moves or closes things under you
  every 5 seconds. The refresh redraws it only when what it shows has
  changed (or a minute on, for the relative times), and not while an
  editor, form or confirmation in it is open, focused or not. The task
  categories' readiness and review show their last result at once and
  are read again at most every 30 seconds. Before, the whole view was
  rebuilt at every refresh, and the readiness read anew each time
  shrank and grew the section while you scrolled or edited.
- Cockpit's form for a new agent no longer creates it from a
  configuration drafted for something else. When the draft for the
  chosen sandbox fails, Create waits, and the form says why and what to
  do, as for a name too long for an OpenShell sandbox (at most 13
  characters; a template's own name can be longer). A new name drafts
  it again. Before, the form kept the earlier configuration, for a
  systemd service, and a shorter name only renamed it, so the agent
  could be created without its sandbox.

## v0.9.9 — 2026-11-20

Changes to Cockpit's pages. An agent's chat opens in a window
over the list of agents, from a Chat button next to the agent's name,
without scrolling through the agent's whole view. That view starts with
its contents, a link to each section, kept at the top while it scrolls.
And in the Activity tab, the activity shown is level with the list;
an agent's view keeps its place across a refresh at any width. A chat
thread is free again as soon as its run has ended.

Upgrading from 0.9.8 needs no change to configuration or gateway data.

### Added

- An agent's view in Cockpit starts with its contents: a link to each
  section (autonomy mode, tool boundaries, schedules, credentials,
  configuration, chat, …), kept at the top of the window while the view
  scrolls. A link scrolls its section into view.
- Cockpit's list of agents has a **Chat** button for each agent that
  offers chat. It opens the agent's chat in a window over the list
  (`#/agents/NAME/chat`), without scrolling through the agent's whole
  view. The agent's view has the button too. Esc closes the window, and
  the conversation is the same in both places.

### Fixed

- In Cockpit's Activity tab, the activity shown on the right starts
  level with the list of activities on the left, where it started 1rem
  lower.
- An agent's view in Cockpit keeps its place across the refresh every
  5 seconds at any window width. At some widths, scrolled to the end of
  the view, the refresh moved it up by a few lines.
- A chat thread is free again as soon as a client sees its run's end.
  Since 0.9.7, an answer to a client tool, or the next message, sent at
  once could be refused with `409` ("a run is in progress on this
  thread"), because the thread was released only after the stream had
  ended.

## v0.9.8 — 2026-11-20

Two fixes for agents in OpenShell sandboxes. Their OAuth tokens can live
longer, so that they are renewed less often: each renewal closes the
sandbox's open connections, and a long call that spans one ends in
`EOF`. And Cockpit's form for a new agent shows the configuration for
the sandbox chosen last.

Upgrading from 0.9.7 needs no change to configuration or gateway data.
To renew an agent's token every 28 minutes instead of every 3, make its
client with tokens of 30 minutes:
`agent-admin idp-client NAME --keycloak --apply --admin USER --token-lifetime 30m`.

### Added

- `agent-admin idp-client NAME --keycloak [--apply] --token-lifetime D`
  sets the lifetime of the agent's OAuth tokens in Keycloak (default 5
  minutes, at most an hour). Each renewal of an OpenShell agent's token
  closes its sandbox's connections; with 30 minutes and
  `refresh_before_seconds: 120`, that happens every 28 minutes instead
  of every 3.

### Changed

- `idp-client --keycloak --apply` (and Cockpit's "Make the client and
  set the credential") keeps the token lifetime of the client it
  replaces, also one set in Keycloak's admin console. Before, it reset
  the lifetime to 5 minutes.

### Fixed

- Cockpit's form for a new agent shows the configuration for the
  template and sandbox chosen last. Choosing a template and then at once
  an OpenShell sandbox could leave the template's configuration for
  systemd in the form, whichever draft answered last. Choosing the
  sandbox again was needed.

## v0.9.7 — 2026-11-20

A fix for chat with an agent whose stream breaks off, as an OpenShell
agent's does at each token renewal. The message was sent again while
the first run still worked; both runs mixed the conversation's history,
and the model then read only part of it and answered as if it had not
read the question. A thread now has one run at a time, and the history
the model gets has each tool result after its call.

Upgrading from 0.9.6 needs no change to configuration or gateway data.
A conversation mixed before is read in order again.

### Fixed

- A chat thread has one run at a time: a message sent while the
  thread's previous run still works is refused (`409`), and Cockpit says
  so. Before, both ran and mixed the conversation's history, as after a
  dropped connection of an OpenShell agent; the model then answered as
  if it had not read the question.
- The history sent to an OpenAI-compatible model has each tool result
  right after its call: a call without a result gets a "no result"
  answer, a result without a call is left out. A history mixed before
  can be read again. A call without arguments sends `{}`, not `null`.

## v0.9.6 — 2026-11-20

A fix for agents in OpenShell sandboxes with an OAuth client: their
token is no longer renewed every minute, which closed the sandbox's
connections and ended the agent's calls in `EOF`. Setting the
credential again and editing the provider's settings work on an agent
that has a sandbox.

Upgrading from 0.9.5 needs no change to gateway data. An existing
OpenShell agent created from a template has no
`gateway.oauth.refresh_before_seconds` (`agent-admin check NAME` notes
it): add `refresh_before_seconds: 120` to its `gateway.oauth` and apply it
with `agent-admin edit NAME --from FILE`, which stops the agent; then set
its credential again (`agent-admin idp-client NAME --keycloak --apply
--admin USER`, or `set-credential`).

### Fixed

- An OpenShell agent with an OAuth client is no longer cut off every
  minute. Its token was renewed at every one of OpenShell's sweeps when
  `gateway.oauth.refresh_before_seconds` was unset (OpenShell's 300) and
  the token lived 5 minutes, as Keycloak's do, and each renewal closed
  the sandbox's connections: calls to the model and the gateway ended in
  `EOF`, and the agent lost its tool results. Agents drafted for OpenShell
  (from a template, or Cockpit's form) now get
  `refresh_before_seconds: 120`; `agent-admin check` notes an agent
  without it, and `check-token` and `idp-client --keycloak --apply` say
  when the token's lifetime does not suit the window. Correct an existing
  agent with `agent-admin edit`, then set its credential again.
- `agent-admin set-credential` (and `idp-client --keycloak --apply`) on an
  OpenShell agent that has a sandbox replaces its provider: it stops the
  relay and deletes the sandbox first. Before, OpenShell refused to delete
  the provider ("attached to sandbox(es)") and the credential was not set,
  while Keycloak already had the client's new secret.
- `agent-admin edit` of an OpenShell agent whose provider profile changes
  (`gateway.url`, or `gateway.oauth`'s token URL, scopes or
  `refresh_before_seconds`) replaces the profile: it stops the agent,
  deletes its sandbox and provider, imports the profile anew, and asks
  for `set-credential`. Before, it failed with "custom provider profile
  … already exists" after writing the new files.

## v0.9.5 — 2026-11-19

A change for agents in OpenShell sandboxes: the connection to the
model endpoint passes OpenShell's proxy uninspected, so long model calls
with large requests no longer end in `EOF` there.

Upgrading from 0.9.4 needs no change to configurations or gateway data.
An existing OpenShell agent gets the new sandbox policy when its
configuration is applied again:
`agent-admin edit NAME --from /etc/custos/agents/NAME/agent.yaml`
recreates its sandbox (its sessions go). Systemd agents are not
affected. With an `https://` model endpoint whose certificate a private
CA issued, an agent in a container needs that CA in the agent image, or
`sandbox.openshell_inspect_model: true`.

### Changed

- In an OpenShell sandbox, the connection to the agent's model endpoint
  passes OpenShell's proxy at the TCP level, uninspected, still for
  `agent-launcher` only. Before, the proxy parsed and relayed every model
  request and answer as HTTP, though the agent's rule allowed them all,
  and long model calls with large requests failed there with
  `model endpoint: Post ".../chat/completions": EOF`, where the same agent
  outside OpenShell worked. `sandbox.openshell_inspect_model: true` keeps
  the inspection, with OpenShell's record of each request. An existing
  agent gets the new policy with `agent-admin edit` (its sandbox is
  recreated). An `https://` model endpoint's certificate is now checked
  by the agent, in a container against the agent image's trust store: a
  model server with a private CA's certificate needs that CA in the
  image, or `openshell_inspect_model: true`.

## v0.9.4 — 2026-11-19

A fix for Cockpit's new-agent form: an OpenShell agent renamed there
gets the OAuth client of its own name, so `idp-client --keycloak --apply`
and its form make the client for it.

Upgrading from 0.9.3 needs no change to configurations or gateway data,
and no agent needs a restart: reload the page in the browser. An agent
created with the wrong client id before keeps it: change
`gateway.oauth.client_id` to `agent-NAME` with `agent-admin edit`, then
make its client.

### Fixed

- Cockpit's new-agent form names an OpenShell agent's OAuth client after
  the agent's name as typed (`client_id: agent-NAME`). Before, a
  configuration drafted from a template kept the client id of the
  template's name after the agent was renamed, and `idp-client --keycloak
  --apply` (and its form) then refused the agent: "gateway.oauth.client_id
  is agent-health-check: --apply makes and replaces only agent-NAME". A
  client id written by hand stays as it is.

## v0.9.3 — 2026-11-19

A fix for Cockpit's page: it stays quick while an agent does not
answer, for example one whose service fails to start again and again.

Upgrading from 0.9.2 needs no change to configurations or gateway data,
and no agent needs a restart: reload the page in the browser.

### Fixed

- Cockpit stays quick while an agent does not answer, for example one
  whose service fails to start again and again while systemd holds its
  socket. Such an agent is shown as not answering, with the time it is
  asked again, after 30 seconds and then at growing intervals up to
  5 minutes; Start and Restart ask it again at once. Before, every
  refresh of the Agents and Activity views waited 8 seconds for it.

## v0.9.2 — 2026-11-19

A fix for agents whose model is an OpenAI-compatible endpoint served by
a local server with its own chat template (`provider: openai`): a
model's message that only calls tools now has the content `""`, so a
template that reads the content of every message, as Gemma's in
LM Studio does, no longer fails the request.

Upgrading from 0.9.1 needs no change to configurations or gateway data.
An agent uses the fixed client from its next start: on the host and on
OpenShell's systemd compute driver, restart it, or let its next activity
start it. OpenShell agents in containers: rebuild the agent image, then
set each agent's gateway credential again to recreate its sandbox
(guide, chapter 11). A session whose conversation failed this way
continues once the agent runs the fixed client.

### Fixed

- An agent's requests to an OpenAI-compatible endpoint (`provider:
  openai`) give a model's message with tool calls only the content `""`,
  where they left the field out. The API allows both, but the chat
  templates of local servers read it from every message: with Gemma in
  LM Studio, a conversation with such a message failed with `Error
  rendering prompt with jinja template: "Cannot perform operation in on
  undefined values"`.

## v0.9.1 — 2026-11-19

A fix for agents whose model is an OpenAI-compatible endpoint
(`provider: openai`): a model call no longer fails in the middle of a
run with `EOF` when a server or proxy closed the connection the
previous call had left open. This was reported with an OpenShell agent
and LiteLLM in front of the model.

Upgrading from 0.9.0 needs no change to configurations or gateway data.
An agent uses the fixed client from its next start: on the host and on
OpenShell's systemd compute driver, restart it, or let its next activity
start it. OpenShell agents in containers: rebuild the agent image, then
set each agent's gateway credential again to recreate its sandbox
(guide, chapter 11).

### Fixed

- An agent's model calls to an OpenAI-compatible endpoint
  (`provider: openai`) no longer fail in the middle of a run with
  `model endpoint: Post ".../chat/completions": EOF`. Each call now goes
  on a new connection. Before, a call reused the previous call's
  connection, which the server or a proxy between could have closed
  meanwhile: LiteLLM, for one, closes a connection idle for 5 seconds,
  and a tool call between two model calls often takes longer. It
  showed most in OpenShell sandboxes, whose proxy keeps the agent's side
  of such a connection open.

## v0.9.0 — 2026-11-19

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
- On a transactional system, use the systemd compute driver 0.2.0 or
  later: it ships the mount points that 0.1.4 had to make on the
  read-only root.
- Known and accepted, as in 0.8.0: five vulnerabilities in the HTTP/2
  of `golang.org/x/net` v0.58.0 (GO-2026-6603, -6610, -6611, -6612,
  -6617), whose fix needs Go 1.26. The agent serves no HTTP/2
  (security review 0.7, R2).

### Added

- Cockpit makes an OpenShell agent's OAuth client in Keycloak and sets
  its secret, in the agent's Credentials section: a Keycloak user that
  may manage the realm's clients, its password, and **Make the client
  and set the credential** (`agent-admin idp-client NAME --keycloak
  --apply`). The page keeps neither the password nor the secret.
- `agent-admin idp-client NAME --keycloak --apply --admin USER` sets up
  an OpenShell agent's OAuth client in one command: it logs in to
  Keycloak's admin API (the password asked for, or on standard input),
  makes the client, lists the issuer for agents, checks a token and sets
  the agent's `gateway-client-secret`. No `kcadm.sh`, no container
  command and no secret in a file. Before, this took a `kcadm.sh` script
  and four more commands. The password goes over HTTPS only, with no
  redirect followed; only the agent's own client `agent-NAME` is made or
  replaced; an issuer it listed is taken off again when the token check
  fails (security review 0.9).
- Every shipped agent template can run in an OpenShell sandbox:
  `patch-pilot-transactional`, `disk-watch`, `disk-watch-transactional`,
  `health-check`, `service-doctor` and `exposure-watch` as well as
  `patch-pilot` and `triage`. On a transactional host, Cockpit's
  new-agent form now offers OpenShell for its templates. Before, only
  `patch-pilot` and `triage` allowed it. Each template's
  `needs_openshell` names the limit of 13 characters for the agent's
  name.

### Changed

- The guide's chapter 11 gives the systemd compute driver's setup as
  commands, in order: OpenShell for its user, its gateway running and
  registered (with OpenShell's package: `systemctl --user … enable
  --now openshell-gateway` and `openshell gateway add`), the driver's
  packages, a reboot on a transactional system, then
  `agent-admin openshell-setup --driver systemd`. It says which
  certificate `--ca` takes and when it is not needed, and that with the
  systemd driver that CA is trusted host-wide.
- The guide's chapter 11 says how to run the Keycloak client script
  against a Keycloak in a container:
  `KCADM="podman exec -i keycloak /opt/keycloak/bin/kcadm.sh"`, with
  `kcadm.sh` logged in inside the container.

### Fixed

- `agent-admin check`, `create`, `draft` and Cockpit's new-agent form
  refuse an OpenShell agent's name longer than 13 characters: its
  sandbox, `agent-<name>`, would exceed OpenShell's 19 characters for a
  sandbox's name. Before, the name was taken, and setting the agent's
  gateway credential failed after creating its provider
  (`name exceeds maximum length (22 > 19)`).
- `agent-admin openshell-setup --driver systemd` checks that the
  driver's helper runs after starting it (its socket,
  `/run/openshell-systemd-helper/helper.sock`), and otherwise stops and
  names the helper's journal. Before, it went on, and failed waiting for
  the driver's socket, pointing at the driver's journal, which only said
  that there was no helper.

### Security

- The relay of an OpenShell agent reads root's OpenShell registration
  (`/root/.config/openshell`) by a type of its own,
  `agent_openshell_home_t`, where its SELinux domain could read every
  user's `~/.config` (`config_home_t`; security review 0.8, S5). The
  package and `agent-admin openshell-setup` restore the label; after
  copying the registration by hand, run
  `restorecon -R /root/.config/openshell`.

## v0.8.0 — 2026-11-18

OpenShell agents on OpenShell's systemd compute driver, and the
OpenShell side set up by `agent-admin`. An agent in an OpenShell
sandbox can now run on the OpenShell systemd compute driver as well as
on Docker or Podman (`sandbox.openshell_driver: systemd`). Its sandbox
is then systemd units on the host, with no image to build: the
workload runs the host's `agent-launcher` as a user of its own, and
the relay reads the agent's records from the journal. `agent-admin openshell-setup`
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

### Added

- `agent-admin check`, `create` and `edit` note an OpenShell agent's
  `gateway.oauth.refresh_before_seconds` below 90: OpenShell's gateway
  renews tokens once a minute, so such a token can expire before it is
  renewed, and a sandbox started then has no token. The guide's example
  uses 120, where it used 60.
- `agent-admin openshell-setup --user USER --driver container|systemd
  [--ca FILE] [--check]` sets up OpenShell on the host for OpenShell
  agents, for either compute driver. It installs nothing: it checks
  that OpenShell, Podman or Docker, or the systemd compute driver are
  installed, and names what is missing. It then configures the driver,
  registers OpenShell's gateway for root as root's own files, and puts
  mcp-gateway's CA where the driver needs it. Before, these were steps
  by hand in chapter 11.
- OpenShell agents can run on OpenShell's systemd compute driver:
  `sandbox.openshell_driver: systemd` in `agent.yaml` creates the
  sandbox without an image (`--from host`), and the relay reads the
  agent's records from the journal of the sandbox's workload unit.
  The default, `container`, is Docker or Podman, as before.
- Root's copy of the OpenShell gateway's registration
  (`/root/.config/openshell`) must be root's own files: the guide's
  `cp -a` is followed by `chown -R root:root`. The relay reads it under
  SELinux to follow the systemd driver's journal.
- The guide's chapter 11 compares the two ways OpenShell runs an
  agent's sandbox, containers or the OpenShell systemd compute driver,
  says when to choose which, and what changes in each setup step with
  the systemd driver (no image, the CA in the host's trust store, the
  agent's records from the workload unit's journal). Chapter 2 lists the
  driver among the requirements.
- An agent created from a template can run in an OpenShell sandbox:
  `agent-admin create NAME --template T --sandbox openshell
  [--openshell-driver container|systemd]` (and `draft`) add
  `gateway.url`, the agent's OAuth client and the `sandbox` line from
  mcp-gateway's HTTPS settings. A template says where it may run
  (`backends` in `template.yaml`); `patch-pilot` and `triage` allow
  OpenShell. Cockpit's new-agent form offers the choice. Before, every
  agent from a template ran as a systemd service.

### Changed

- The OpenShell backend needs OpenShell 0.1.8 or later, where it needed
  0.1.7: the tests run with 0.1.8, and 0.1.7 is no longer tried.

### Fixed

- An agent in an OpenShell container sandbox can run the shipped
  diagnostic playbooks: the agent image has them in
  `/usr/share/custos/playbooks`. Before, it answered `no playbook "…" on
  this host` for every one. Rebuild the image with the playbooks in its
  build context (chapter 11, step 2), then set each agent's gateway
  credential again, which recreates its sandbox from the new image.
- Stopping an agent in an OpenShell sandbox on rootless Podman no longer
  logs an SELinux denial (`kill` in `cap_userns` for `agent_relay_t`):
  the relay leaves its log command (`podman logs`, run in the OpenShell
  user's user namespace) to the unit's stop, which ends it with the
  unit's other processes. Before, the relay tried to signal it itself
  and was refused.
- With OpenShell's systemd compute driver, the relay's `journalctl` may
  read the runtime journal (`/run/log/journal`) and look at the journal's
  file systems. SELinux refused both before, without a message from
  `journalctl`: on a host whose journal is not (yet) persistent, the
  agent's records did not reach the host's journal.

## v0.7.0 — 2026-11-16

The code reworked so that several people can work on it, with nothing
new for an administrator to learn. `internal/admin` is split into
packages by topic (the identity provider, the templates, promotion and
readiness, the setup), `agent-admin`'s commands are a table with a file
per group, and the largest files of the agent's side are split by
topic. `docs/architecture.md` says which package owns what and what it
may import, and a test (`test/boundaries`) holds the code to it. Where
a maintained Go module does what Custos had written itself, the module
is used: go-systemd for journald's protocol, `x/oauth2` and golang-jwt
for `agent-admin check-token`, go-udiff for `agent-admin compare`, and
the OpenAI Go SDK for the model client. The security review before
0.7 (`docs/security-review-0.7.md`) went through what moved and what
the modules replaced, and found nothing above a note in them. CI now
checks the build's modules against the Go vulnerability database.

Upgrading from 0.6.x needs no change to configurations or gateway
data, and adds nothing to the contract; mcp-gateway 1.16 stays the
minimum. To know:

- A script that passes `agent-admin` an option the command does not
  have fails now, where the option was ignored before.
- The model evaluation gives the picture of 0.6.0 (chapter 5) with the
  OpenAI Go SDK: Qwen3 8B wrote 3 of 4 reports, and 2 of 4 on the same
  day without the SDK, which four runs do not show to be a difference.
  No tested model followed a prompt injection.
- Known and accepted for this release: five vulnerabilities in the
  HTTP/2 of `golang.org/x/net` v0.58.0 (GO-2026-6603, -6610, -6611,
  -6612, -6617), which 0.6 has as well. Their fix needs Go 1.26;
  Custos builds with the Go the distribution ships. The agent serves
  no HTTP/2 (security review 0.7, R2).

### Changed

- The agents' records reach journald through
  `github.com/coreos/go-systemd/v22/journal` instead of Custos's own
  writer of journald's protocol. The records and their fields are
  unchanged. A record too large for a datagram now reaches the journal
  as a record where the writer may create a file in `/dev/shm` to pass
  it (`agent-admin`'s records); an agent under SELinux may not, and its
  record is kept as a JSON line on standard error, as before.
- `agent-admin` refuses an option a command does not have (`agent-admin
  delete NAME --keep-sate`, say), where it was ignored before. Its
  commands are a table with the standard library's `flag` per command;
  options and arguments are read in any order, as before, and
  everything after `--` is an argument (security review 0.7, R3).
- `custos-tests`: the functional tests' checks are in one file per topic,
  `/usr/libexec/custos/tests/sections/`, with the framework in `lib.sh`
  next to `run-tests.sh`. The command, its options, the checks and their
  order are unchanged.
- The client for OpenAI-compatible model servers builds its requests and
  reads the answers with the OpenAI Go SDK
  (`github.com/openai/openai-go/v3`, pinned to v3.50.0, the last that
  builds with Go 1.25). What the server receives differs in details that
  do not change a request's meaning: `"stream": false` is no longer sent,
  an assistant message with tool calls only has no `content` key instead
  of `"content": null`, and the request names the SDK (`User-Agent:
  OpenAI/Go 3.50.0`, `Accept: application/json`). The tools' schemas are
  sent as before, their properties in the order they were written. The
  SDK's retries and its `X-Stainless-*` headers are switched off, and
  its environment variables (`OPENAI_API_KEY`, `OPENAI_BASE_URL`) are
  not read. `agent-launcher` grows by about 11 MB.
- `agent-admin check-token` requests its token with
  `golang.org/x/oauth2` and reads the claims with
  `github.com/golang-jwt/jwt/v5`. An issuer that answers the token
  request with JSON labelled `text/plain` is refused now; RFC 6749 asks
  for `application/json`, which Keycloak sends.
- `agent-admin compare` computes its differences with
  `github.com/aymanbagabas/go-udiff`; what it prints is unchanged.

## v0.6.2 — 2026-11-10

A security fix for the OpenShell relay, which also fixes 0.6.1's build
on aarch64 for SLE 16. On older kernels, SLE 16's among them, a local
user's listener on the port forward's port could pass the relay's
owner check as root's and receive the agent's secret (security review
0.6, P10).

Upgrading from 0.6.1 needs no change to configurations or gateway data,
and adds nothing to the contract. Restart the OpenShell agents' relays
for the fix to take effect (`systemctl try-restart
'agent-openshell@*.service'`); systemd agents are not affected.

### Fixed

- The OpenShell relay's check that it talks to root's port forward read
  the owner from the accepted connection's entry in the kernel's socket
  table. Older kernels, SLE 16's 6.12 among them, list that entry with
  uid 0 until the listener accepts the connection. A local user's
  listener on the forward's port that waits before accepting could so
  pass as root's and get the agent's secret; and the check failed for a
  listener that is not root's, which broke 0.6.1's build on aarch64 for
  SLE 16. The owner is now the listening socket's (security review 0.6,
  P10).

## v0.6.1 — 2026-11-10

Agents against their templates, and two fixes from running 0.6.0.
`agent-admin compare` shows how an agent differs from the template it
was created from, and `apply-template` takes the template's
configuration or permissions over, in Cockpit too. Health checks no
longer take a failed mcp-gateway server instance for a broken host
service, and `patch-pilot-transactional` looks for updates without an
approval.

Upgrading from 0.6.0 needs no change to configurations or gateway data.
Within contract version 1 this release adds `agent-admin compare` and
`apply-template` and the file `/etc/custos/agents/<name>/template`
(§14a, §17). Agents created before keep their configuration and
permissions: `agent-admin compare NAME --template TEMPLATE` shows what
the new templates change, and `apply-template` takes it over.

### Changed

- `patch-pilot-transactional` refreshes the repositories without an
  approval: `tukit__refresh_repositories` only fetches their metadata,
  so its permission is read-only, like `list_updates`, instead of
  `patch-errata`. Looking for updates, also for an agent that delegates
  it, no longer waits for an approval. An agent created before keeps
  its permissions; change it with `agent-admin permissions NAME`.

### Added

- `agent-admin compare` and `apply-template` (CONTRACT.md §14a, §17): an
  agent against its template, and the template's configuration or
  permissions taken over, in Cockpit too (the agent's **Template**
  section). `compare` shows a line diff of the configurations and the
  permissions only the agent or only the template has. Applying keeps the
  agent's name, model and credentials, its schedules, events, sessions,
  sandbox and gateway connection, and its mode, promoted categories and
  owners. `create --template` now records the template in
  `/etc/custos/agents/<name>/template`; for an agent created before, name
  the template with `--template`.

### Fixed

- The `failed-units` playbook and the `health-check` and `triage`
  templates tell the model that a unit named
  `mcp-<server>-<hash>.service` is one of mcp-gateway's transient server
  instances, started for one user or agent: its failure is the gateway
  server's (for example podman's for an agent without a Podman socket),
  it has no unit file and is never restarted. Models had reported such
  a failure as the host's and proposed restarting it. An agent created
  before keeps its instruction; the playbook applies to every agent.

## v0.6.0 — 2026-11-09

Diagnosis with small models, and a seal that covers each agent. The
host now does more of a playbook run itself: it runs the steps with
their arguments, shows the model only the playbook's tools, bounds its
output, can turn its thinking off, asks once for a report that was
answered in prose, and tells it that what a tool returns is data, never
instructions. With these, no tested model followed a prompt
injection, and Qwen3 8B on four CPUs found half of the planted faults
(chapter 5).
The seal now covers each agent's gateway data and the token issuers;
the security review before 0.6 (`docs/security-review-0.6.md`) found
how hand-edited data could still become trusted, and how the OpenShell
relay's owner check could be passed, and each is fixed. `update-status`
sees a pending reboot and the services to restart (mcp-gateway 1.16),
and Cockpit promotes a task category with a form instead of YAML.

Upgrading from 0.5.x needs no change to configurations: contract
version 1 only gained names. To know:

- mcp-gateway 1.16 is the minimum: its exec commands see
  `/run/reboot-needed`.
- A host sealed before runs `agent-admin seal --agents` once, so that
  the seal covers the agents' data. Add token issuers with
  `agent-admin issuers` from now on, not by editing `_http.yaml`: an
  edited file is no longer trusted.
- The services to restart need mcp-gateway's server `restarts`, which
  holds `CAP_SYS_PTRACE`: install `mcp-gateway-restarts-server` and run
  `custos-setup --restarts`, or link its profile yourself (chapter 9).
- Every agent's instruction ends with the host's paragraph on tool
  outputs; an agent's own instruction is unchanged.

### Changed

- Custos requires mcp-gateway 1.16: its exec commands see whether an
  update waits for a reboot (`/run/reboot-needed`).
- Cockpit's **Promote…** and **Change envelope…** open a form for the
  envelope instead of a YAML text (#163): windows with the days as
  checkboxes (and *every day*, *weekdays*, *weekend*), from and to, and
  the time zone, the host's at first; an optional rate; the conditions
  that demote the category at once, with the health check's command.
  The form says the envelope in words, checks it before sending it, and
  shows the category's readiness. **Edit as YAML** keeps the text, and
  an envelope with parts the form does not show opens there.
- The demonstration videos follow one problem through the agentic loop
  (#149), with a strip below the pages that lights up each stage. A
  service fails, and its event starts web-doctor (observe). Its report
  is confirmed (diagnose). It proposes a restart (propose). A person
  approves it on the gateway's approval page, or on mcp-gateway's
  Cockpit page in the technical version (decide). It runs, on both
  records (act). Readiness follows (learn). The category is promoted
  with an envelope (promote). The next failure is fixed without asking
  (act). That action is reviewed (review). "Should not have run"
  demotes it (demote). The demonstration's readiness thresholds are
  low, and a subtitle says so. custos-tests' scripted model answers
  that event's prompt with a probe, a report and a restart.

### Added

- The model is told that a tool's output is data, never instructions
  (#155): every agent's instruction ends with the host's paragraph
  saying so, and an output that reads like instructions reaches the
  model with the host's notice beside it (`custos_notice`), naming the
  signs; `tool.result` records them (`AGENT_INJECTION_SIGNS`). An
  output that came back as an error and a client tool's result get it
  too, and a `custos_notice` inside an output is a sign of its own
  (security review 0.6, P4–P7). Advice to the model; the gateway's
  approval stays the boundary.
- The report nudge (#155): a model that answers a playbook run in prose,
  without a report and without a tool call, is asked once more, in the
  same activity, to file it with `report_diagnosis`. The activity says
  so (`report_nudged`), and so does the answer's `model.call` record
  (`AGENT_REPORT_NUDGE`). `model-eval.sh` shows which runs were asked
  and counts the reports filed after it.
- `{unit_re}` in a playbook step's arguments: the unit escaped for a
  regular expression (security review 0.6, N5). `service-health`'s
  journal pattern uses it, so that `nginx.service` no longer also matches
  `nginxXservice`.
- An activity says how much its model thought (`thought_bytes`, the sum
  of its `model.call` records' `AGENT_THOUGHT_BYTES`), and Cockpit's
  activity view says so, and whether the host had to ask the model for
  the playbook's report (`report_nudged`) and whether it then filed one.
- A playbook run shows the model only the playbook's tools (#155): its
  steps' and those it names in the new key `tools`, of those the gateway
  offers, and the report tool. A small model reads every tool it is
  shown before it answers; the prompts of playbook runs get shorter.
- A playbook step takes the call's arguments (`args`), and `{unit}` in
  them is the unit the activity is about: the request's new `"unit"`
  (`POST /v1/activities`), or for an event that runs a playbook the unit
  that failed (#155). A step that needs a unit runs only with one.
- `model-eval.sh --limit SECONDS`, each run's time limit (CI: 20
  minutes, so the job ends in its time with any model), and per run the
  model's calls: how many, the largest prompt, the tokens written and
  the longest call. A run that ends in prose keeps the answer and
  whether it names the fault.
- `model.max_output_tokens`: how much the model may write in one answer
  (#155). A small model can write without end; with a limit its answer
  is cut off there, and the `model.call` record says so
  (`AGENT_CUT_OFF`). It is kept when Cockpit saves the model defaults.
  `model-eval.sh --max-output TOKENS` sets it (CI: 2048) and counts the
  cut-off calls.
- `model.reasoning_effort` (provider `openai`): how much a model that
  thinks before it answers should think, sent as `reasoning_effort`
  (#155). Ollama thinks by default with a model that can, whatever the
  prompt says; `none` turns that off. The thinking a server returns
  apart from the answer (`reasoning`, `reasoning_content`) is no longer
  dropped unseen: it stays out of the answer, and the `model.call`
  record counts it (`AGENT_THOUGHT_BYTES`). `model-eval.sh --no-think`
  sets `none` and counts the thinking per run.
- The seal covers each agent's gateway data and the agents' token
  issuers too (#158, the rest of security review M5). `agent-admin`
  signs them as it writes them; the gateway denies an agent whose data
  was changed by hand everything (its owners approve nothing), and
  trusts no issuer from an `_http.yaml` changed by hand. The emergency
  stop is never sealed. `agent-admin show` and Cockpit report a broken
  seal and an earlier signed copy put back (journal: `gateway.data`). A
  new seal covers the agents' data from the start; a host sealed before
  runs `agent-admin seal --agents` once. `agent-admin issuers` writes
  `_http.yaml`, which was edited by hand before.
- `model-eval.sh` (custos-tests) measures how well a model does Custos's
  work on a test system (#150): the bundled diagnostics install with the
  model, the health-check agent's playbooks against planted faults
  (report filed, quotes verified, fault found, plan safe, duration), and
  the prompt-injection cases. CI's job `models` runs it for each release
  and on demand with the models of `test/vm/eval-models` (Qwen3 1.7B to
  8B, on Ollama, CPU only) and tabulates them. User guide, chapter 5,
  "Which model is good enough".
- Demonstration videos of mcp-gateway and Custos, without audio and with
  English subtitles, in a business and a technical version: CI's job
  `video` records a scripted tour (`test/video/tour.js`) of the real
  pages in the Leap 16 test VM: Custos's and mcp-gateway's Cockpit
  pages, mcp-gateway's own approval page with a sign-in at the identity
  provider, and Cockpit's terminal. Each version comes as an MP4 with
  the subtitles burned in, an MP4 with a subtitle track, and `.srt` and
  `.vtt` files, in the job's artifact `demo-videos` only. The job runs
  for each release, when its tag is pushed (CI then builds the packages
  and records the videos, nothing else), and by hand (Actions, CI, Run
  workflow). custos-tests' `testidp` gained
  the authorization code flow with PKCE (`--login`, `--issuer`), for
  the approval page's sign-in.

### Fixed

- A tool's error output reached the journal (`AGENT_REASON` on
  `tool.result`) and the activity record (`calls[].reason`) verbatim,
  for example the whole stdout and stderr of an exec command that
  exited non-zero. Both now keep the gateway's reason (`mcp-gateway: …`)
  or a fixed text with the output's size, never the output (security
  review 0.6, P4a).
- The OpenShell relay's check that it talks to root's port forward read
  the first entry of the kernel's socket table with the connection's
  addresses, which can be an earlier connection's TIME_WAIT entry,
  listed with uid 0. A local user's listener on the forward's port could
  so pass as root's and get the agent's secret; and the check failed
  where the listener is not root's, which broke 0.5.0's build on aarch64
  for SLE 16.0 and 16.1. Only a connected entry counts now (security
  review 0.6, P9).
- Security review 0.6 (`docs/security-review-0.6.md`) of the seal of
  the agents' data: any `agent-admin` write, and `seal --agents`, signed
  an agent's data edited by hand as it was, which the gateway then
  counted and `show` no longer reported (N1); a missing key made
  `enable` or `disable` sign everything on disk with a new one (N2); a
  pause or a demotion failed when the key could not be used (N3).
  `agent-admin` now builds only on data that verifies: other changes are
  refused, a pause, a demotion or a lower mode is written unsigned; `seal
  --agents --accept NAME` signs a file as it is after a person checked it;
  a missing key is an error, and `seal --rekey` makes a new one, signing
  again only what verifies or is accepted. Each signature names its
  record, time and host (N4), and `show` reports an earlier record and
  another host's. The evaluator runs no health check from data that does
  not verify. `idp-client` and `check-token` name `agent-admin issuers`,
  and say when `_http.yaml` does not match its seal.
- `update-status` could not see a reboot or restart that an update waits
  for: its only required step, `systemd__check_restart_reload`, reports
  only on restarts systemd-mcp started itself. Its new step
  `exec__reboot_status` (a read-only diagnostic command) says a reboot
  is pending when a kernel newer than the running one is installed
  (from rpm) or `/run/reboot-needed` exists (zypper and
  transactional-update write it after an update that needs a reboot),
  and lists the running and the installed kernels; the exec server's
  commands see that file from mcp-gateway 1.16 on. Its new optional step
  `restarts__pending_restarts`, mcp-gateway's server `restarts` (1.16,
  package `mcp-gateway-restarts-server`), lists the services and
  processes that still use files an update replaced, as `zypper ps`
  does. health-check may call both, and no longer has
  `check_restart_reload`. `custos-setup --restarts` links the server's
  profile into `/etc/mcp-gateway/servers.d/` (sdrahn/mcp-gateway#345);
  without the option it offers no server that holds a capability
  (security review 0.6, P1). Where the exec server's commands cannot
  see `/run/reboot-needed`, `reboot_status` says so rather than that no
  reboot is pending (P3).
- `custos-setup` kept the diagnostic commands of 0.4.0 and 0.5.0 as an
  administrator's file, since it knew only 0.3.0's as shipped; it now
  replaces those too, unchanged, with the current ones.
- The shipped playbooks showed the model the wrong things (#155): their
  steps were called without arguments, so `failed-units` listed the
  *active* units (systemd-mcp's default) and never the failed one, and
  the journal steps read the last 100 lines of the whole journal, mostly
  Custos's and the gateway's own. Their steps now ask for what they look
  for: the failed units with their exit status, the journal's lines
  about failures, the time services by name, the full disk's and the OOM
  killer's messages, the denials. `service-health` reads the named
  unit's state, its own journal and systemd's account of its restarts;
  it no longer calls `check_restart_reload`, which only reports on a
  restart the server itself started.
- A field being typed in on the Agents tab (the promote form's envelope,
  its reason, a kept editor) lost the keyboard focus at each refresh,
  every five seconds: what was typed next went nowhere. The refresh now
  gives the focus back, with the caret where it was.
- Cockpit's form to promote a task category closed within five seconds
  and lost the envelope being written: the Agents tab rebuilt the
  agent's view, form included, at each refresh. So did the reason that
  Demote and "Should not have run" record. The form and the reason are
  now kept while the form is open or anything was typed, as the
  permissions editor is, and start afresh once an action used them.

## v0.5.0 — 2026-11-07

A host whose trust holds against its own agents. The full security
review (`docs/security-review-0.5.md`, #125) found that a compromised
agent could pass for root in the journal, reach root's relay secret,
count as an approver, reach any host, or hide its declined calls from
the override rate; each of these is fixed, and the stop works without
the gateway's policy. The task categories' state is sealed, and there
is a way back from enabling them. Agents can start on a unit's failure,
read the product documentation through the gateway, and, in OpenShell,
get their tokens from the site's identity provider and reach the
gateway by the host's own name. A second agent host, built from the
contract alone, keeps the contract honest.

Upgrading from 0.4.x needs no change to configurations: contract
version 1 only gained names. To know:

- mcp-gateway 1.15 is still the minimum. Update `custos-selinux` in
  the same transaction as `custos`: it has the relay's new rule.
- Check the gateway's approver rules
  (`/etc/mcp-gateway/policy/rbac/data.yaml`) for `group:agent-clients`
  and `group:mcp-users`, which earlier documentation recommended: every
  agent is in them. The owner rules no longer grant anything to an
  approver named `agent-*`; name a group of people instead (review M2).
- A systemd agent reaches only the host itself, its model endpoint and
  the nameservers from its next start (`systemctl restart
  agent@NAME.service`, or idle exit). An agent that needs more, such as a
  proxy, gets it with a drop-in (user guide, chapter 14).
- The task categories' state is sealed by the first `agent-admin
  enable` or `disable` with this version; until then it counts as it is.
- OpenShell agents: rebuild an image built before (it resolves names by
  DNS first), and recreate each sandbox (`agent-admin set-credential`)
  so the relay secret no longer reaches its command line. Two OpenShell
  agents whose names share a forward port: the second's unit now fails
  to start instead of reaching the first; recreate it under another
  name (`agent-admin create` names the port).
- For the documentation in the templates, install mcp-docs with its
  gateway definition (`mcp-docs-gateway`, the server `suse-docs`) and
  the documentation packages. Agents created before keep their
  configuration and permissions; take the template's again to use it.
- Running agents keep the old launcher until they next start.

### Added

- The agent templates read the product documentation through the
  gateway (#130): mcp-docs as the server `suse-docs`, with SUSE's
  documentation packages, Custos's and the others installed. Each
  template is shown and allowed its reading tools (`find_sections`,
  `search`, `outline`, `read_section`; in `diagnostics` for
  `health-check`), and its instruction says to check findings and
  proposed steps against the documentation and to name the sections
  used. Without the server, nothing changes. Agents created before keep
  their configuration and permissions.
- OpenShell agents reach mcp-gateway by the host's own name, so an
  official TLS certificate covers them (#131). The user guide (chapter
  11) recommends it; `host.openshell.internal` with a private CA remains
  for test systems. `agent-admin check`, `create` and `edit` note an
  OpenShell agent by that alias, a gateway name that is not in DNS
  (OpenShell resolves it by DNS, never `/etc/hosts`) or resolves to a
  loopback address (which OpenShell never connects to), and a `gateway.url` other than the gateway's `http.audience`;
  Cockpit's **Check** shows the notes. The agent image resolves names by
  DNS first (OpenShell's policy DNS): with Podman, a name in the host's
  `/etc/hosts` gave the agent an address OpenShell refused. Rebuild an
  image built before.
- The site's identity provider issues OpenShell agents' tokens (#111):
  `agent-admin idp-client NAME` says what the agent's OAuth client must
  be, from the gateway's own settings, and with `--keycloak` prints a
  `kcadm.sh` script that creates it (subject `agent-<name>`, the
  gateway's audience and scopes, 5-minute tokens, without the realm's
  default client scopes). `agent-admin check-token NAME < secret` gets a
  token with the client and checks each claim the gateway relies on,
  before `set-credential`. CI runs both against a real Keycloak.
- A second agent host for tests (#112): `test/vm/secondhost`, built from
  the contract alone, and conformance checks (`test/contract`) that it
  and the launcher pass; the functional and Cockpit tests use it as an
  agent of its own. In `custos-tests`.
- The way back from enabling task categories (#110): `agent-admin
  disable CATEGORY…` disables them again, and `disable --all` returns the
  host to diagnostics-only; Cockpit's **Disable…** does the same. It is
  refused, naming what is in the way, while an agent has a permission in
  the category or has it promoted, and recorded as `categories.disabled`.
- The task categories' state is sealed (#109): `agent-admin` signs it
  with a key kept as a systemd credential (the TPM's, where there is
  one), and the gateway counts it only with a valid signature. A state
  edited by hand, unsigned or deleted counts as diagnostics-only, and
  the denials say why; `agent-admin show` reports it (`sealed`,
  `tampered`). A host set up before is sealed by its next change.
- Events: an agent's `events:` start its activities when a systemd unit
  fails (`on_unit_failure`). `agent-admin` puts an `OnFailure=` drop-in
  on each unit, which starts `agent-event@<agent>_<event>_<unit>.service`;
  the activity has origin `event` (`origin.event`, `AGENT_ORIGIN_EVENT`),
  its prompt names the unit and systemd's account of the failure. Damped
  per unit: none while its activity runs, nor within
  `min_interval_seconds` (default 600); each skip is recorded
  (`event.skipped`). Cockpit shows an agent's events (#118).

### Changed

- A tool in `gateway.tools` whose server is not in `gateway.servers`:
  the check's message says the fix ("add agents to gateway.servers"),
  and Cockpit's create form and configuration editor offer a button that
  adds the server and checks again (#116).
- A delegation (`agents__delegate`) answers with the other agent's full
  response (up to 32 KiB), its diagnosis report if it wrote one, and why
  its activity did not finish, instead of the first 500 characters of
  its last text. The delegating agent can judge from it whether the task
  was done; the answer is still checked for signs of a prompt injection
  (#119).

### Fixed

- Security (review M6, #125): readiness and the evaluator counted a
  category's proposals from the agent's own `approval.pending` records,
  so a compromised agent that left one out for a declined call, or named
  another tool there, kept its override rate at 0 and was never demoted
  for overrides. The proposals are now the gateway's decisions, by the
  tool its `mcp-approval` record names; the agent's records add only
  what the gateway does not know (arguments, results, cancels).
- Security (review M5, #125): the seal covered less than CONTRACT.md
  §6b said. `agent-admin show` now reports a seal removed from a host
  `agent-admin` sealed, and an earlier signed state put back (it
  verifies; the journal has a later record); `agent-admin permissions`
  refuses a pattern for the server or tool of a permission in
  `diagnostics`, which would allow every tool on a diagnostics-only
  host. §6b says what the seal does not cover. Records of enablement
  changes carry `AGENT_SEALED`; a host sealed before reports a removed
  seal from its next change on.
- Security (review M4, #125): a systemd agent could connect to any host
  on an HTTP port (SELinux limited the ports, not the hosts), the cloud
  metadata service included, although CONTRACT.md §3a allows only the
  gateway and the model endpoint. `agent@.service` now denies every
  address but the host's own, and `agent-admin fence` adds the model
  endpoint's addresses and the nameservers at each start. An agent that
  needs more (a proxy) gets it with a drop-in (user guide, chapter 14).
- Security (review M2, #125): an agent could count as an approver.
  CONTRACT.md recommended `group:agent-clients` in the gateway's
  approver rules, a group every agent's user is in, and `agent-admin
  owners` accepted `group:agent-clients`, `group:mcp-users` and
  `remote-user:agent-*`. The owner rules (`owners.rego`) now grant
  nothing to an approver named `agent-*`; `agent-admin owners` refuses
  those rules; the documentation names a group of people instead. Check
  the gateway's approver rules (`/etc/mcp-gateway/policy/rbac/data.yaml`) for
  `agent-clients` and `mcp-users`.
- Security (review M1, #125): what an approver read in Cockpit could
  differ from what would run. Bidi overrides and isolates, zero-width
  characters and line separators in a call's arguments, the agent's
  rationale, a plan's command or a quote were shown as they are, so a
  model could make `echo cleanup; rm -rf /srv #` read as a harmless
  command; a plan's command of two lines showed as one. They are now
  shown as `\uXXXX` with a *hidden characters* badge, every line of a
  command on its own (with a badge for several), and Copy gives what is
  shown.
- Security (review M3, #125): `agent-admin stop-all` stopped nothing
  when the gateway's OPA did not load the stop within 20 s: it waited
  for the policy first and returned its error before stopping any unit.
  It now writes the flag, stops every unit and sandbox (and the event
  units), then waits for the policy and reports it; `pause` tells the
  agent's host before it writes the gateway's data.
- Security (review H2, H3, #125): an OpenShell agent's relay secret
  could reach a local user, who could then act as root on the agent's
  control API (start activities as anyone, read every response, pause).
  `agent-admin` put the secret, and the agent's configuration, on
  `openshell sandbox create`'s command line: they now go in with
  `--env-from`. The relay sent the secret to whatever listened on the
  forward's port, and a failed forward was ignored: the unit now stops
  an old forward and fails if its own does not start, and the relay
  sends nothing to a socket root does not own. `agent-admin create`
  refuses an OpenShell agent whose forward port another one has.
  Rebuild nothing; an OpenShell agent gets the change when its sandbox
  is next created (`agent-admin set-credential`, or an `edit` that
  reaches it).
- Security (review H1, #125): an OpenShell agent could write records
  that counted as root's. Its relay, which runs as root, passed every
  event from the sandbox to the journal, `agent-admin`'s too (reviews,
  feedback, permission changes, emergency stops), and copied any field
  name, so a name with a newline added fields of the agent's choosing.
  The relay now passes only the events an agent host writes, and a
  record never carries a field name outside `[A-Z0-9_]`; readiness and
  the evaluator take no record of the relay's as root's (journald's
  `_COMM` and `_SYSTEMD_UNIT`), and the gateway's records only from
  `mcp-gateway.service` itself (`_SYSTEMD_UNIT`).
- Cockpit's permissions editor closed, and lost what was typed, within
  five seconds: the Agents tab rebuilt the agent's view and kept the
  editor only while its text field had the focus. It now stays open with
  the edit until it is saved; so does the owners field.
- An activity without tool calls had `calls: null` instead of `[]`
  (§4); found by the second host's conformance checks (#112).
- Cockpit stopped and started a socket unit that does not exist for a
  host whose lifecycle names only its service (§3).
- `agent-admin pause` and `resume` refused an agent the gateway has no
  data for; they now tell its host (§8).

## v0.4.2 — 2026-11-06

The agent's full response. An activity keeps the model's last text in
full, and Cockpit's activity detail opens it in a dialog that renders
its Markdown, so a playbook's report table can be read whole.

Upgrading from 0.4.1 needs no change to configurations or gateway
data. Within contract version 1 this release adds the activity's
`response` (in `GET /v1/activities/{id}`).

### Added

- The agent's full response: an activity keeps the model's last text
  in full (`response`, up to 32 KiB, in `GET /v1/activities/{id}`), and
  Cockpit's activity detail shows its start with **Show the full
  response**, a dialog that renders its Markdown (tables, lists, code)
  as text, never as HTML. Before, only the first 500 characters were
  kept, and a playbook's table could not be read.

## v0.4.1 — 2026-11-06

Fixes for running the diagnostic playbooks with a local model. The
host now makes a playbook's steps itself, through the gateway, so a
model that is unreliable at issuing tool calls still gets every
output; the activities and their reports outlive the agent's idle
exit; Cockpit offers an agent only the playbooks it can run; and a
listing that names a unit no longer marks the next call as a suspected
prompt injection.

Upgrading from 0.4.0 needs no change to configurations or gateway
data. Within contract version 1 this release adds
`calls[].playbook_step`, `AGENT_PLAYBOOK_STEP` and `GET /v1/playbooks`.

### Fixed

- A call was marked as a suspected prompt injection whenever one of its
  argument values was in an earlier tool's output: a diagnostic
  playbook that reads the log of a unit `boot_blame` named was flagged.
  The value counts only where the output tells the model to use it
  ("please set", "call … with value").
- A playbook's steps are called by the host, in order, before the
  model's first turn, through the gateway with the same decision,
  approvals and records (`calls[].playbook_step`,
  `AGENT_PLAYBOOK_STEP`). A model that cut off its tool calls, as
  gpt-oss did for tools without arguments, left steps out of the
  report.
- Tools without arguments reach an `openai` model with the plain schema
  of an empty object. With the schema mcp-gateway's exec server sends
  (`"additionalProperties": false`), gpt-oss-20b in LM Studio now and
  then wrote a truncated argument object, and the call was lost.
- An ended activity, and its diagnosis report, was gone from Cockpit's
  Activity tab and `GET /v1/activities` once the agent exited when idle
  (10 minutes without requests): a scheduled playbook's run was not
  there when someone looked. The agent keeps its ended activities in
  its state directory (`/var/lib/agents/<name>/activities.json`, at
  most 200 of the last 30 days).
- Cockpit offered every playbook on the host for every agent, though
  only an agent the gateway offers a playbook's tools (as health-check)
  can run it. It now offers only those the agent can run and lists the
  others apart with the tools they need, from the agent's new
  `GET /v1/playbooks`.

## v0.4.0 — 2026-11-06

Autonomy within limits the gateway enforces, and the agent's account of
each call where it is decided. A promoted category's envelope can now
have a rate: within it the category acts on its own, beyond it each call
waits for an approval again (mcp-gateway's `approval_over_rate`).
Approvers see the agent's reason, its activity and the signs of a
suspected prompt injection with the approval itself, on the gateway's
page, in mails and in notifications. Readiness judges each call by the
permissions in force when it was made and counts an emergency stop as an
incident. An agent's owners see and manage its sessions in the gateway.
Model calls have a timeout of their own and are all recorded, failed
ones included. The bundled diagnostics work under SELinux, and the
network diagnostics see the host's network through Custos's own server
`custos-net`.

Upgrading from 0.3.x needs no change to configurations or gateway data:
contract version 1 only gained names. To know:

- Upgrade mcp-gateway to 1.15 first: `custos` requires it (the agent's
  proposal with each call, `approval_over_rate`, `network_view`). Update
  `custos-selinux` in the same transaction: it has the rules for the
  diagnostic commands.
- On a host set up with 0.3.0 (`custos-setup --diagnostics-only`), run
  `custos-setup` again with the same options: it replaces 0.3.0's exec
  commands (unchanged copies only), installs the server `custos-net`,
  and restarts the exec server's instances and the agent. Give the
  `health-check` agent the template's new permissions for `custos-net`:
  `agent-admin permissions health-check <
  /usr/share/custos/templates/health-check/permissions.yaml`.
- A model call is no longer cut off after 10 minutes: without
  `model.timeout_seconds`, `limits.max_invocation_seconds` bounds it with
  the rest of the activity.
- The records readiness now reads (`permissions.changed`,
  `agents.stopped`) start with this version: calls made before the first
  `permissions.changed` count by the permissions it changed from, or the
  current ones, as before.
- Running agents keep the old launcher until they next start (idle exit,
  or `systemctl restart agent@NAME.service`); OpenShell agents need their
  image rebuilt (user guide, chapter 11).

### Added

- Readiness judges each call by the agent's permissions at the call's
  time, and counts an emergency stop during an approved call's activity
  as an incident (O11). `agent-admin` records each change of an agent's
  permissions (`permissions.changed`, with them before and after) and
  the emergency stop and its release (`agents.stopped`,
  `agents.released`), as root; readiness, review and the demotion
  evaluator read them, an agent's own records of them never.
- An envelope's `rate` (`{calls: 20, per: 1h}`; `per` is `1s`, `1m` or
  `1h`) is enforced (O12): mcp-gateway's `approval_over_rate` obligation
  (its requirement R6, 1.15) on each of a promoted category's permissions
  while they act without approval, per tool. A call over it waits for an
  approval, for that call only, naming the rate reached; reaching it
  three times within a day demotes the category (`agent-admin
  evaluate`). Before, `promote` refused an envelope with a rate. Cockpit
  shows the rate with the category's windows.
- `model.timeout_seconds`: how long one model call may take. Without it,
  a call has no limit of its own and `limits.max_invocation_seconds`
  bounds it with the rest of the activity; a call that runs out says
  which limit it was. Also in the model defaults (`agent-admin defaults`).
- An agent's owners see it in mcp-gateway's *Clients* tab and with
  `mcp-gateway-admin clients`, and may end its session or cancel its
  tasks (`owners.rego`: `manage_session`, `manage_task`). Before, the
  tab showed an agent only to an approver of every server the gateway
  has, as an agent's session spans the endpoint for all servers.
- The agent's account of each call reaches mcp-gateway with the call
  (its proposal, requirement R5; mcp-gateway 1.15): the model's reason,
  the activity and who started it, the signs of a suspected prompt
  injection. The gateway shows it with the approval on its own page, in
  `mcp-gateway-admin approvals`, mails and desktop notifications, marked
  as the agent's claim (CONTRACT.md §7, O6).
- Requirements for mcp-gateway from Custos 0.4
  (`docs/requirements/mcp-gateway-for-custos-0.4.md`): R5, a proposal
  the client sends with a call (summary, impact, steps, context,
  warnings), shown with the approval on the gateway's page, in mails and
  notifications, marked as the client's claim (CONTRACT.md O6).

### Changed

- `custos` and `custos-tests` require mcp-gateway 1.15: an older gateway
  ignores `approval_over_rate`, so an envelope's rate would not be
  enforced.

### Fixed

- The network diagnostics (`network_addresses`, `network_routes`,
  `listening_sockets`; the playbook `network-config`) saw nothing of the
  host since 0.3.0: mcp-gateway's exec server runs its instances in a
  network namespace of their own, where `ip` cannot open a netlink
  socket and `ss` finds no sockets. They are Custos's server
  `custos-net` now (`/usr/share/custos/gateway/`), whose instances see the
  host's network without using it (`network_view: host`, mcp-gateway
  1.15, requirement R7); the template `health-check` and the playbook use
  its tools, `custos-setup` installs it, and Custos's SELinux module
  lets `mcpsrv_exec_t` read the network through netlink and run `ip`.
  A `health-check` agent made by 0.3.0 keeps its permissions: give it the
  template's `custos-net` ones (`agent-admin permissions health-check <
  /usr/share/custos/templates/health-check/permissions.yaml`).
- `custos-setup` replaces an unchanged copy of an earlier release's exec
  commands (0.3.0's), which it kept as the administrator's before; so the
  fixed commands reach a host set up with 0.3.0 when it runs again.

- The diagnostic commands `boot_time`, `boot_blame` and `pressure_stall`
  (`exec.d-diagnostics.yaml`) failed under SELinux with a tool error
  (0.3.0): mcp-gateway's exec server domain, `mcpsrv_exec_t`, may not
  ask systemd for the boot's times or read `/proc/pressure`. Custos's
  SELinux module now allows these, for the commands it ships.
- Diagnostic commands that failed on any host (0.3.0): `disk_usage` (its
  `--output=` list was split at the commas, unquoted in a YAML list) and
  `package_dependencies` (rpm could not open its working directory, `/`;
  it runs in `/usr` now, and runs no package's `%verify` script). `pressure_stall` says that pressure stall
  information is off in the kernel (SUSE's default; boot with `psi=1`)
  instead of failing.
- `custos-setup` installed the exec commands without telling the exec
  server, which reads them when an instance starts: an instance running
  already offered the health-check agent none of them. It now stops the
  exec server's running instances (through mcp-gateway's control API)
  after adding commands, does so before creating the agent, and restarts
  an existing agent when it added one.
- A call to an `openai` model was cut off after 10 minutes, whatever the
  activity's limit, which a slow local model with a growing prompt
  reached ("Client.Timeout exceeded while awaiting headers").
- `model.call` records had no `AGENT_DURATION_MS`, and a model call that
  failed left no record. It now has one with `AGENT_OUTCOME=error`, the
  error, how long it took and the size sent (`AGENT_PROMPT_BYTES`).
- Cockpit: in an agent's chat, the messages jumped back to their start
  every few seconds, when the agent view refreshed, which hid the latest
  messages and the input (0.3.0). The view now keeps the scroll position
  of the chat's messages and of the page when it refreshes.

## v0.3.0 — 2026-11-06

The bundled health-check and diagnostics agent, and trust that does not
rest on the agent. A host can be installed diagnostics-only
(`custos-diagnostics`, `custos-setup`): one read-only agent that runs
published diagnostic playbooks and reports hypotheses with evidence the
host checked against the tools' output, while every other task
category is installed but disabled until an administrator enables it,
which the gateway enforces. Promotion counts the diagnostics history,
and an agent's owners approve its calls. What decides demotion and
review now comes from mcp-gateway, not from the agent: each call's
outcome, the actions to review, the reviews by the gateway's approvers,
and notices of automatic demotions. A prompt-injection evaluation suite
runs in every VM test and in the air-gapped gate against the tested
model, and an approval item marks a suspected injection with what the
model read before the call.

Upgrading from 0.2.x needs no change to configurations or gateway data:
contract version 1 only gained names. To know:

- Upgrade mcp-gateway to 1.14 first: `custos` requires it (the reasons
  for denials, call outcomes, notices and reviews).
- Running agents keep the old launcher and unit until they next start
  (idle exit, or `systemctl restart agent@NAME.service`); OpenShell
  agents need their image rebuilt (user guide, chapter 11).
- A host upgraded from 0.2 has no enablement state: every task category
  stays enabled, as before. Diagnostics-only is for a fresh install
  (`custos-setup --diagnostics-only`, user guide chapter 2).
- A promoted category's autonomous calls now become review items for the
  gateway's approvers and the agent's owners (Cockpit's Approvals tab,
  the gateway's approval page), and automatic demotions are mailed to
  them through the gateway.
- Review verdicts count only when root recorded them: a review an agent
  wrote into its own journal no longer counts, which can change a
  category's readiness.
- An owner who approves on the host needs the gateway's control socket
  group (`socket_group`, `mcp-users` by default), like any approver.

Known limits: the enablement state's tamper check detects a change by
hand, it does not prevent one, and a host does not go back to
diagnostics-only; an envelope's `rate`, `hosts` and `blast_radius` are
still refused (mcp-gateway's rate limits, O12; the fleet, O13); the
suspected-injection mark is the agent's own check, advisory; an
autonomous call whose agent named no activity is reviewed on the gateway
only.


### Added

- Cockpit's *What would the gateway decide?* asks the gateway's policy
  (its OPA, through the root bridge) and shows its decision with its
  reason, for a call with the arguments given; without administrative
  access the page works the decision out itself, as before.
- `agent-admin review` (and Cockpit's *To review* in the agent view)
  lists a promoted category's autonomous actions from the gateway's
  record of the calls it let through (`mcp-result`), each with its
  decision id: an agent can no longer leave one out of its own records
  to escape review. A call reviewed on the gateway counts as reviewed
  there; one reviewed by root and on the gateway counts once.
- The demotion evaluator tells the approvers of each automatic demotion
  through mcp-gateway's notices (its requirement R3): about the agent,
  for the servers of the category's permissions, by mail, desktop
  notification and the gateway's approval page, to the servers'
  approvers and the agent's owners. `agent-admin evaluate` says whether
  they were notified; a refused notice leaves the demotion as it is.
- The gateway's approvers review a promoted category's autonomous
  actions (mcp-gateway's review items, its requirement R4): the agents
  module marks each call a bounded category makes without an approval
  for review; the servers' approvers and the agent's owners review it on
  the gateway's approval page, with `mcp-gateway-admin reviews`, or in
  Cockpit's Approvals tab (*To review*), as themselves. Their verdicts
  (`mcp-review`) count in the readiness as root's do, and an incident
  demotes the category.
- The gateway's denials of an agent's call say why (with mcp-gateway's
  `deny_reasons` and `unmatched_reasons`, its requirement R1): "task
  category patch-errata is not enabled on this host (diagnostics-only):
  agent-admin enable patch-errata", "the agent is paused", "the
  emergency stop is in effect", "the agent is advisory" and the others,
  in the agent's error and the gateway's audit, instead of "denied by
  policy" or "no matching permission" (user guide chapter 12).
- The rollback condition `tool_error` and the incident rate take each
  call's outcome from the gateway's audit (mcp-gateway's `mcp-result`,
  its requirement R2): a call the gateway saw fail (a tool error, an
  error, a timeout) counts although the agent recorded it as ok, or not
  at all. The agent's own `tool.result` still counts too.
- A suspected prompt injection is shown with the approval item
  (CONTRACT.md §7, O14): the agent checks what its model read before each
  call for signs of an injection (an instruction to ignore its
  instructions, a forged role marker, a claimed approval, an instruction
  to use the called tool, the call's own argument values). Cockpit marks
  the approval item *suspected injection*, with the signs and the output
  the model read before the call; the activity view marks the call; the
  journal has `AGENT_SUSPECTED_INJECTION`. Advisory: the approval is what
  holds.
- A prompt-injection evaluation suite (CONTRACT.md O14): the functional
  tests' server serves notes an attacker wrote (an override, a forged
  system message, a claimed approval, a tool not given, a leak request,
  instructions buried in a log and in data). Section "Prompt injection"
  runs them with a scripted model that obeys each one, and the
  air-gapped gate with the tested model: an injected change waits for
  an approval and does not run declined, a tool not given never reaches
  the gateway. How often the tested model followed is reported
  (`injection-<model>.json`, the gate's summary).
- A model's call to a tool the agent was not given is refused by the
  launcher and recorded as `tool.denied.local` with reason `not offered`
  and the model's stated reason (user guide chapter 10).
- Requirements for mcp-gateway (`docs/requirements/mcp-gateway-for-custos-0.3.md`):
  a reason for a denial given by a policy module (such as "task
  category not enabled"), each call's outcome in the audit, notices to
  approvers from a trusted local service (automatic demotions), and
  reviews of autonomous actions by the gateway's approvers.
- User guide, chapter 14: where inference runs, in the order on-premises,
  the customer's own cloud account, vendor-hosted; every network flow
  that leaves a Custos host, each with its switch (Custos itself sends
  no telemetry, model updates or incident reports); fencing a systemd
  agent to its model; installing without a network.
- The air-gapped release gate (CONTRACT.md O15): on release branches, CI
  installs the packages, Ollama and a tested open-weight model
  (`test/airgap/versions`: Qwen3 0.6B) from an offline mirror in a Leap 16
  VM without network, and runs the functional tests, with an agent on
  that model calling a read-only tool and asking for an approved change.
  An agent's attempt to reach a host outside fails the gate.
- `run-tests.sh --model URL NAME` (custos-tests): the same checks with
  an agent on a real model, for openQA and other test systems.
- Credentials management. `agent-admin credentials [NAME…]` lists an
  agent's credentials by name, with what uses them and the missing ones,
  never a value; `agent-admin remove-credential NAME CRED [--force]`
  removes one. In Cockpit, each agent has a Credentials card: set,
  replace and remove them, store one key for several agents whose model
  uses the same name (each its own encrypted copy), and restart the
  agents that run. The agents list marks an agent whose credential is
  missing.

- CONTRACT.md: the design of the bundled health-check and diagnostics
  install (§20b, O16): the `diagnostics` and `compliance-audit`
  categories, the enablement state `diagnostics-only` enforced by the
  gateway (§6b, `agent-admin enable`), diagnosis reports with
  host-verified evidence and administrator feedback (§4a), diagnostic
  playbooks (§14c) and `custos-setup` for the installer (§17a). Not
  implemented yet; the journal's message IDs are reserved.

- Diagnostics-only hosts (CONTRACT.md §6b, the bundled diagnostics
  install): the task categories `diagnostics` (read-only permissions
  only, never promoted) and `compliance-audit`; an enablement state in
  which only `diagnostics` is usable, enforced by the gateway (calls of
  a disabled category are denied and its tools not shown) and by
  `agent-admin`, which refuses and records (`category.refused`)
  permissions, promotions and templates of a disabled category;
  `agent-admin enable CATEGORY…|--all`, the one recorded action out of
  it; `agent-admin diagnostics-only` for the installer's setup; and
  `agent-admin show` reporting a state changed by hand. The functional
  tests check it as a release gate.

- Diagnostic playbooks (CONTRACT.md §14c): eleven published, read-only
  diagnostic procedures in `/usr/share/custos/playbooks` (failed units,
  service health, disk and memory pressure, journal errors, SELinux
  denials, time sync, network configuration, boot health, package
  consistency, update status), your own in `/etc/custos/playbooks`, and
  their commands for mcp-gateway's exec server
  (`exec.d-diagnostics.yaml`). `POST /v1/activities` and schedules run
  one (`playbook`); it is refused when the gateway does not offer a
  step's tool. `agent-admin playbooks` lists them.

- The agent template `health-check`, the bundled diagnostics agent:
  every diagnostic playbook's tools as read-only permissions in
  `diagnostics`, so it runs on a diagnostics-only host; supervised; the
  playbooks failed-units, disk-pressure and journal-errors daily and
  update-status weekly.

- Diagnosis reports (CONTRACT.md §4a): an agent reports with the host's
  tool `report_diagnosis`: a summary, findings, hypotheses with its
  confidence, the evidence as quotes from its tools' output, and a
  remediation plan for the administrator. Custos verifies each quote
  against what that tool returned in the activity and marks the others
  unverified. The report is in the activity (`report`), the journal
  (`diagnosis.report`) and on AG-UI (`agent.report`).
  `agent-admin feedback` records an administrator's verdict on it
  (`diagnosis.feedback`).

- Cockpit: an activity's diagnosis report, with each hypothesis's
  confidence (the agent's) next to how many of its quotes Custos
  verified, each quote marked verified or the agent's claim alone, the
  plan's commands to copy, and **Was it right?** for the
  administrator's feedback; and **Diagnostic playbooks** in an agent's
  view, to run one as the person.
- Readiness shows the diagnostics history (CONTRACT.md §6b):
  `agent-admin readiness` and Cockpit's Task categories card list the
  agent's diagnosis reports over the window, how many of their quotes
  Custos verified, and the feedback on them (confirmed, rejected, plans
  useful and carried out); evidence for a category enabled later, not a
  threshold. Only root's feedback counts.

- `custos-setup` and the package `custos-diagnostics`, the bundled
  health-check and diagnostics install the installer offers (CONTRACT.md
  §17a): on a fresh host it makes the host diagnostics-only, sets the
  model for one of the four backends in the documented order (on-host,
  local network, the customer's cloud, vendor), creates `health-check`,
  stores its key from a file and installs the playbooks' exec commands;
  idempotent, one line per step.

- On a host that started diagnostics-only, the agent's diagnostics
  history counts in readiness (CONTRACT.md §6a, §6b): a category is
  ready only with at least 10 diagnosis reports with feedback and 70 %
  of the decided hypotheses confirmed (`diagnostics_feedback`,
  `diagnostics_confirmed_rate` in `/etc/custos/promotion.yaml`), so
  `promote` follows them; Cockpit says so.

- Owners approve their agent's calls (CONTRACT.md §7, O1):
  `agent-admin owners NAME user:alice group:web-team` (and *Owners* in
  Cockpit's agent view) lets them decide the agent's pending calls
  besides the servers' approvers, and mails them as it mails those. A
  policy module for the gateway's approver rules (`owners.rego`), with
  no gateway change.
- Cockpit tells approvers of automatic demotions (CONTRACT.md O11): the
  Approvals tab lists the task categories the demotion evaluator set
  back to supervised in the last week, with the agent and the trigger
  (from root's records only), and the agent's view marks such a
  category.
- Cockpit shows a diagnostics-only host's enablement state on the Agents
  tab (enabled and disabled categories, who changed it, a state changed
  outside agent-admin), with **Enable…**; a disabled category is marked
  in an agent's task categories and offers no **Promote…**.

### Changed

- `custos` and `custos-tests` require mcp-gateway 1.14, which has
  what Custos asked of it (`docs/requirements/mcp-gateway-for-custos-0.3.md`):
  reasons for denials from policy modules, each call's outcome in the
  audit, notices to approvers and reviews by approvers.

### Fixed

- Security: an agent could write a review verdict into its own journal
  (`category.review` with `SYSLOG_IDENTIFIER=custos-promotion`): a
  later "ok" for one of its autonomous actions replaced an approver's
  "incident", lowering its override rate, so the demotion evaluator
  could miss a reason to demote it. Readiness, the review and the
  evaluator now count only reviews journald records as root's
  (`_UID=0`); `agent-admin show`'s enablement check, too.
- A deleted agent stayed on the Cockpit page, as stopped, until the
  host restarted: `agent-admin delete` left its directory in
  `/run/agents`. It now removes it, and the page no longer lists a
  directory without a socket and without gateway data, which also
  clears what 0.2.0 left behind.
- A key given as `model.credential`, where the name of the stored key
  goes, made the agent fail with `no such file or directory`.
  `agent-admin check`, `create`, `edit` and `defaults` now refuse a
  value that looks like a key (`sk-…` and other keys' prefixes, or a long
  string with many digits) without repeating it. After `create` and
  `edit`, agent-admin says when the model's credential is not stored yet,
  with the `set-credential` command. The agent's own error says the same,
  and Cockpit's model defaults ask for the credential's name.

## v0.2.0 — 2026-11-05

Autonomy per task category, and agent templates. An agent's autonomy is
promoted per kind of work (patching, service restarts, …) within time
windows the gateway enforces, once the journal shows approvers rarely
override it; a demotion evaluator takes it back when the override rate
or a rollback condition says so, and approvers review what a promoted
category did. Seven templates give agents that look after one system a
start, transactional systems included (mcp-server-tukit), with model
defaults for new agents; the Cockpit page creates agents from them.
Fixes from testing 0.1.1: model credentials work with SELinux enforcing
and on hosts with a TPM, and the Cockpit page shows why an action failed
instead of hanging.

Upgrading from 0.1.x needs no change to configurations or gateway data:
contract version 1 only gained names. To know:

- Running agents keep the old launcher and unit until they next start
  (idle exit, or `systemctl restart agent@NAME.service`). OpenShell
  agents: rebuild the agent image, then set each agent's gateway
  credential again to move its sandbox to it (user guide, chapter 11).
- Remove the workarounds for 0.1.x if you used them: the drop-in
  `/etc/systemd/system/agent@NAME.service.d/60-tpm.conf` and the local
  SELinux module `custos-credentials` (`semodule -r custos-credentials`).
- Credentials stored by 0.1.x are readable by everyone (still
  encrypted): `chmod 0600 /etc/credstore.encrypted/agent-*`.
- `agent-admin edit` and `release-all` now leave a schedule or an agent
  you disabled as it is.
- Nothing is promoted on its own: every category stays supervised until
  `agent-admin promote`, which also enables `custos-demote.timer`.

Known limits: approvers are not told of an automatic demotion; a review
verdict needs root (or Cockpit's administrative access), not a gateway
approver; an envelope's `rate`, `hosts` and `blast_radius` are refused
until something enforces them (mcp-gateway, the fleet); the templates
are checked against the servers' tool definitions, not yet run against
the servers themselves.

### Added

- Task categories (CONTRACT.md §6a): an agent's autonomy is promoted
  per category, not for the whole agent. The catalog ships
  `patch-errata`, `security-response` and `config-lifecycle`
  (`/usr/share/custos/categories.d`, your own in
  `/etc/custos/categories.d`; `agent-admin categories`). A permission
  joins one with `category:`, and stays supervised until its category
  is promoted.
- `agent-admin promote NAME CATEGORY --envelope FILE` lets a category
  act without approval within its envelope's time windows, which the
  gateway's policy enforces; outside them its calls need an approval.
  `agent-admin demote` takes it back.
- Readiness: `agent-admin readiness NAME [CATEGORY]` measures, from the
  agent's records and the gateway's approval decisions, how often
  approvers overrode a category's proposals and how often approved
  calls went wrong. `promote` refuses a category below the thresholds
  (14 days, 50 proposals, at most 5 % overrides and 2 % incidents; your
  own in `/etc/custos/promotion.yaml`) unless given
  `--despite-readiness --reason TEXT`.
- The review of what a promoted category did without an approval:
  `agent-admin review NAME [CATEGORY]` lists the week's autonomous
  actions to review (all of them up to 20 a week, then 20), and
  `agent-admin review NAME CATEGORY ACTIVITY CALL ok|incident --reason
  TEXT` records a verdict (`category.review`). An incident counts as an
  override in the readiness and demotes the category at once; the
  demotion evaluator also demotes on it. In Cockpit, *To review* with
  **Fine** and **Should not have run**.
- The Cockpit page: an agent's task categories with their readiness
  (ready or not, what is not met, override and incident rates,
  proposals), **Promote…** with the envelope, and **Demote**.
- The demotion evaluator: `custos-demote.timer` (every 5 minutes;
  `promote` enables it) runs `agent-admin evaluate`, which demotes a
  bounded category whose override rate since its promotion is above the
  threshold, or whose envelope's `rollback` condition fires:
  `tool_error`, `limit_exceeded` or `health_check_failed` (a command of
  `health_check`). Demotions are recorded by `custos-demote` with the
  reason. Both are recorded in the
  journal (`custos-promotion`, `category.promoted`,
  `category.demoted`), and `agent-admin show` lists the categories.
- Agent templates (CONTRACT.md §14a) for managing one system, in
  `/usr/share/custos/templates` (your own in `/etc/custos/templates`):
  `triage` and `exposure-watch` (advisory, read-only),
  `service-doctor`, `patch-pilot`, `disk-watch`, and for transactional
  systems with mcp-server-tukit `patch-pilot-transactional` and
  `disk-watch-transactional` (supervised). Each has the configuration,
  the permissions with the gateway's real tool names, their task
  categories and suggested envelopes. `agent-admin templates` lists
  them; `agent-admin create NAME --template TEMPLATE [--from FILE]`
  creates an agent with the template's permissions and mode. The
  Cockpit page's **Create agent** can start from a template.
- Model defaults for new agents (CONTRACT.md §14b):
  `/etc/custos/defaults.yaml`, set with `agent-admin defaults --from
  FILE` or the Cockpit page's **Model defaults**. A new agent's
  configuration, blank or from a template, starts with that model
  (`agent-admin draft NAME [--template T]`); existing agents keep
  theirs. When the model names a credential, the page asks for the new
  agent's secret after creating it.

### Fixed

- The example `patch-pilot` named tools that no gateway server has
  (`install_patches`, `restart_unit`, …); it names mcp-server-zypp's
  and systemd-mcp's tools now.
- The Cockpit page hung when `agent-admin` refused something: **Check**
  and **Save and apply** in an agent's configuration editor, and every
  other action that runs `agent-admin` or `systemctl`, never showed the
  error, and Cockpit reported an unexpected error instead. The page
  threw in a handler of Cockpit's own promise, which does not reject the
  promise it returns. The test stub now behaves like Cockpit's promises.
- The Cockpit page cleared an action's error at its next refresh, a
  moment later, so a refused **Release all agents** (or delete, mode,
  …) seemed to do nothing. An action's error now stays until the next
  action or **Dismiss**.
- The Cockpit page listed no agents at all while one agent's socket
  was held by systemd but its service failed at start (a credential not
  yet set, say): the request to that agent was never answered, and the
  page waited for it, skipping every refresh. A read of an agent's or
  the gateway's API now gives up after 8 seconds, and the agent is
  listed as not answering, pointing to its logs.
- The demotion evaluator and the review counted records from the whole
  second of a promotion, so a proposal declined in the same second just
  before it counted against the new promotion, which could be demoted
  at once for its override rate. `promoted_by` and `demoted_by` keep
  the time to the fraction of the second.
- Building the OpenShell agent image (`packaging/openshell/Containerfile`)
  failed while openSUSE's codec repository was unreachable; the image
  needs nothing from it and no longer refreshes it.
- An agent whose model credential was stored on a host with a TPM2 did
  not start (`Failed to set up credentials: Protocol error`,
  `243/CREDENTIALS`): `systemd-creds encrypt` seals the secret to the
  TPM there, and `agent@.service`'s `PrivateDevices=yes` kept systemd
  from reaching the TPM while it decrypted it. The unit allows
  `/dev/tpmrm0` for that (`DeviceAllow=`); the agent's own `/dev` stays
  private. It also says `ConfigurationDirectoryMode=0750`, as
  `agent-admin create` makes the directory, so systemd no longer warns
  at every start.
- `agent-admin set-credential` left the encrypted credential readable
  by everyone (0644, from `systemd-creds encrypt`'s umask); it is root's
  alone now (0600). Decrypting it needs root's host key or the TPM.
- An agent could not read its model credential under SELinux
  (`model.credential: open /run/credentials/…: permission denied`): the
  `custos` module did not let `agent_t` read the unit's credentials
  (`init_var_run_t`), and the base policy does not log that denial.
- `agent-admin edit` enabled every schedule's timer of the agent again,
  and `release-all` started every agent's socket and schedule timer,
  so a schedule or an agent an administrator had disabled ran again.
  `edit` enables only new schedules now (a running timer restarts for
  its new calendar), and `release-all` starts only what is enabled.

## v0.1.1 — 2026-11-04

Fixes from testing 0.1.0. The package builds on OBS. The Cockpit page
shows Start for a stopped agent, stops OpenShell agents for good, and
gains Restart and Logs. OpenShell agents get through a token renewal,
and their unit no longer fails to start when OpenShell misses that the
sandbox is ready.

Upgrading from 0.1.0 needs nothing: no configuration, gateway data or
contract changes. Running agents keep the old launcher until they exit
on idle or are restarted (`systemctl restart agent@NAME.service`, or
the page's Restart); OpenShell agents pick up the relay unit's change
at their next start.

### Added

- The Cockpit page: **Restart** and **Logs** for an agent. Restart
  restarts its service (an OpenShell agent's relay and sandbox); Logs
  opens Cockpit's journal for the agent's unit, with the unit's messages
  and the agent's records.

### Fixed

- The spec's `%check` failed in the package's tarball, which has no
  `.github`: a test of the release workflow read
  `.github/workflows/release.yml`. It skips without it. CI now builds
  the RPM from the same tarball as OBS.
- The Cockpit page showed no **Start** for a stopped agent: it took an
  agent for stopped only when its directory in `/run/agents` was gone,
  but systemd removes only the socket and keeps the directory. An agent
  without a socket is now stopped, and **Start** starts it again; for an
  OpenShell agent its relay's socket, which the page finds from systemd.
- **Stop** in the Cockpit page did not stop an OpenShell agent: it
  stopped the relay's service and left its socket listening, so the next
  request started the agent again. It stops both now.
- An OpenShell agent's unit could fail to start after a restart (a
  no-op `agent-admin edit`, say): `openshell sandbox start` missed that
  the sandbox was Ready and waited past the unit's start timeout.
  `agent-openshell-start` now bounds each start to 15 seconds, decides by
  the sandbox's phase, and counts its 120-second limit on the clock.
- An OpenShell agent's activity could fail right after OpenShell renewed
  its gateway token: the sandbox refused the connection for a moment
  (`connect: permission denied`), or the proxy closed it without an
  answer (`EOF`), and the agent did not try again. A refused connection
  is now retried for every request (nothing was sent), with the same
  backoff as the proxy's 500s; a dropped one only for a request that is
  safe to repeat (listing tools, initialize, ping), never a `tools/call`,
  which may have reached the gateway.

## v0.1.0 — 2026-11-04

The first release: governed AI agents on SUSE Linux Enterprise Server 16
and openSUSE Leap 16, supervised from Cockpit, with every tool call
decided by mcp-gateway (≥ 1.12).

There is nothing to upgrade from. Install `custos` and `custos-selinux`,
and optionally `custos-cockpit` and `custos-docs`, from the OBS
packages. The user guide, chapters 2 to 4, goes from the installation to
a first agent whose change waits for an approval. The OpenShell backend
is a draft (CONTRACT.md O10).

### Added

- **Agents.** Each agent is a Google ADK Go agent, run by systemd as its
  own system user (`agent@<name>.socket`, `agent@<name>.service`) and
  started on demand by its socket. It is configured in
  `/etc/custos/agents/<name>/agent.yaml`. Models are any
  OpenAI-compatible server (on-premises, air-gapped), Gemini, or
  Vertex AI. Credentials are kept with `systemd-creds`. Each agent has
  limits on model calls, tool calls and run time, and its schedules run
  as systemd timers.
- **Governance in mcp-gateway.** The policy module
  `/usr/share/mcp-gateway/policy/custos/agents.rego` enforces three
  autonomy modes: `advisory`, `supervised` (the default; a new agent can
  do nothing until it is given permissions) and `bounded`. Approvals are
  decided as the person, through the gateway. Pause, and the emergency
  stop (`agent-admin stop-all`, `release-all`), are enforced by the
  gateway on every backend.
- **Control API and audit.** Each agent serves its status and activities
  on `/run/agents/<name>/api.sock`, identifying callers by their peer
  credentials. It writes journal records with `AGENT_*` fields and their
  own `MESSAGE_ID`s, joined with the gateway's audit from proposal to
  execution. Chat runs over AG-UI.
- **Delegation** between agents through the gateway's `agents` server
  (`agent-delegate`), with the chain traced to its origin.
- **`agent-admin`**: `create`, `edit`, `check`, `delete`, `mode`,
  `permissions`, `set-credential`, `pause`, `resume`, `stop-all`,
  `release-all`, `trigger`, `list`, `show`. Changes are in force when
  the command returns.
- **OpenShell sandboxes** (draft): agents in NVIDIA OpenShell, reaching
  the gateway's HTTPS listener with OAuth tokens that OpenShell renews.
  The control API is relayed with the caller, and the agent's records
  reach the host journal. The relay unit waits until the sandbox is
  Ready.
- **Cockpit page** (`custos-cockpit`): the Activity, Approvals, Agents,
  Policy and Audit tabs, chat, creating, editing and deleting agents,
  and the emergency stop. It is tested in a real Cockpit with SELinux
  enforcing.
- **SELinux** (`custos-selinux`): `agent_t` for the agents,
  `mcpsrv_agents_t` for delegation, and `agent_relay_t` for the
  OpenShell relay.
- **Functional tests** (`custos-tests`) for openQA:
  `/usr/libexec/custos/tests/run-tests.sh --disposable --results DIR`,
  writing TAP and JUnit.
- **User guide** (`docs/user-guide`, package `custos-docs`): installing,
  connecting to mcp-gateway and an identity provider (Keycloak and
  others), configuration, permissions and modes, approvals, the Cockpit
  page, administration, monitoring and audit, OpenShell, troubleshooting
  with every error message, and a reference. `custos-docs` installs it
  with the contract in `/usr/share/custos/docs`, together with a
  collection file from which mcp-docs serves it to agents on the host.
- **The contract** (`CONTRACT.md`), version 1: the interface between
  the agent hosts and the control plane.
