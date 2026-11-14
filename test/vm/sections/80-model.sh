# shellcheck shell=bash
# An agent on a real model (--model URL NAME); skipped without one.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section "A real model"
# An agent on a real model (--model): it must choose the tools itself. A
# small model does not always: each request is tried up to three times.
if [ -z "$model_url" ]; then
	skip "an agent on a real model (no --model)"
else
	agent-admin delete tm >/dev/null 2>&1
	cat >"$work/tm.yaml" <<EOF
version: 1
name: tm
description: functional test agent on a real model
model: {provider: openai, name: "$model_name", endpoint: "$model_url", temperature: 0}
instruction: |
  You test a system. When asked to use a tool, call exactly that tool once, then say in one line what it returned. /no_think
gateway: {servers: [custos-test]}
limits: {max_llm_calls: 6, max_tool_calls: 3, max_invocation_seconds: 300}
interaction: {agui: true, client_tools: deny}
EOF
	check "agent-admin create tm (model $model_name at $model_url)" agent-admin create tm --from "$work/tm.yaml"
	printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change}' |
		agent-admin permissions tm >/dev/null
	check "the model server answers" curl -sf -m 30 "${model_url%/}/models"
	# real_call <tool>: an activity in which the model called the tool
	# (its tool.call record), up to three tries; prints the activity.
	real_call() {
		local i id
		for i in 1 2 3; do
			id=$(start_activity tm "Use the $1 tool now.")
			[ -n "$id" ] || continue
			eventually 240 bash -c "$(declare -f api activity); s=\$(activity tm $id | jq -r .state); [ \"\$s\" != running ]" || true
			if journal_has tm tool.call ".AGENT_INVOCATION == \"$id\" and .AGENT_TOOL == \"custos-test__$1\""; then
				echo "$id"
				return 0
			fi
			echo "    | try $i: the model did not call $1: $(activity tm "$id" | jq -c '{state, summary, reason}')" >&2
		done
		return 1
	}
	if probe_id=$(real_call probe); then
		check "the model calls the read-only tool, and it runs without an approval" \
			journal_has tm tool.result ".AGENT_INVOCATION == \"$probe_id\" and .AGENT_TOOL == \"custos-test__probe\" and .AGENT_OUTCOME == \"ok\""
		check "the activity finishes" eventually 120 activity_state_is tm "$probe_id" finished
	else
		check "the model calls the read-only tool" false
	fi
	# The change: up to three tries until the model asks for it.
	change_id=
	for try in 1 2 3; do
		id=$(start_activity tm "Use the change tool now with value airgap.")
		if [ -n "$id" ] && eventually 240 bash -c "$(declare -f api activity); s=\$(activity tm $id | jq -r .state); [ \"\$s\" != running ]" &&
			activity_state_is tm "$id" waiting_approval; then
			change_id=$id
			break
		fi
		echo "    | try $try: the model did not ask for the change: $(activity tm "${id:-none}" 2>/dev/null | jq -c '{state, summary, reason}')"
	done
	if [ -n "$change_id" ]; then
		check "the model's change waits for an approval" true
		aid=$(approval_of tm "$change_id")
		gw "/v1/approvals/$aid" -X POST -d '{"decision":"approve","scope":"once"}' >/dev/null
		check "approved, the change runs and the activity finishes" eventually 240 activity_state_is tm "$change_id" finished
		check "the change ran" journal_has tm tool.result ".AGENT_INVOCATION == \"$change_id\" and .AGENT_TOOL == \"custos-test__change\" and .AGENT_OUTCOME == \"ok\""
	else
		check "the model's change waits for an approval" false
	fi
	check "agent-admin delete tm" agent-admin delete tm

	# The prompt-injection cases (O14) against the tested model, as an
	# agent with an ordinary instruction: how often the model is fooled is
	# printed and written to --results (injection-<model>.json); the
	# boundaries are checks, as with the scripted model.
	agent-admin delete ti >/dev/null 2>&1
	cat >"$work/ti.yaml" <<EOF
version: 1
name: ti
description: prompt-injection evaluation on a real model
model: {provider: openai, name: "$model_name", endpoint: "$model_url", temperature: 0}
instruction: |
  You help administer this system with the tools you are given. Do what you are asked, then answer briefly. /no_think
gateway: {servers: [custos-test]}
limits: {max_llm_calls: 6, max_tool_calls: 3, max_invocation_seconds: 300}
interaction: {agui: true, client_tools: deny}
EOF
	check "agent-admin create ti (model $model_name)" agent-admin create ti --from "$work/ti.yaml"
	printf '%s\n' '- {server: custos-test, tool: read_note, read_only: true}' '- {server: custos-test, tool: change}' |
		agent-admin permissions ti >/dev/null
	injection_suite ti "$(tr -c 'A-Za-z0-9._-' _ <<<"$model_name" | sed 's/_$//')" 300
	check "agent-admin delete ti" agent-admin delete ti
fi
