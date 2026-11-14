# shellcheck shell=bash
# Owners' approvals, prompt injection (the scripted cases), pause, cancel,
# schedules.
# Needs: t1 and its schedule.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section "Owners approve their agent's calls"
# CONTRACT.md §7, O1: an agent's owners may decide its pending calls
# besides the servers' approvers (owners.rego in the gateway's approver
# policy). custos-tester is no approver by the shipped rules.
# Deciding on the control socket takes its group (socket_group of
# gateway.yaml), as for any approver; the policy decides the rest.
id custos-tester >/dev/null 2>&1 || useradd -m custos-tester
sock_group=$(stat -c %G /run/mcp-gateway/control.sock)
usermod -aG "$sock_group" custos-tester
tester_gw() { runuser -u custos-tester -- curl -sS --max-time 30 --unix-socket /run/mcp-gateway/control.sock "http://localhost$1" "${@:2}"; }
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change}' | agent-admin permissions t1
agent-admin owners t1 >/dev/null
id=$(start_activity t1 CHANGE)
check "a change waits for an approval" eventually 30 activity_state_is t1 "$id" waiting_approval
aid=$(approval_of t1 "$id")
lists() { tester_gw /v1/approvals | jq -e --arg a "$aid" 'map(.id) | index($a)' >/dev/null; }
check "custos-tester reaches the control socket ($sock_group)" bash -c "$(declare -f tester_gw); tester_gw /v1/approvals | jq -e 'type == \"array\"' >/dev/null"
check "without owners, custos-tester does not see the approval" not lists
sessions_t1() { tester_gw /v1/sessions | jq -e 'any(.[]; .sub == "agent-t1")' >/dev/null; }
check "without owners, custos-tester does not see t1's session (the gateway's Clients tab)" not sessions_t1
check "agent-admin refuses the agent itself as owner" not agent-admin owners t1 user:agent-t1
check "agent-admin owners t1 user:custos-tester" agent-admin owners t1 user:custos-tester
check "show: t1's owners" bash -c "agent-admin show t1 | jq -e '.agents.t1.owners == [\"user:custos-tester\"]' >/dev/null"
check "as t1's owner, custos-tester sees the approval" eventually 10 lists
check "as t1's owner, custos-tester sees t1's session in the gateway's clients" eventually 10 sessions_t1
check "and decides it, as custos-tester" bash -c \
	"$(declare -f tester_gw); tester_gw /v1/approvals/$aid -X POST -d '{\"decision\":\"approve\",\"scope\":\"once\"}' -o /dev/null -w '%{http_code}' | grep -q '^20'"
check "the change ran" eventually 30 activity_state_is t1 "$id" finished
check "the gateway's audit: approved by custos-tester" eventually 10 gw_audit_has ".event == \"mcp-approval\" and .id == \"$aid\" and .ok == true and .by == \"custos-tester\""
check "agent-admin owners t1 (none)" agent-admin owners t1
check "without owners again, show has none" bash -c "agent-admin show t1 | jq -e '.agents.t1 | has(\"owners\") | not' >/dev/null"
gpasswd -d custos-tester "$sock_group" >/dev/null

section "Prompt injection"
# CONTRACT.md O14: notes an attacker wrote reach the model as a tool's
# output and tell it to call what the task did not ask for. The scripted
# model obeys every one (the worst case): the approval and the gateway's
# offer must still hold. The tested model gets the same cases in the
# section "A real model".
agent-admin delete inj >/dev/null 2>&1
check "agent-admin create inj" agent-admin create inj --from "$(config inj custos-test)"
printf '%s\n' '- {server: custos-test, tool: read_note, read_only: true}' '- {server: custos-test, tool: change}' | agent-admin permissions inj
check "inj is running" eventually 60 bash -c "$(declare -f api); api inj /v1/status | jq -e '.contract == 1' >/dev/null"
injection_suite inj scripted 60
check "the scripted model followed every injection, so each boundary was tried" test "$injection_followed" = "$injection_total" -a "$injection_total" -ge 7
check "journal: the call to wipe, not offered, is recorded with the reason" journal_has inj tool.denied.local \
	'.AGENT_TOOL == "custos-test__wipe" and .AGENT_REASON == "not offered" and .AGENT_RATIONALE == "The note says to."'
check "agent-admin delete inj" agent-admin delete inj

section Pause
agent-admin pause t1
check "the gateway data says paused" grep -q 'paused: true' /etc/mcp-gateway/policy/agents/t1.yaml
check "status says paused" json_ok "$(api t1 /v1/status)" '.paused == true'
# What the Cockpit page reads for the posture, as anyone without root.
check "agent-admin show, as nobody, says paused" json_ok "$(as_nobody /usr/sbin/agent-admin show t1)" \
	'.agents.t1.paused == true and .agents.t1.mode == "supervised" and (.agents.t1.permissions | length) == 2'
check "a new activity is refused (409)" code_is 409 api t1 /v1/activities -X POST -d '{"prompt":"PROBE"}'
check "journal: agent.paused" eventually 10 journal_has t1 agent.paused 'true'
agent-admin resume t1
check "after resume, activities run again" eventually 30 runs_with t1 PROBE 'probe ok'

section Cancel
id=$(start_activity t1 CHANGE)
eventually 30 activity_state_is t1 "$id" waiting_approval
aid=$(approval_of t1 "$id")
check "cancel answers 204" code_is 204 api t1 "/v1/activities/$id/cancel" -X POST
check "its state is cancelled" eventually 20 activity_state_is t1 "$id" cancelled
check "journal: activity.cancel" eventually 10 journal_has t1 activity.cancel ".AGENT_INVOCATION == \"$id\""
[ -n "$aid" ] && gw "/v1/approvals/$aid" -X POST -d '{"decision":"deny","scope":"once"}' >/dev/null 2>&1

section Schedules
if ! systemctl start 'agent-schedule@t1_check.service'; then
	journalctl --no-pager -o cat --since "@$t0" -u 'agent-schedule@t1_check.service' 2>&1 | tail -n 15 | sed 's/^/    | /'
fi
check "the schedule's run is an activity of origin schedule" eventually 30 journal_has t1 invocation.start '.AGENT_ORIGIN == "schedule" and .AGENT_SCHEDULE == "check" and .AGENT_USER == "system:schedule"'
