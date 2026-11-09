# Changelog

All notable changes to Custos. Versions follow
[Semantic Versioning](https://semver.org/). Within contract version 1
(CONTRACT.md), names, paths and fields are added, never renamed or
removed.

## Unreleased

### Changed

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

- The report nudge (#155): a model that answers a playbook run in prose,
  without a report and without a tool call, is asked once more, in the
  same activity, to file it with `report_diagnosis`. The activity says
  so (`report_nudged`), and so does the answer's `model.call` record
  (`AGENT_REPORT_NUDGE`). `model-eval.sh` shows which runs were asked
  and counts the reports filed after it.
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

### Fixed

- `update-status` could not see a reboot or restart that an update waits
  for: its only required step, `systemd__check_restart_reload`, reports
  only on restarts systemd-mcp started itself. Its new step
  `exec__reboot_status` (a read-only diagnostic command) says a reboot
  is pending when a kernel newer than the running one is installed
  (from rpm) or `/run/reboot-needed` exists (zypper and
  transactional-update write it after an update that needs a reboot),
  and lists the running and the installed kernels. The exec server's
  sandbox does not show the host's `/run/reboot-needed`, so without a
  newer kernel it says that file may not be visible rather than that
  nothing waits. Which services wait for a restart
  (`zypper ps`) is not shown: it needs to read other users' processes,
  and the command runs as the agent's user. health-check no longer has
  `check_restart_reload`.
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

### Added

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
