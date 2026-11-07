package mcp.approvals_owners_test

import data.mcp.approvals
import rego.v1

req := {"principal": {"sub": "agent-patch-pilot", "uid": 981}, "server": "zypp", "name": "patch", "action": "tools.call"}

other := {"principal": {"sub": "agent-triage", "uid": 982}, "server": "zypp", "name": "patch", "action": "tools.call"}

alice := {"name": "alice", "uid": 1001, "groups": ["users"]}

bob := {"name": "bob", "uid": 1002, "groups": ["users", "web-team"]}

carol := {"name": "carol", "uid": 1003, "groups": ["users"]}

# The servers' own rules name only the admins.
rbac := {"approvers": {"default": ["role:admin"]}, "bindings": {"users": {}, "groups": {"wheel": ["admin"]}}}

agents := {"patch-pilot": {"mode": "supervised", "permissions": [], "owners": ["user:alice", "group:web-team", "self"]}}

allowed(approver, request) if {
	approvals.allow with input as {"approver": approver, "request": request}
		with data.mcp.rbac as rbac
		with data.mcp.agents as agents
}

test_owners_approve_their_agent if {
	allowed(alice, req)
	allowed(bob, req)
	not allowed(carol, req)
}

test_owners_approve_only_their_agent if {
	not allowed(alice, other)
	not allowed(bob, other)
}

# Not by "self": the agent's own uid grants nothing.
test_self_is_no_owner if {
	not allowed({"name": "agent-patch-pilot", "uid": 981, "groups": []}, req)
}

# An owner decides without a password over the control socket: the rule
# is not "self".
test_owner_without_self if {
	approvals.allow_without_self with input as {"approver": alice, "request": req}
		with data.mcp.rbac as rbac
		with data.mcp.agents as agents
}

test_owners_notified if {
	n := approvals.notify with input as {"request": req}
		with data.mcp.rbac as rbac
		with data.mcp.agents as agents
	"user:alice" in n
	"group:web-team" in n
}

# Without owners, the servers' rules alone, as before.
test_no_owners if {
	not approvals.allow with input as {"approver": alice, "request": req}
		with data.mcp.rbac as rbac
		with data.mcp.agents as {"patch-pilot": {"mode": "supervised", "permissions": []}}
	approvals.allow with input as {"approver": {"name": "dave", "uid": 1004, "groups": ["wheel"]}, "request": req}
		with data.mcp.rbac as rbac
		with data.mcp.agents as agents
}

# Owners see and may end their agent's sessions and tasks (the gateway's
# Clients tab), though the servers' rules name only the admins and an
# agent's session spans every server of the aggregated endpoint.
session(sub, transport, iss) := {
	"sub": sub, "uid": 981, "transport": transport, "iss": iss, "endpoint": "all",
	"servers": ["zypp", "systemd", "fs"], "client": {"name": "custos/patch-pilot"},
}

may(rule, approver, key, target) if {
	approvals[rule] with input as {"approver": approver, key: target}
		with data.mcp.rbac as rbac
		with data.mcp.agents as object.union(agents, {"_http": {"issuers": ["https://agents.example"]}})
}

test_owners_manage_their_agents_sessions if {
	s := session("agent-patch-pilot", "unix", "")
	may("manage_session", alice, "session", s)
	may("manage_session", bob, "session", s)
	not may("manage_session", carol, "session", s)

	# Another agent's, a person's, and an agent-* subject from an issuer not
	# trusted for agents: not theirs.
	not may("manage_session", alice, "session", session("agent-triage", "unix", ""))
	not may("manage_session", alice, "session", session("patch-pilot", "unix", ""))
	not may("manage_session", alice, "session", session("agent-patch-pilot", "http", "https://idp.example"))

	# Over HTTPS from the agents' issuer (OpenShell): theirs.
	may("manage_session", alice, "session", session("agent-patch-pilot", "http", "https://agents.example"))
}

test_owners_manage_their_agents_tasks if {
	t := {"server": "zypp", "tool": "patch", "sub": "agent-patch-pilot", "uid": 981, "transport": "unix"}
	may("manage_task", alice, "task", t)
	not may("manage_task", carol, "task", t)
	not may("manage_task", alice, "task", object.union(t, {"sub": "agent-triage"}))
}

# The agent itself is no owner of its session ("self" grants nothing).
test_agent_does_not_manage_its_session_as_owner if {
	not may("manage_session", {"name": "agent-patch-pilot", "uid": 981, "groups": []}, "session", session("agent-patch-pilot", "unix", ""))
}

# An agent never approves as an owner, even when a rule names a group it
# is in (every agent's user is in agent-clients and mcp-users): its own
# calls, another agent's, or as a remote approver named agent-*
# (security review M2).
test_agent_is_never_an_owner if {
	groupy := {"patch-pilot": {"mode": "supervised", "permissions": [], "owners": ["group:agent-clients", "group:mcp-users", "role:admin", "remote-user:agent-triage"]}}
	rb := {"approvers": {"default": []}, "bindings": {"users": {"agent-patch-pilot": ["admin"], "agent-triage": ["admin"]}, "groups": {}}}
	some approver in [
		{"name": "agent-patch-pilot", "uid": 981, "groups": ["agent-patch-pilot", "mcp-users", "agent-clients"]},
		{"name": "agent-triage", "uid": 982, "groups": ["agent-triage", "mcp-users", "agent-clients"]},
		{"name": "agent-triage", "groups": [], "remote": true},
	]
	not approvals.allow with input as {"approver": approver, "request": req}
		with data.mcp.rbac as rb
		with data.mcp.agents as groupy
	not approvals.allow_without_self with input as {"approver": approver, "request": req}
		with data.mcp.rbac as rb
		with data.mcp.agents as groupy
	not approvals.manage_session with input as {"approver": approver, "session": {"sub": "agent-patch-pilot", "uid": 981, "transport": "unix", "servers": ["zypp"]}}
		with data.mcp.rbac as rb
		with data.mcp.agents as groupy
}

# A person in such a group still is.
test_person_in_agents_group_is_owner if {
	groupy := {"patch-pilot": {"mode": "supervised", "permissions": [], "owners": ["group:web-team"]}}
	approvals.allow with input as {"approver": bob, "request": req}
		with data.mcp.rbac as rbac
		with data.mcp.agents as groupy
}
