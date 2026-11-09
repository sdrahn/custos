# Autonomy modes of Custos agents (CONTRACT.md §6), added to
# mcp-gateway's shipped policy (user guide chapter 12, approach A).
# Installed as /usr/share/mcp-gateway/policy/custos/agents.rego.
#
# An agent's principal is its user agent-<name>. Its permissions come
# only from data.mcp.agents.<name>, which agent-admin writes to
# /etc/mcp-gateway/policy/agents/<name>.yaml (one directory, which OPA
# watches from its start: it does not watch directories made later):
#
#   <name>:
#     mode: supervised        # advisory | supervised | bounded (default supervised)
#     paused: false           # true: every call is denied (Intervene)
#     permissions:            # gateway permissions, plus two flags:
#       - {server: zypp, tool: list_*, read_only: true}
#       - {server: zypp, tool: install_patches}                   # changes the system
#       - {server: systemd, tool: restart_unit, autonomous: true, args: {unit: '^nginx\.service$'}}
#
#       - {server: zypp, tool: patch, category: patch-errata}      # a task category (§6a)
#     categories:             # written by agent-admin promote / demote
#       patch-errata:
#         mode: bounded       # supervised | bounded
#         envelope: {windows: [{days: [sat], from: '02:00', to: '04:00', tz: Europe/Berlin}],
#                    rate: {calls: 20, per: 1h}}
#
# A permission is read-only only when it says so; everything else
# changes the system (supervised by default). By mode:
#
#   advisory    read-only permissions; nothing that changes the system
#   supervised  read-only ones allowed; the others need an approval
#   bounded     also the "autonomous" ones without an approval; the
#               others need an approval
#
# Tool annotations (readOnlyHint) are untrusted and never used here.
#
# Over the gateway's HTTPS listener (agents in an OpenShell sandbox,
# CONTRACT.md §16a) an agent is a token with subject agent-<name> from an
# issuer listed in data.mcp.agents._http.issuers (agents/_http.yaml,
# none by default): that issuer issues agent-<name> subjects to agents
# only. Such a subject from any other issuer is denied everything.
# data.mcp.agents._control.stopped (agents/_control.yaml, agent-admin
# stop-all) denies every agent everything: the emergency stop.
# data.mcp.agents._enablement (agents/_enablement.yaml, agent-admin
# enable) disables task categories (below). Agent names start with a
# letter, so _control and _enablement are no agent's.
#
# Every denial has a reason (deny_reasons), and a call a permission names
# that is no permission now is explained (unmatched_reasons): for the
# agent's error and the gateway's audit, with an mcp-gateway that takes
# them from modules (its R1); an older one ignores both sets.
package mcp.authz

import rego.v1

agent_principal if {
	input.principal.transport == "unix"
	startswith(input.principal.sub, "agent-")
}

agent_principal if {
	input.principal.transport == "http"
	startswith(input.principal.sub, "agent-")
	agent_issuer
}

agent_issuer if input.principal.iss in http_issuers

# The issuers trusted for agents: _http.yaml's, when the seal covers it
# (below) or does not cover agent data; none otherwise.
http_issuers := object.get(object.get(data.mcp.agents, "_http", {}), "issuers", []) if {
	data_trusted("_http")
} else := []

agent_name := substring(input.principal.sub, 6, -1) if agent_principal

agent_data := data.mcp.agents[agent_name]

agent_mode := object.get(agent_data, "mode", "supervised")

agent_flags := ["read_only", "autonomous", "category"]

perms contains q if {
	some p in agent_data.permissions
	permission_enabled(p)
	q := agent_permission(p)
}

# Category enablement (CONTRACT.md §6b): data.mcp.agents._enablement
# (agents/_enablement.yaml, agent-admin enable) says which task
# categories this host may use at all. A permission of a disabled
# category is no permission: its calls are denied in every mode, and
# discovery does not show its tools. Without the data every category is
# enabled; an unknown state enables none.
enablement := object.get(data.mcp.agents, "_enablement", {})

