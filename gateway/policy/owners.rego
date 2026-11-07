# Owners of Custos agents approve their agents' calls (CONTRACT.md §7,
# O1), added to mcp-gateway's approver rules (package mcp.approvals) as
# agents.rego is to its authorization. Installed as
# /usr/share/mcp-gateway/policy/custos/owners.rego.
#
# An agent's owners are rules in its agent data, which agent-admin owners
# writes (/etc/mcp-gateway/policy/agents/<name>.yaml):
#
#   <name>:
#     owners: ["user:alice", "group:web-team"]
#
# with the forms of the gateway's approver rules: user:, group:, role:,
# remote-user:, remote-group: ("self" would be the agent itself and
# grants nothing here). An owner may decide on the agent's pending calls
# whatever the server's own rules say, and is told of them by mail as
# they are; the server's approvers keep deciding too. An owner also sees
# and may end the agent's sessions (the gateway's Clients tab,
# mcp-gateway-admin clients) and its tasks: the server's approver rules
# alone would ask an approver for every server of the agent's endpoint,
# which for the aggregated one is every server registered.
package mcp.approvals

import rego.v1

# The pending request's principal is an agent: its user agent-<name>
# (or a token's subject agent-<name> from a trusted issuer; the gateway
# denies any other such subject everything, so it has no pending call).
custos_agent := substring(input.request.principal.sub, 6, -1) if startswith(input.request.principal.sub, "agent-")

custos_owners contains rule if {
	some rule in object.get(object.get(data.mcp.agents, custos_agent, {}), "owners", [])
	is_string(rule)
	rule != "self"
}

# An agent never approves (CONTRACT.md §7): an approver whose name is an
# agent's (agent-<name>, a local user or a token's subject) is no owner,
# whatever group or role a rule names. Every agent's user is in
# agent-clients and mcp-users (security review M2).
custos_agent_approver if startswith(object.get(input.approver, "name", ""), "agent-")

custos_owner if {
	not custos_agent_approver
	some rule in custos_owners
	grants(rule, input.request.principal)
}

allow if custos_owner

allow_without_self if custos_owner

notify contains rule if {
	some rule in custos_owners
	startswith(rule, "user:")
}

notify contains rule if {
	some rule in custos_owners
	startswith(rule, "group:")
}

# The agent of a session's or task's subject: its user agent-<name> on
# the unix socket, or the subject agent-<name> from an issuer trusted for
# agents (agents/_http.yaml); an agent-* subject from any other issuer
# is no agent (agents.rego), and nobody owns it.
owned_agent(subject) := substring(subject.sub, 6, -1) if {
	startswith(subject.sub, "agent-")
	subject.transport == "unix"
}

owned_agent(subject) := substring(subject.sub, 6, -1) if {
	startswith(subject.sub, "agent-")
	subject.transport == "http"
	subject.iss in object.get(object.get(data.mcp.agents, "_http", {}), "issuers", [])
}

owner_of(subject) if {
	not custos_agent_approver
	name := owned_agent(subject)
	some rule in object.get(object.get(data.mcp.agents, name, {}), "owners", [])
	is_string(rule)
	rule != "self"
	grants(rule, subject)
}

manage_session if owner_of(input.session)

manage_task if owner_of(input.task)
