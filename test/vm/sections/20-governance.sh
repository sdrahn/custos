# shellcheck shell=bash
# The gateway governs: supervised by default, approvals, autonomy modes.
# Needs: t1.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section "Supervised by default"
a=$(run_activity t1 PROBE)
last_answer=$a
check "without permissions, the gateway shows the agent no tools" summary_has "$a" "not offered: custos-test__probe"
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change}' | agent-admin permissions t1
check "with probe read-only, PROBE runs without an approval" eventually 30 runs_with t1 PROBE 'probe ok'
check "journal: tool.result ok for custos-test__probe" eventually 10 journal_has t1 tool.result '.AGENT_TOOL == "custos-test__probe" and .AGENT_OUTCOME == "ok"'
check "journal: model.call says how long the call took" eventually 10 journal_has t1 model.call '(.AGENT_DURATION_MS // "" | test("^[0-9]+$"))'
# model.max_output_tokens: the model server is asked for at most that
# much (max_tokens), and an answer cut off there is recorded as such.
f=$(config tmo custos-test)
sed -i 's|endpoint: "http://127.0.0.1:8008/v1"}|endpoint: "http://127.0.0.1:8008/v1", max_output_tokens: 64}|' "$f"
agent-admin delete tmo >/dev/null 2>&1
agent-admin create tmo --from "$f" >/dev/null
check "model.max_output_tokens reaches the model server, which cuts the answer off there" eventually 30 runs_with tmo "WRITE ON" 'I keep writing'
check "journal: model.call records the cut-off (CUT_OFF)" eventually 10 journal_has tmo model.call '.AGENT_CUT_OFF == "max_output_tokens"'
check "journal: an answer not cut off has no CUT_OFF" journal_has t1 model.call '.AGENT_CUT_OFF == null'
# The model's thinking (model.reasoning_effort): Ollama returns it apart
# from the answer, and reasoning_effort: none turns it off.
check "a model's thinking is not the answer" eventually 30 runs_with tmo THINK 'thought about it'
check "journal: model.call counts the thinking (THOUGHT_BYTES)" eventually 10 journal_has tmo model.call '(.AGENT_THOUGHT_BYTES // "0" | tonumber) > 0'
sed -i 's|max_output_tokens: 64}|max_output_tokens: 64, reasoning_effort: none}|' "$f"
check "agent-admin edit tmo: reasoning_effort none" agent-admin edit tmo --from "$f"
check "model.reasoning_effort none reaches the model server, which does not think" eventually 30 runs_with tmo THINK 'thinking off'
agent-admin delete tmo >/dev/null 2>&1
check "journal: arguments are a digest only" journal_has t1 tool.call '.AGENT_ARGS_SHA256 != null'
# Ended activities outlive the launcher (its idle exit): the next one,
# started by the socket, lists them.
kept_id=$(start_activity t1 PROBE)
eventually 30 activity_state_is t1 "$kept_id" finished
systemctl stop agent@t1.service
check "an ended activity is listed after the agent's service stopped and started again" eventually 30 activity_state_is t1 "$kept_id" finished
check "kept in the agent's state directory, the agent's alone" stat_is /var/lib/agents/t1/activities.json agent-t1:agent-t1:600
# The model's last text in full (§4 response): in the activity, not in
# the list, and kept across the restart too.
check "GET /v1/activities/{id} has the response, the list only the summary" bash -c \
	"$(declare -f api); api t1 /v1/activities/$kept_id | jq -e '.response | contains(\"probe ok\")' >/dev/null &&
		api t1 /v1/activities | jq -e '[.activities[] | select(.response)] | length == 0' >/dev/null"

for decision in approve deny; do
	section "Approval: $decision"
	id=$(start_activity t1 CHANGE)
	check "the activity waits for an approval" eventually 30 activity_state_is t1 "$id" waiting_approval
	aid=$(approval_of t1 "$id")
	check "its call carries the gateway's approval id" test -n "$aid"
	check "the gateway lists the approval for agent-t1" json_ok "$(gw /v1/approvals)" ".[] | select(.id == \"$aid\" and .principal.sub == \"agent-t1\")"
	check "journal: approval.pending with the id" eventually 10 journal_has t1 approval.pending ".AGENT_APPROVAL_ID == \"$aid\""
	check "journal: the call's rationale" journal_has t1 tool.call '.AGENT_RATIONALE == "Writing the test marker; no other impact."'
	# The agent's account of the call, with the approval on the gateway's
	# side too (mcp-gateway's proposal, its R5; from 1.15).
	if gateway_at_least 1.15; then
		check "the gateway shows the agent's proposal with the approval, as the client's claim" json_ok "$(gw /v1/approvals)" \
			".[] | select(.id == \"$aid\") | .proposal.client_supplied == true and .proposal.summary == \"Writing the test marker; no other impact.\" and
			 .proposal.context.agent == \"t1\" and .proposal.context.activity == \"$id\""
	fi
	check "the call is still waiting before the decision" json_ok "$(activity t1 "$id")" '.calls[-1].state == "waiting_approval"'
	gw "/v1/approvals/$aid" -X POST -d "{\"decision\":\"$decision\",\"scope\":\"once\"}" >/dev/null
	check "the activity finishes" eventually 30 activity_state_is t1 "$id" finished
	want=$([ "$decision" = approve ] && echo approved || echo declined)
	check "its call is $want" json_ok "$(activity t1 "$id")" ".calls[-1].approval == \"$want\""
	check "journal: approval.resolved $want" eventually 10 journal_has t1 approval.resolved ".AGENT_APPROVAL_ID == \"$aid\" and .AGENT_OUTCOME == \"$want\""
	# The history of the action (§10) as the Cockpit page's Audit tab joins
	# it: the gateway's decision record and the result, by approval id.
	ok=$([ "$decision" = approve ] && echo true || echo false)
	check "the gateway's audit: mcp-approval for $aid, $want" eventually 10 \
		gw_audit_has ".event == \"mcp-approval\" and .id == \"$aid\" and .principal == \"agent-t1\" and .ok == $ok and .by != null"
	check "journal: tool.result names approval $aid" eventually 10 journal_has t1 tool.result ".AGENT_APPROVAL_ID == \"$aid\""
	if gateway_at_least 1.15; then
		check "the gateway's audit ties the decision to the proposal shown (its digest)" gw_audit_has \
			".event == \"mcp-approval\" and .id == \"$aid\" and (.proposal_digest | startswith(\"sha256:\"))"
		check "the gateway dropped none of the agent's proposals" not gw_audit_has '.event == "mcp-proposal-dropped"'
	fi
	if [ "$decision" = approve ]; then
		check "the change ran" summary_has "$(activity t1 "$id")" 'changed test'
	else
		check "the change did not run" not summary_has "$(activity t1 "$id")" 'changed test'
	fi
done

section "Autonomy modes"
agent-admin mode t1 advisory
check "advisory: the gateway hides the change" eventually 30 runs_with t1 CHANGE 'not offered: custos-test__change'
check "advisory: reading still works" runs_with t1 PROBE 'probe ok'
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change, autonomous: true}' | agent-admin permissions t1
agent-admin mode t1 bounded
n0=$(count t1 approval.pending)
check "bounded: an autonomous change runs without an approval" eventually 30 runs_with t1 CHANGE 'changed test'
check "bounded: no approval was asked" test "$(count t1 approval.pending)" = "$n0"
check "the gateway audited the call as agent-t1" eventually 10 gw_audit_has '.sub == "agent-t1" and .name == "change" and .effect == "allow"'
agent-admin mode t1 supervised
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change}' | agent-admin permissions t1