# The seal (§6b, #109): data.mcp.agents._seal.public_key (agents/_seal.yaml)
# is the public key agent-admin signs the state with (ES256; its private
# key is a systemd credential, root's). Once a host has one, the state
# counts only with a signature over its state, enabled and record: edited
# by hand, unsigned or deleted, the host is diagnostics-only. Without a
# seal (a host installed before it, or with the full capability and never
# changed), the state counts as it is.
seal_key := object.get(object.get(data.mcp.agents, "_seal", {}), "public_key", "")

default enablement_signed := false

enablement_signed if {
	io.jwt.verify_es256(enablement.signature, seal_key)
	[_, payload, _] := io.jwt.decode(enablement.signature)
	payload.state == enablement.state
	payload.enabled == object.get(enablement, "enabled", [])
	payload.record == enablement.record
}

default enablement_seal_broken := false

enablement_seal_broken if {
	seal_key != ""
	not enablement_signed
}

enablement_state := object.get(enablement, "state", "") if not enablement_seal_broken

enablement_state := "diagnostics-only" if enablement_seal_broken

# The seal of the agents' data (§6b, #158): with agents: true in
# _seal.yaml (agent-admin seals a new host so, or agent-admin seal
# --agents), each agent's data and _http.yaml count only with a signature
# by the seal's key over {"agent": <its name>, "data": <it without the
# signature>}. An agent whose data the seal does not cover (edited by
# hand, unsigned, another agent's) is denied everything; _http.yaml then
# trusts no issuer. _control.yaml, the emergency stop, is never sealed.
default agents_sealed := false

agents_sealed if {
	seal_key != ""
	object.get(object.get(data.mcp.agents, "_seal", {}), "agents", false) == true
}

data_signed(name) if {
	d := data.mcp.agents[name]
	io.jwt.verify_es256(d.signature, seal_key)
	[_, payload, _] := io.jwt.decode(d.signature)
	payload.agent == name
	payload.data == object.remove(d, {"signature"})
}

# Data the gateway counts: any, without a seal over agent data; a signed
# one, with it.
data_trusted(_) if not agents_sealed

data_trusted(name) if data_signed(name)

permission_enabled(p) if {
	not misdeclared_diagnostics(p)
	category_usable(p)
}

# diagnostics only reads: a permission in it that is not read-only is
# none.
misdeclared_diagnostics(p) if {
	p.category == "diagnostics"
	not p.read_only == true
}

category_usable(_) if enablement_state == ""

category_usable(p) if {
	enablement_state == "diagnostics-only"
	p.category == "diagnostics"
}

category_usable(p) if {
	enablement_state == "categories"
	not has_category(p)
}

category_usable(p) if {
	enablement_state == "categories"
	p.category == "diagnostics"
}

category_usable(p) if {
	enablement_state == "categories"
	p.category in object.get(enablement, "enabled", [])
}

agent_permission(p) := object.remove(p, agent_flags) if {
	p.read_only == true
} else := with_envelope_rate(object.remove(p, agent_flags), p) if {
	autonomous_now(p)
} else := object.union(object.remove(p, agent_flags), {"require_approval": true, "approval_channel": "oob"}) if {
	permission_mode(p) in {"supervised", "bounded"}
}

# Task categories (CONTRACT.md §6a). A permission with a category takes
# its category's mode: bounded only once agent-admin promote has given
# the category an entry, otherwise supervised. advisory on the agent
# caps every category. A permission without one keeps the agent's mode,
# with version 1's autonomous flag.
agent_categories := object.get(agent_data, "categories", {})

permission_mode(p) := "advisory" if {
	agent_mode == "advisory"
} else := object.get(object.get(agent_categories, p.category, {}), "mode", "supervised") if {
	has_category(p)
} else := agent_mode

# A helper, so that "not has_category(p)" holds for a permission without
# the key (not is_string(p.category) does not).
has_category(p) if is_string(p.category)

