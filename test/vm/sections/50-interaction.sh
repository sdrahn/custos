# shellcheck shell=bash
# AG-UI, access to the socket, a second agent host, delegation.
# Needs: t1. Leaves: the agent t2, and the delegation server registered.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section AG-UI
out=$(api t1 /agui -N -w '\nHTTP %{http_code}\n' -X POST -d '{"threadId":"custos-test-1","messages":[{"role":"user","content":"PROBE"}],"tools":[{"name":"evil","parameters":{"type":"object"}}]}')
agui_ok=1
for ev in RUN_STARTED STATE_SNAPSHOT TOOL_CALL_START TOOL_CALL_RESULT TEXT_MESSAGE_CONTENT RUN_FINISHED; do
	check "the stream has $ev" grep -q "\"type\":\"$ev\"" <<<"$out" || agui_ok=
done
if [ -z "$agui_ok" ]; then
	# What the stream had instead: its events, and a RUN_ERROR's message.
	echo "    | stream: $(sed -n 's/^data: //p' <<<"$out" | jq -rs 'map(.type) | join(",")' 2>/dev/null)"
	# Not a stream at all (an HTTP error's body), as it came.
	grep -q '^data: ' <<<"$out" || echo "    | answer: $(head -c 400 <<<"$out")"
	sed -n 's/^data: //p' <<<"$out" | jq -c 'select(.type == "RUN_ERROR")' 2>/dev/null | sed 's/^/    | /'
	journalctl --since -60s --no-pager -o cat -u agent@t1.service 2>/dev/null | tail -n 20 | sed 's/^/    | t1: /'
fi
check "a client tool not allowed is journaled" eventually 10 journal_has t1 tool.denied.local '.AGENT_TOOL == "evil"'

section "Access to the socket"
id custos-tester >/dev/null 2>&1 || useradd -m custos-tester
gpasswd -d custos-tester agent-clients >/dev/null 2>&1
check "a user outside agent-clients cannot connect" not as_tester -f http://agent/v1/status -o /dev/null
usermod -aG agent-clients custos-tester
check "a member of agent-clients reads the status" as_tester -f http://agent/v1/status -o /dev/null
check "but not another user's sessions (403)" code_is 403 as_tester http://agent/apps/t1/users/root/sessions
check "and cannot pause the agent (403)" code_is 403 as_tester -X POST http://agent/v1/pause
check "ADK's builder endpoint is not reachable (404)" code_is 404 api t1 /builder/save -X POST

section "A second host (the contract alone)"
# CONTRACT.md Part I, #112: a host that is not Custos's launcher, built on
# the standard library from the contract alone, serves the control API
# and writes the records; the control plane's tools find and use it as
# they do the launcher. Its activities are scripted: WAIT runs until
# cancelled.
mock() { local caller=$1 p=$2; shift 2; runuser -u "$caller" -- curl -s --max-time 30 --unix-socket /run/agents/mock/api.sock "http://agent$p" "$@"; }
systemctl stop custos-second-host-mock.service >/dev/null 2>&1
check "the second host runs (systemd-run)" systemd-run --quiet --unit=custos-second-host-mock.service \
	"$dir/secondhost" -name mock -service custos-second-host-mock.service
check "its socket is the contract's: /run/agents/mock/api.sock, root:agent-clients 660" eventually 10 \
	stat_is /run/agents/mock/api.sock root:agent-clients:660
check "a member of agent-clients reads its status: contract 1, its implementation, its unit" bash -c \
	"$(declare -f mock); mock custos-tester /v1/status | jq -e '.contract == 1 and .implementation == \"custos-test/second-host\" and
		.lifecycle.kind == \"systemd\" and .lifecycle.service == \"custos-second-host-mock.service\"' >/dev/null"
check "nobody else reaches it" not as_nobody curl -sf --max-time 5 --unix-socket /run/agents/mock/api.sock http://agent/v1/status
m=$(mock custos-tester /v1/activities -X POST -d '{"prompt":"hello","wait":true}')
check "custos-tester's activity: origin api, by custos-tester (SO_PEERCRED)" json_ok "$m" \
	'.state == "finished" and .origin.kind == "api" and .origin.by == "custos-tester" and .summary == "echo: hello"'
check "journal: invocation.start of the second host, with the contract's fields" eventually 10 journal_has mock invocation.start \
	'.AGENT_CONTRACT == "1" and .AGENT_ORIGIN == "api" and .AGENT_ORIGIN_BY == "custos-tester" and .SYSLOG_IDENTIFIER == "agent-mock"'
w=$(mock custos-tester /v1/activities -X POST -d '{"prompt":"WAIT until cancelled"}' | jq -r .id)
check "a running activity is listed first" bash -c \
	"$(declare -f mock); mock custos-tester /v1/activities | jq -e '.activities[0] | .id == \"$w\" and .state == \"running\"' >/dev/null"
check "its user cancels it (204)" code_is 204 mock custos-tester "/v1/activities/$w/cancel" -X POST
check "cancelled" bash -c "$(declare -f mock); mock custos-tester /v1/activities/$w | jq -e '.state == \"cancelled\"' >/dev/null"
check "a supervisor cannot pause it (403)" code_is 403 mock custos-tester /v1/pause -X POST
check "root pauses it" code_is 204 api mock /v1/pause -X POST
check "paused, it refuses a new activity (409)" code_is 409 mock custos-tester /v1/activities -X POST -d '{"prompt":"x"}'
check "root resumes it" code_is 204 api mock /v1/resume -X POST
check "journal: agent.paused by root" eventually 10 journal_has mock agent.paused '.AGENT_BY == "root"'
check "journal: agent.resumed by root" eventually 10 journal_has mock agent.resumed '.AGENT_BY == "root"'
systemctl stop custos-second-host-mock.service >/dev/null 2>&1
rm -rf /run/agents/mock

section Delegation
cp /usr/share/custos/gateway/servers.d/agents.yaml /etc/mcp-gateway/servers.d/agents.yaml
eventually 30 servers_have agents
agent-admin delete t2 >/dev/null 2>&1
check "agent-admin create t2" agent-admin create t2 --from "$(config t2 agents)"
printf '%s\n' "- {server: agents, tool: delegate, read_only: true, args: {agent: '^t1\$'}}" | agent-admin permissions t2
d=$(run_activity t2 "DELEGATE t1 PROBE")
check "t2's delegation returns t1's answer" summary_has "$d" "probe ok"
did=$(jq -r .id <<<"$d")
check "t1 recorded a delegated activity from agent-t2 with t2's activity as parent" eventually 10 journal_has t1 invocation.start ".AGENT_ORIGIN == \"delegation\" and .AGENT_ORIGIN_BY == \"agent-t2\" and .AGENT_PARENT_AGENT == \"t2\" and .AGENT_PARENT_ACTIVITY == \"$did\""
check "the gateway audited agents__delegate by agent-t2" eventually 10 gw_audit_has '.sub == "agent-t2" and .server == "agents" and .name == "delegate" and .effect == "allow"'
d=$(run_activity t2 "DELEGATE t3 PROBE")
check "delegating to an agent outside the permission is denied" not summary_has "$d" 'probe ok'
check "the gateway audited the denial" eventually 10 gw_audit_has '.sub == "agent-t2" and .name == "delegate" and .effect == "deny"'
