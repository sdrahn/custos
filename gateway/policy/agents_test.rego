package mcp.authz_agents_test

import data.mcp.authz
import rego.v1

pp := {"sub": "agent-patch-pilot", "uid": 990, "groups": ["agent-patch-pilot", "mcp-users"], "transport": "unix", "session_id": "s1"}

call(principal, server, tool, args) := {
	"principal": principal,
	"action": "tools.call",
	"resource": {"server": server, "kind": "tool", "name": tool, "annotations": {"readOnlyHint": true}},
	"args": args,
	"grants": [],
}

agents(mode) := {"patch-pilot": {"mode": mode, "permissions": [
	{"server": "zypp", "tool": "list_*", "read_only": true},
	{"server": "zypp", "tool": "install_patches"},
	{"server": "systemd", "tool": "restart_unit", "autonomous": true, "args": {"unit": "^nginx\\.service$"}},
]}}

effect(mode, server, tool, args) := e if {
	e := authz.decision.effect with input as call(pp, server, tool, args)
		with data.mcp.agents as agents(mode)
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

test_advisory if {
	effect("advisory", "zypp", "list_updates", {}) == "allow"
	effect("advisory", "zypp", "install_patches", {}) == "deny"
	effect("advisory", "systemd", "restart_unit", {"unit": "nginx.service"}) == "deny"
}

test_supervised if {
	effect("supervised", "zypp", "list_updates", {}) == "allow"
	effect("supervised", "zypp", "install_patches", {}) == "ask"
	effect("supervised", "systemd", "restart_unit", {"unit": "nginx.service"}) == "ask"
	effect("supervised", "fs", "read_file", {}) == "deny"
}

test_supervised_asks_out_of_band if {
	d := authz.decision with input as call(pp, "zypp", "install_patches", {})
		with data.mcp.agents as agents("supervised")
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
	d.ask.channel == "oob"
}

test_default_mode_is_supervised if {
	a := {"patch-pilot": {"permissions": [{"server": "zypp", "tool": "install_patches"}]}}
	authz.decision.effect == "ask" with input as call(pp, "zypp", "install_patches", {})
		with data.mcp.agents as a
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

test_bounded if {
	effect("bounded", "systemd", "restart_unit", {"unit": "nginx.service"}) == "allow"
	effect("bounded", "systemd", "restart_unit", {"unit": "sshd.service"}) != "allow"
	effect("bounded", "zypp", "install_patches", {}) == "ask"
}

test_annotations_never_allow if {
	# The call's annotations say read-only; the permission does not.
	effect("advisory", "zypp", "install_patches", {}) == "deny"
}

test_paused_and_stopped if {
	a := {"patch-pilot": object.union(agents("bounded")["patch-pilot"], {"paused": true})}
	authz.decision.effect == "deny" with input as call(pp, "zypp", "list_updates", {})
		with data.mcp.agents as a
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
	authz.decision.effect == "deny" with input as call(pp, "zypp", "list_updates", {})
		with data.mcp.agents as agents("bounded")
		with data.mcp.agents._control as {"stopped": true}
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

test_bound_role_denies if {
	authz.decision.effect == "deny" with input as call(pp, "zypp", "list_updates", {})
		with data.mcp.agents as agents("bounded")
		with data.mcp.rbac.bindings as {"users": {"agent-patch-pilot": ["admin"]}, "groups": {}}
}

test_agent_without_data_denied if {
	authz.decision.effect == "deny" with input as call(pp, "zypp", "list_updates", {})
		with data.mcp.agents as {}
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

test_humans_unaffected if {
	alice := {"sub": "alice", "groups": ["wheel"], "transport": "unix", "session_id": "s1"}
	authz.decision.effect == "allow" with input as call(alice, "zypp", "install_patches", {})
		with data.mcp.agents as agents("advisory")
		with data.mcp.agents._control as {"stopped": true}
}

# Over the HTTPS listener (OpenShell, CONTRACT.md §16a): a token from an
# issuer listed in agents/_http.yaml.
pp_http(iss) := {"sub": "agent-patch-pilot", "iss": iss, "groups": [], "transport": "http", "session_id": "s2"}

http_effect(iss, issuers, mode, tool) := e if {
	a := object.union(agents(mode), {"_http": {"issuers": issuers}})
	e := authz.decision.effect with input as call(pp_http(iss), "zypp", tool, {})
		with data.mcp.agents as a
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

test_http_agent_from_trusted_issuer if {
	http_effect("https://agents.example", ["https://agents.example"], "supervised", "list_updates") == "allow"
	http_effect("https://agents.example", ["https://agents.example"], "supervised", "install_patches") == "ask"
	http_effect("https://agents.example", ["https://agents.example"], "advisory", "install_patches") == "deny"
}

test_http_agent_subject_from_other_issuer_denied if {
	http_effect("https://idp.example", ["https://agents.example"], "bounded", "list_updates") == "deny"

	# None trusted by default.
	http_effect("https://agents.example", [], "bounded", "list_updates") == "deny"
	authz.decision.effect == "deny" with input as call(pp_http("https://agents.example"), "zypp", "list_updates", {})
		with data.mcp.agents as agents("bounded")
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

test_http_agent_subject_from_other_issuer_denied_despite_role if {
	authz.decision.effect == "deny" with input as call(pp_http("https://idp.example"), "zypp", "list_updates", {})
		with data.mcp.agents as {"_http": {"issuers": ["https://agents.example"]}}
		with data.mcp.rbac.bindings as {"users": {"agent-patch-pilot": ["admin"]}, "groups": {}}
}

test_http_agent_stopped_and_paused if {
	a := object.union(agents("bounded"), {"_http": {"issuers": ["https://agents.example"]}, "_control": {"stopped": true}})
	authz.decision.effect == "deny" with input as call(pp_http("https://agents.example"), "zypp", "list_updates", {})
		with data.mcp.agents as a
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

# Task categories (CONTRACT.md §6a).
cat_agent(mode, categories) := {"patch-pilot": {"mode": mode, "categories": categories, "permissions": [
	{"server": "zypp", "tool": "list_*", "read_only": true},
	{"server": "zypp", "tool": "install_patches", "category": "patch-errata"},
	{"server": "systemd", "tool": "restart_unit", "category": "config-lifecycle"},
	{"server": "systemd", "tool": "reload_unit", "autonomous": true},
]}}

# Saturday 2026-11-07 03:00 in Berlin (02:00 UTC), and Saturday 05:00.
sat_0300_berlin := time.parse_rfc3339_ns("2026-11-07T02:00:00Z")

sat_0500_berlin := time.parse_rfc3339_ns("2026-11-07T04:00:00Z")

window := {"windows": [{"days": ["sat"], "from": "02:00", "to": "04:00", "tz": "Europe/Berlin"}]}

promoted := {"patch-errata": {"mode": "bounded", "envelope": window}}

cat_effect(mode, categories, server, tool, now) := e if {
	e := authz.decision.effect with input as call(pp, server, tool, {})
		with data.mcp.agents as cat_agent(mode, categories)
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
		with time.now_ns as now
}

test_category_supervised_by_default if {
	# A category without an entry is supervised, even on a bounded agent.
	cat_effect("bounded", {}, "zypp", "install_patches", sat_0300_berlin) == "ask"
	cat_effect("supervised", {}, "zypp", "install_patches", sat_0300_berlin) == "ask"
}

test_promoted_category_in_its_window if {
	cat_effect("supervised", promoted, "zypp", "install_patches", sat_0300_berlin) == "allow"
}

test_promoted_category_outside_its_window_asks if {
	cat_effect("supervised", promoted, "zypp", "install_patches", sat_0500_berlin) == "ask"

	# Another day at the same time.
	fri := time.parse_rfc3339_ns("2026-11-06T02:00:00Z")
	cat_effect("supervised", promoted, "zypp", "install_patches", fri) == "ask"
}

test_promotion_is_per_category if {
	# Promoting patch-errata leaves config-lifecycle supervised.
	cat_effect("supervised", promoted, "systemd", "restart_unit", sat_0300_berlin) == "ask"
}

test_uncategorized_keeps_the_agent_mode if {
	# Version 1's autonomous flag still applies to a permission without a
	# category, and only there.
	cat_effect("bounded", promoted, "systemd", "reload_unit", sat_0500_berlin) == "allow"
	cat_effect("supervised", promoted, "systemd", "reload_unit", sat_0300_berlin) == "ask"
}

test_advisory_caps_every_category if {
	cat_effect("advisory", promoted, "zypp", "install_patches", sat_0300_berlin) == "deny"
	cat_effect("advisory", promoted, "zypp", "list_updates", sat_0300_berlin) == "allow"
}

test_demoted_category_asks if {
	demoted := {"patch-errata": {"mode": "supervised", "envelope": window}}
	cat_effect("supervised", demoted, "zypp", "install_patches", sat_0300_berlin) == "ask"
}

test_envelope_without_windows_is_always if {
	always := {"patch-errata": {"mode": "bounded", "envelope": {}}}
	cat_effect("supervised", always, "zypp", "install_patches", sat_0500_berlin) == "allow"
}

test_window_without_days_or_tz if {
	utc := {"patch-errata": {"mode": "bounded", "envelope": {"windows": [{"from": "01:30", "to": "02:30"}]}}}
	cat_effect("supervised", utc, "zypp", "install_patches", sat_0300_berlin) == "allow"
	cat_effect("supervised", utc, "zypp", "install_patches", sat_0500_berlin) == "ask"
}

# The envelope's rate (O12): the gateway's approval_over_rate obligation on the
# category's calls without an approval, and only on those.
rated := {"patch-errata": {"mode": "bounded", "envelope": object.union(window, {"rate": {"calls": 20, "per": "1h"}})}}

cat_decision(mode, categories, server, tool, now) := d if {
	d := authz.decision with input as call(pp, server, tool, {})
		with data.mcp.agents as cat_agent(mode, categories)
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
		with time.now_ns as now
}

test_rate_is_the_gateways_approval_over_rate if {
	d := cat_decision("supervised", rated, "zypp", "install_patches", sat_0300_berlin)
	d.effect == "allow"
	d.obligations.approval_over_rate == ["20/h"]

	# Not the hard cap: over the rate the call asks, it is not denied.
	not d.obligations.rate_limit
}

test_rate_not_on_calls_that_need_an_approval if {
	# Outside the window a person decides each call: no rate.
	d := cat_decision("supervised", rated, "zypp", "install_patches", sat_0500_berlin)
	d.effect == "ask"
	not d.obligations.approval_over_rate
}

test_rate_units if {
	every per, want in {"1s": "5/s", "1m": "5/m", "1h": "5/h"} {
		c := {"patch-errata": {"mode": "bounded", "envelope": object.union(window, {"rate": {"calls": 5, "per": per}})}}
		cat_decision("supervised", c, "zypp", "install_patches", sat_0300_berlin).obligations.approval_over_rate == [want]
	}
}

test_invalid_rate_is_supervised if {
	every r in [{"calls": 5, "per": "2h"}, {"calls": 0, "per": "1h"}, {"calls": 1.5, "per": "1h"}, {"calls": "5", "per": "1h"}, {"calls": 5}, {"calls": 5, "per": "1h", "burst": 2}] {
		c := {"patch-errata": {"mode": "bounded", "envelope": object.union(window, {"rate": r})}}
		cat_effect("supervised", c, "zypp", "install_patches", sat_0300_berlin) == "ask"
	}
}

test_window_ends_before_to if {
	at_0400 := time.parse_rfc3339_ns("2026-11-07T03:00:00Z")
	cat_effect("supervised", promoted, "zypp", "install_patches", at_0400) == "ask"
}

test_unknown_category_mode_denies_everything if {
	bad := {"patch-errata": {"mode": "autonomous"}}
	cat_effect("supervised", bad, "zypp", "list_updates", sat_0300_berlin) == "deny"
}

test_category_is_not_passed_to_the_gateway if {
	ps := authz.perms with input as call(pp, "zypp", "install_patches", {})
		with data.mcp.agents as cat_agent("supervised", promoted)
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
		with time.now_ns as sat_0300_berlin
	count(ps) > 0
	every q in ps {
		not q.category
	}
}

# Category enablement (CONTRACT.md §6b).
diag_agents := {"patch-pilot": {"mode": "supervised", "permissions": [
	{"server": "systemd", "tool": "list_units", "read_only": true, "category": "diagnostics"},
	{"server": "systemd", "tool": "get_unit_log", "category": "diagnostics"},
	{"server": "zypp", "tool": "list_updates", "read_only": true},
	{"server": "zypp", "tool": "patch", "category": "patch-errata"},
	{"server": "systemd", "tool": "restart_unit", "category": "config-lifecycle"},
]}}

enabled_effect(enablement, server, tool) := e if {
	e := authz.decision.effect with input as call(pp, server, tool, {})
		with data.mcp.agents as object.union(diag_agents, enablement)
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

# Without enablement data every category is usable, as before.
test_enablement_absent if {
	enabled_effect({}, "systemd", "list_units") == "allow"
	enabled_effect({}, "zypp", "list_updates") == "allow"
	enabled_effect({}, "zypp", "patch") == "ask"
	enabled_effect({}, "systemd", "restart_unit") == "ask"
}

test_diagnostics_only if {
	d := {"_enablement": {"state": "diagnostics-only", "enabled": ["diagnostics"]}}
	enabled_effect(d, "systemd", "list_units") == "allow"

	# Other categories, and permissions without one, are denied.
	enabled_effect(d, "zypp", "patch") == "deny"
	enabled_effect(d, "systemd", "restart_unit") == "deny"
	enabled_effect(d, "zypp", "list_updates") == "deny"
}

# A diagnostics permission that is not read-only is none, in any state.
test_diagnostics_reads_only if {
	enabled_effect({}, "systemd", "get_unit_log") == "deny"
	enabled_effect({"_enablement": {"state": "diagnostics-only"}}, "systemd", "get_unit_log") == "deny"
}

test_categories_enabled if {
	d := {"_enablement": {"state": "categories", "enabled": ["diagnostics", "patch-errata"]}}
	enabled_effect(d, "systemd", "list_units") == "allow"
	enabled_effect(d, "zypp", "list_updates") == "allow"

	# Enabled: supervised, as on a host without enablement data.
	enabled_effect(d, "zypp", "patch") == "ask"
	enabled_effect(d, "systemd", "restart_unit") == "deny"
}

# Every category enabled behaves as no enablement data at all.
test_all_enabled_is_the_broader_install if {
	all := {"_enablement": {"state": "categories", "enabled": ["diagnostics", "patch-errata", "config-lifecycle", "security-response", "compliance-audit"]}}
	every t in [["systemd", "list_units"], ["zypp", "list_updates"], ["zypp", "patch"], ["systemd", "restart_unit"], ["systemd", "get_unit_log"]] {
		enabled_effect(all, t[0], t[1]) == enabled_effect({}, t[0], t[1])
	}
}

# An unknown state enables nothing.
test_enablement_unknown_state if {
	d := {"_enablement": {"state": "everything"}}
	enabled_effect(d, "systemd", "list_units") == "deny"
	enabled_effect(d, "zypp", "list_updates") == "deny"
}

# A promoted category that is disabled is still denied.
test_disabled_bounded_category if {
	a := {"patch-pilot": {
		"mode": "supervised", "permissions": [{"server": "zypp", "tool": "patch", "category": "patch-errata"}],
		"categories": {"patch-errata": {"mode": "bounded", "envelope": {"windows": []}}},
	}}
	authz.decision.effect == "allow" with input as call(pp, "zypp", "patch", {})
		with data.mcp.agents as a
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
	authz.decision.effect == "deny" with input as call(pp, "zypp", "patch", {})
		with data.mcp.agents as object.union(a, {"_enablement": {"state": "diagnostics-only"}})
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

# Discovery hides a disabled category's tools: the model is not offered them.
test_discovery_hides_disabled_categories if {
	disc := object.union(call(pp, "zypp", "patch", {}), {"discovery": true})
	authz.decision.effect != "deny" with input as disc
		with data.mcp.agents as diag_agents
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
	authz.decision.effect == "deny" with input as disc
		with data.mcp.agents as object.union(diag_agents, {"_enablement": {"state": "diagnostics-only"}})
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

# The seal (#109). A test key: the private one signs as agent-admin
# would, the public one is the host's seal; other_seal_key is another
# host's.
seal_jwk := `{"crv":"P-256","d":"fzwgA5ipXkInjL5YbSdY5CrySDvoLG2w1Foo5kjP7kE","kty":"EC","x":"YzczqQqXxemy45f5ORLObSB7m9ciW0oTjlfDvr84Zks","y":"0hLVnpCi_K5jBUdkRMhQsb-3Z8Fw874VJaxmgzPYSYs"}`

seal_pem := "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEYzczqQqXxemy45f5ORLObSB7m9ci\nW0oTjlfDvr84ZkvSEtWekKL8rmMFR2REyFCxv7dnwXDzvhUlrGaDM9hJiw==\n-----END PUBLIC KEY-----\n"

other_seal_key := "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEkreF/wgF7MlBi6q1u3/vxPC5JvNv\nXowOlQ2xIzvPqPfe2fL1wyuoJIWDLNEgWEAdPlsWGvRUQaqq5GUNCQgCGg==\n-----END PUBLIC KEY-----\n"

signed(state, enabled, record) := {"state": state, "enabled": enabled, "record": record, "signature": io.jwt.encode_sign(
	{"alg": "ES256", "typ": "JWT"},
	{"state": state, "enabled": enabled, "record": record},
	json.unmarshal(seal_jwk),
)}

sealed(e) := object.union({"_seal": {"public_key": seal_pem}}, e)

# A signed state counts as it says.
test_sealed_state_signed if {
	d := sealed({"_enablement": signed("categories", ["diagnostics", "patch-errata"], "n-1")})
	enabled_effect(d, "zypp", "patch") == "ask"
	enabled_effect(d, "zypp", "list_updates") == "allow"
	enabled_effect(d, "systemd", "restart_unit") == "deny"
}

# Edited by hand (its state, its list or its record), unsigned, deleted,
# or signed with another key: the host is diagnostics-only.
test_sealed_state_tampered_is_diagnostics_only if {
	s := signed("diagnostics-only", ["diagnostics"], "n-1")
	every e in [
		object.union(s, {"state": "categories", "enabled": ["diagnostics", "patch-errata"]}),
		object.union(signed("categories", ["diagnostics"], "n-1"), {"enabled": ["diagnostics", "patch-errata"]}),
		object.union(signed("categories", ["diagnostics", "patch-errata"], "n-1"), {"record": "n-2"}),
		object.remove(signed("categories", ["diagnostics", "patch-errata"], "n-1"), ["signature"]),
	] {
		d := sealed({"_enablement": e})
		enabled_effect(d, "zypp", "patch") == "deny"
		enabled_effect(d, "zypp", "list_updates") == "deny"
		enabled_effect(d, "systemd", "list_units") == "allow"
	}

	# Deleted: no state at all, but a seal.
	enabled_effect(sealed({}), "zypp", "patch") == "deny"
	enabled_effect(sealed({}), "systemd", "list_units") == "allow"

	# Another host's key.
	other := {"_seal": {"public_key": other_seal_key}, "_enablement": signed("categories", ["diagnostics", "patch-errata"], "n-1")}
	enabled_effect(other, "zypp", "patch") == "deny"
}

# Without a seal the state counts as it is, as before #109.
test_unsealed_state_counts_as_it_is if {
	d := {"_enablement": {"state": "categories", "enabled": ["diagnostics", "patch-errata"]}}
	enabled_effect(d, "zypp", "patch") == "ask"
}

# A broken seal is the reason given, not the category.
test_sealed_state_tampered_reason if {
	e := object.union(signed("diagnostics-only", ["diagnostics"], "n-1"), {"state": "categories", "enabled": ["diagnostics", "patch-errata"]})
	reasons := authz.unmatched_reasons with input as call(pp, "zypp", "patch", {})
		with data.mcp.agents as object.union(diag_agents, sealed({"_enablement": e}))
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
	some r in reasons
	contains(r, "does not match its seal")
	every r2 in reasons {
		not contains(r2, "agent-admin enable patch-errata")
	}
}

# The seal of the agents' data (#158): with agents: true, each agent's
# data and _http.yaml count only signed by the seal's key, over the
# agent's name and its data without the signature.
signed_data(name, d) := object.union(d, {"signature": io.jwt.encode_sign(
	{"alg": "ES256", "typ": "JWT"},
	{"agent": name, "data": d},
	json.unmarshal(seal_jwk),
)})

agents_sealed(a) := object.union(a, {"_seal": {"public_key": seal_pem, "agents": true}})

pp_data := agents("supervised")["patch-pilot"]

data_effect(a, tool) := e if {
	e := authz.decision.effect with input as call(pp, "zypp", tool, {})
		with data.mcp.agents as a
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
}

# Signed, the data counts as it says.
test_agent_data_signed if {
	a := agents_sealed({"patch-pilot": signed_data("patch-pilot", pp_data)})
	data_effect(a, "list_updates") == "allow"
	data_effect(a, "install_patches") == "ask"
}

# Edited by hand, unsigned, another agent's signature, or another host's
# key: the agent is denied everything.
test_agent_data_tampered_denied if {
	s := signed_data("patch-pilot", pp_data)
	every d in [
		object.union(s, {"mode": "bounded"}),
		object.union(s, {"permissions": [{"server": "zypp", "tool": "*"}]}),
		pp_data,
		object.union(pp_data, {"signature": signed_data("triage", pp_data).signature}),
	] {
		data_effect(agents_sealed({"patch-pilot": d}), "list_updates") == "deny"
	}
	other := object.union({"patch-pilot": s}, {"_seal": {"public_key": other_seal_key, "agents": true}})
	data_effect(other, "list_updates") == "deny"
}

# The reason says why.
test_agent_data_tampered_reason if {
	reasons := authz.deny_reasons with input as call(pp, "zypp", "list_updates", {})
		with data.mcp.agents as agents_sealed({"patch-pilot": pp_data})
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
	some r in reasons
	contains(r, "the agent's data does not match its seal")
}

# A seal without agents: true (a host sealed before #158), or no seal:
# unsigned data counts as it is.
test_agent_data_unsealed_counts if {
	data_effect(sealed({"patch-pilot": pp_data}), "list_updates") == "allow"
	data_effect({"patch-pilot": pp_data}, "install_patches") == "ask"
}

# The emergency stop is not sealed: written by hand, it stops a sealed
# host's agents.
test_emergency_stop_not_sealed if {
	a := agents_sealed({"patch-pilot": signed_data("patch-pilot", pp_data), "_control": {"stopped": true}})
	data_effect(a, "list_updates") == "deny"
}

# _http.yaml: signed, its issuers are trusted; edited or unsigned, none
# is, with the reason.
test_http_issuers_sealed if {
	iss := "https://agents.example"
	h := signed_data("_http", {"issuers": [iss]})
	ok := agents_sealed({"patch-pilot": signed_data("patch-pilot", pp_data), "_http": h})
	authz.decision.effect == "allow" with input as call(pp_http(iss), "zypp", "list_updates", {})
		with data.mcp.agents as ok
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
	every bad in [object.union(h, {"issuers": [iss, "https://evil.example"]}), {"issuers": [iss]}] {
		a := agents_sealed({"patch-pilot": signed_data("patch-pilot", pp_data), "_http": bad})
		authz.decision.effect == "deny" with input as call(pp_http(iss), "zypp", "list_updates", {})
			with data.mcp.agents as a
			with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
	}
	reasons := authz.deny_reasons with input as call(pp_http(iss), "zypp", "list_updates", {})
		with data.mcp.agents as agents_sealed({"patch-pilot": signed_data("patch-pilot", pp_data), "_http": {"issuers": [iss]}})
		with data.mcp.rbac.bindings as {"users": {}, "groups": {}}
	some r in reasons
	contains(r, "do not match their seal")
}