autonomous_now(p) if {
	not has_category(p)
	agent_mode == "bounded"
	p.autonomous == true
}

autonomous_now(p) if {
	has_category(p)
	permission_mode(p) == "bounded"
	in_envelope(agent_categories[p.category])
	rate_valid(agent_categories[p.category])
}

# The envelope's rate (§6a, O12): {calls: N, per: 1s | 1m | 1h}, the
# gateway's approval_over_rate obligation ("N/s", "N/m", "N/h"; its
# requirement R6, mcp-gateway 1.15) on each of the category's permissions
# while they act without an approval. The gateway counts per agent and
# tool, in its memory; a call over the rate waits for an approval (scope
# once), as if the category were supervised, and is not counted once
# approved. Calls that need an approval anyway are not counted: a person
# decides each. A rate that is not one of these makes the category
# supervised: it is never ignored.
rate_units := {"1s": "s", "1m": "m", "1h": "h"}

envelope_rate(c) := object.get(object.get(c, "envelope", {}), "rate", null)

rate_valid(c) if envelope_rate(c) == null

rate_valid(c) if envelope_rate_obligation(envelope_rate(c))

envelope_rate_obligation(r) := sprintf("%d/%s", [r.calls, rate_units[r.per]]) if {
	is_number(r.calls)
	r.calls == round(r.calls)
	r.calls >= 1
	count(r) == 2
}

with_envelope_rate(q, p) := q if {
	not has_category(p)
} else := q if {
	envelope_rate(agent_categories[p.category]) == null
} else := object.union(q, {"obligations": object.union(object.get(q, "obligations", {}), {"approval_over_rate": array.concat(
	as_list(object.get(q, ["obligations", "approval_over_rate"], [])),
	[envelope_rate_obligation(envelope_rate(agent_categories[p.category]))],
)})})

# The envelope's windows (§6a): outside them a bounded category acts as
# supervised. Each window: days (mon … sun; all if absent), from and to
# ("HH:MM", from before to) in tz (an IANA zone; UTC if absent).
in_envelope(c) if {
	count(object.get(object.get(c, "envelope", {}), "windows", [])) == 0
}

in_envelope(c) if {
	some w in c.envelope.windows
	in_window(w, time.now_ns())
}

in_window(w, ns) if {
	tz := object.get(w, "tz", "UTC")
	day := lower(substring(time.weekday([ns, tz]), 0, 3))
	day in object.get(w, "days", ["mon", "tue", "wed", "thu", "fri", "sat", "sun"])
	[h, m, _] := time.clock([ns, tz])
	now := (h * 60) + m
	minutes(w.from) <= now
	now < minutes(w.to)
}

minutes(hhmm) := (to_number(substring(hhmm, 0, 2)) * 60) + to_number(substring(hhmm, 3, 2))

# Every denial of an agent's call, with its reason: mcp-gateway's
# deny_reasons (from its R1, sdrahn/mcp-gateway#335) names them in the
# decision, the agent's error and the audit; an older gateway reads
# "denied by policy" and ignores the set.
denied if count(agent_denials) > 0

deny_reasons contains r if some r in agent_denials

# The agent's only permissions are its agent data: a role bound to an
# agent user would bypass its mode, so it denies everything instead.
agent_denials contains "a role is bound to the agent's user: an agent's permissions are only its agent data" if {
	agent_principal
	some r in roles
	r != ""
}

# An agent's subject from an issuer not trusted for agents: not an agent,
# and nothing else either.
agent_denials contains "an agent's subject from an issuer not trusted for agents" if {
	input.principal.transport == "http"
	startswith(input.principal.sub, "agent-")
	not agent_issuer
}

# An issuer _http.yaml lists, which the seal does not cover.
agent_denials contains "the agents' token issuers (_http.yaml) do not match their seal (changed outside agent-admin): no issuer is trusted until agent-admin issuers writes them again" if {
	input.principal.transport == "http"
	startswith(input.principal.sub, "agent-")
	input.principal.iss in object.get(object.get(data.mcp.agents, "_http", {}), "issuers", [])
	not data_trusted("_http")
}

# An agent without agent data gets nothing.
agent_denials contains "the agent has no agent data (agent-admin create)" if {
	agent_principal
	not agent_data
}

# An unknown mode gets nothing.
agent_denials contains sprintf("the agent's mode %v is unknown", [agent_mode]) if {
	agent_principal
	not agent_mode in {"advisory", "supervised", "bounded"}
}

# So does an unknown mode of a category.
agent_denials contains sprintf("the mode %v of task category %s is unknown", [c.mode, name]) if {
	agent_principal
	some name, c in agent_categories
	not c.mode in {"supervised", "bounded"}
}

# Pause and emergency stop. Discovery keeps the tools listed, so the
# agent's tool list survives a pause.
agent_denials contains "the agent is paused (agent-admin resume)" if {
	agent_principal
	not input.discovery
	agent_data.paused == true
}

# The agent's data does not match the seal: everything is denied, as on
# a pause, until agent-admin writes it again.
agent_denials contains "the agent's data does not match its seal (changed outside agent-admin): every call is denied until agent-admin writes it again (agent-admin seal --agents)" if {
	agent_principal
	not input.discovery
	agent_data
	not data_trusted(agent_name)
}

agent_denials contains "the emergency stop is in effect (agent-admin resume-all)" if {
	agent_principal
	not input.discovery
	data.mcp.agents._control.stopped == true
}

# A call that a permission of the agent data names, but that is no
# permission now, explained (mcp-gateway's unmatched_reasons, R1): its
# task category is not enabled on this host (§6b), it is a diagnostics
# permission that changes the system, or the agent is advisory and the
# permission changes the system.
unmatched_reasons contains sprintf("task category %s is not enabled on this host (%s): agent-admin enable %s", [p.category, enablement_state, p.category]) if {
	some p in unusable_naming
	has_category(p)
	not misdeclared_diagnostics(p)
	not enablement_seal_broken
}

unmatched_reasons contains sprintf("permissions without a task category are not enabled on this host (%s)", [enablement_state]) if {
	some p in unusable_naming
	not has_category(p)
	not enablement_seal_broken
}

# A broken seal says so: the state on file is not the one agent-admin
# signed, and the host is diagnostics-only until it signs one again.
unmatched_reasons contains "the task categories' state does not match its seal (changed outside agent-admin): only diagnostics is usable until agent-admin enable or disable writes it again" if {
	some p in unusable_naming
	not misdeclared_diagnostics(p)
	enablement_seal_broken
}

unmatched_reasons contains "a diagnostics permission that is not read-only is no permission (diagnostics only reads)" if {
	some p in unusable_naming
	misdeclared_diagnostics(p)
}

unmatched_reasons contains "the agent is advisory: it only reads (agent-admin mode)" if {
	agent_principal
	agent_mode == "advisory"
	some p in agent_data.permissions
	permission_enabled(p)
	not p.read_only == true
	names_request(p)
}

# The agent's permissions that name the request but are not enabled.
unusable_naming contains p if {
	agent_principal
	some p in agent_data.permissions
	not permission_enabled(p)
	names_request(p)
}

# A permission names the request: its server and target, as the gateway
# matches them (its arguments aside).
names_request(p) if {
	field := target_field[input.action]
	server_ok(p)
	glob.match(expand_glob(p[field]), null, input.resource.name)
}

# Reviews of autonomous actions (§6a) by the gateway's approvers
# (mcp-gateway's review obligation, its requirement R4): every call a
# bounded category makes without an approval becomes a review item once
# it ran, for whoever may decide the agent's calls on its server (the
# servers' approvers and the agent's owners). An unreviewed item expires
# after a week. An older gateway ignores the rule.
review_wanted if {
	agent_principal
	not input.discovery
	input.action == "tools.call"
	some p in agent_data.permissions
	permission_enabled(p)
	has_category(p)
	not p.read_only == true
	autonomous_now(p)
	names_request(p)
}
