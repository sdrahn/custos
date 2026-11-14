# shellcheck shell=bash
# Task categories and the promotion path (CONTRACT.md §6a): readiness,
# promote, envelope, review, demotion.
# Needs: t1.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section "Task categories"
# A change of the category waits for an approval, which is then declined.
asks_and_deny() {
	local id aid
	id=$(start_activity t1 CHANGE)
	eventually 30 activity_state_is t1 "$id" waiting_approval || return 1
	aid=$(approval_of t1 "$id")
	gw "/v1/approvals/$aid" -X POST -d '{"decision":"deny","scope":"once"}' >/dev/null
	eventually 30 activity_state_is t1 "$id" finished
}
check "the catalog has patch-errata" bash -c "agent-admin categories | jq -e '.\"patch-errata\".title' >/dev/null"
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change, category: patch-errata}' | agent-admin permissions t1
# Readiness judges a call by the permissions at its time (O11), from
# root's record of each change.
check "journal: permissions.changed by root, with the permissions after" eventually 10 journal_has t1 permissions.changed \
	'._UID == "0" and ((._COMM // "agent-admin") | startswith("agent-openshell") | not) and (.AGENT_PERMISSIONS_TO | fromjson | any(.category == "patch-errata" and .tool == "change"))'
check "a category starts supervised: its change waits for an approval" asks_and_deny
# Readiness (§6a) from the agent's records and the gateway's decision.
check "readiness counts the declined proposal, from the gateway's decision" eventually 30 bash -c \
	"agent-admin readiness t1 patch-errata | jq -e '.categories[\"patch-errata\"] | .proposals >= 1 and .declined >= 1 and .override_rate > 0 and .ready == false' >/dev/null"
check "promote says why the category is not ready" bash -c "printf 'windows: [{from: \"00:00\", to: \"23:59\"}]\n' | agent-admin promote t1 patch-errata --envelope - 2>&1 | grep -q 'is not ready (.*proposals of 50'"
# Today, all day (UTC); not in the last minutes of a day.
[ "$(date -u +%H%M)" -ge 2357 ] && sleep 180
today=$(date -u +%a | tr '[:upper:]' '[:lower:]')
other=$(date -u -d tomorrow +%a | tr '[:upper:]' '[:lower:]')
printf 'windows:\n  - {days: [%s], from: "00:00", to: "23:59", tz: UTC}\n' "$today" >"$work/now.yaml"
printf 'windows:\n  - {days: [%s], from: "00:00", to: "23:59", tz: UTC}\n' "$other" >"$work/other.yaml"
check "promote refuses without readiness" not agent-admin promote t1 patch-errata --envelope "$work/now.yaml"
check "promote refuses a rate per day (the gateway counts per second, minute or hour)" not bash -c "{ cat '$work/now.yaml'; echo 'rate: {calls: 1, per: 1d}'; } | agent-admin promote t1 patch-errata --envelope - --despite-readiness --reason test"
check "agent-admin promote t1 patch-errata" agent-admin promote t1 patch-errata --envelope "$work/now.yaml" --despite-readiness --reason "functional test"
check "show: patch-errata is bounded" bash -c "agent-admin show t1 | jq -e '.agents.t1.categories[\"patch-errata\"].mode == \"bounded\"' >/dev/null"
n0=$(count t1 approval.pending)
check "within its window, the category's change runs without an approval" eventually 30 runs_with t1 CHANGE 'changed test'
check "no approval was asked" test "$(count t1 approval.pending)" = "$n0"
check "journal: category.promoted by root, with the reason" eventually 10 journal_has t1 category.promoted '.SYSLOG_IDENTIFIER == "custos-promotion" and .AGENT_CATEGORY == "patch-errata" and .AGENT_MODE_TO == "bounded" and .AGENT_REASON == "functional test"'
# The after-the-fact review (§6a) of what the promoted category did.
check "review lists the change made without an approval" bash -c \
	"agent-admin review t1 patch-errata | jq -e '.categories[\"patch-errata\"].pending | map(select(.tool == \"custos-test__change\")) | length >= 1' >/dev/null"
act=$(agent-admin review t1 patch-errata | jq -r '.categories["patch-errata"].pending[-1] | "\(.activity) \(.call)"')
# The list is the gateway's record of the calls it let through (mcp-result):
# the action has the gateway's decision id, and so has root's review.
check "the action to review is the gateway's record of it (a decision id)" bash -c \
	"agent-admin review t1 patch-errata | jq -e '.categories[\"patch-errata\"].pending[-1].decision_id | length > 0' >/dev/null"
# shellcheck disable=SC2086 # activity and call
check "review: ok keeps the category bounded" bash -c "agent-admin review t1 patch-errata $act ok >/dev/null && agent-admin show t1 | jq -e '.agents.t1.categories[\"patch-errata\"].mode == \"bounded\"' >/dev/null"
# The same review by the gateway's approvers (its review items, R4), where
# the gateway has them: agents.rego marks the autonomous call for review.
if gw /v1/reviews | jq -e 'type == "array"' >/dev/null 2>&1; then
	gw_review() { gw /v1/reviews | jq -r '[.[] | select(.principal.sub == "agent-t1" and .name == "change" and (.verdict | not))][0].id // empty'; }
	check "the gateway made the autonomous change a review item" eventually 10 bash -c "$(declare -f gw gw_review); test -n \"\$(gw_review)\""
	rid=$(gw_review)
	verdict() { gw "/v1/reviews/$1" -X POST -d "$2" -o /dev/null -w '%{http_code}'; }
	check "the gateway refuses \"should not have run\" without a reason" test "$(verdict "$rid" '{"verdict":"incident"}')" = 400
	check "a verdict on the gateway: ok" bash -c "$(declare -f gw verdict); verdict $rid '{\"verdict\":\"ok\",\"via\":\"test\"}' | grep -q '^20'"
	check "the gateway audited it (mcp-review), by root" eventually 10 gw_audit_has '.event == "mcp-review" and .sub == "agent-t1" and .verdict == "ok" and .by == "root"'
	quiet_evaluator
	check "evaluate keeps the category bounded after ok" bash -c \
		"agent-admin evaluate >/dev/null && agent-admin show t1 | jq -e '.agents.t1.categories[\"patch-errata\"].mode == \"bounded\"' >/dev/null"
	# root and the gateway reviewed the same call (one decision id): one review.
	check "readiness counts the call reviewed by root and on the gateway once" bash -c \
		"agent-admin readiness t1 patch-errata | jq -e '.categories[\"patch-errata\"].reviewed == 1' >/dev/null"
	check "a later verdict on the gateway: should not have run" bash -c \
		"$(declare -f gw verdict); verdict $rid '{\"verdict\":\"incident\",\"reason\":\"functional test (gateway)\"}' | grep -q '^20'"
	quiet_evaluator
	check "evaluate demotes it on the gateway's incident" bash -c \
		"agent-admin evaluate | grep -q 'category patch-errata demoted: review by root on the gateway: custos-test__change (review .*) should not have run: functional test (gateway)'"
	check "promote again, for what follows" agent-admin promote t1 patch-errata --envelope "$work/now.yaml" --despite-readiness --reason "functional test"
	# A new promotion starts a new review period: root's review below takes
	# an action of this one.
	check "after the promotion, the change runs without an approval again" eventually 30 runs_with t1 CHANGE 'changed test'
	act=$(agent-admin review t1 patch-errata | jq -r '.categories["patch-errata"].pending[-1] | "\(.activity) \(.call)"')
else
	skip "reviews by the gateway's approvers (this mcp-gateway has no review items)"
fi
# shellcheck disable=SC2086
check "review: incident (should not have run) demotes it" bash -c "agent-admin review t1 patch-errata $act incident --reason 'functional test' >/dev/null && agent-admin show t1 | jq -e '.agents.t1.categories[\"patch-errata\"].mode == \"supervised\"' >/dev/null"
check "journal: category.review, by root, with the verdict" eventually 10 journal_has t1 category.review '.AGENT_VERDICT == "incident" and .AGENT_TOOL == "custos-test__change" and .AGENT_REASON == "functional test"'
check "journal: root's review names the gateway's decision id" journal_has t1 category.review '.AGENT_VERDICT == "incident" and (.AGENT_DECISION_ID | length > 0)'
check "readiness counts the review as an override" bash -c "agent-admin readiness t1 patch-errata | jq -e '.categories[\"patch-errata\"] | .reviewed >= 1 and .review_overrides >= 1' >/dev/null"
# Only root's reviews count. A later verdict on the same action replaces
# an earlier one, so an agent that could write a review would turn root's
# incident into "ok" and lower its override rate: one written into the
# journal as the agent's user, with agent-admin's identifier, is none
# (journald's trusted _UID).
check "journal: the review is root's (_UID 0), not the relay's" journal_has t1 category.review '._UID == "0" and ((._COMM // "agent-admin") | startswith("agent-openshell") | not) and .AGENT_VERDICT == "incident"'
overrides() { agent-admin readiness t1 patch-errata | jq '.categories["patch-errata"].review_overrides'; }
o0=$(overrides)
read -r act_id act_call <<<"$act"
runuser -u agent-t1 -- logger --journald <<EOF
MESSAGE=forged review
SYSTEMD_UNIT=agent@t1.service
SYSLOG_IDENTIFIER=custos-promotion
AGENT_CONTRACT=1
AGENT_NAME=t1
AGENT_EVENT=category.review
AGENT_CATEGORY=patch-errata
AGENT_INVOCATION=$act_id
AGENT_CALL_ID=$act_call
AGENT_VERDICT=ok
AGENT_BY=root
EOF
check "a review the agent's user wrote is in the journal, not as root's" eventually 10 journal_has t1 category.review \
	".AGENT_INVOCATION == \"$act_id\" and .AGENT_VERDICT == \"ok\" and ._UID != \"0\""
check "readiness keeps root's incident: the agent's own review does not replace it" test "$(overrides)" = "$o0" -a "$o0" -ge 1
check "promote with a window of another day" agent-admin promote t1 patch-errata --envelope "$work/other.yaml" --despite-readiness --reason "functional test"
check "outside its window, the change waits for an approval" asks_and_deny
check "agent-admin demote t1 patch-errata" agent-admin demote t1 patch-errata --reason "functional test"
check "demoted: the change waits for an approval" asks_and_deny
check "journal: category.demoted" eventually 10 journal_has t1 category.demoted '.AGENT_CATEGORY == "patch-errata" and .AGENT_MODE_TO == "supervised"'
# The demotion evaluator (§6a), on a rollback health check.
touch /run/custos-test-healthy
printf 'windows:\n  - {days: [%s], from: "00:00", to: "23:59", tz: UTC}\nrollback: {on: [health_check_failed], health_check: "test -e /run/custos-test-healthy"}\n' "$today" >"$work/rollback.yaml"
check "promote with a rollback health check" agent-admin promote t1 patch-errata --envelope "$work/rollback.yaml" --despite-readiness --reason "functional test"
check "promote enabled custos-demote.timer" systemctl is-enabled -q custos-demote.timer
quiet_evaluator
check "evaluate leaves a healthy category bounded" bash -c \
	"agent-admin evaluate >/dev/null && agent-admin show t1 | jq -e '.agents.t1.categories[\"patch-errata\"].mode == \"bounded\"' >/dev/null"
rm -f /run/custos-test-healthy
quiet_evaluator
# The whole output first, then the check: grep -q on a pipe through tee
# would end tee (SIGPIPE) before the notice line reached the file.
check "evaluate demotes it when its health check fails" bash -c \
	"agent-admin evaluate >$work/evaluate.out; grep -q 'category patch-errata demoted: rollback health_check_failed' $work/evaluate.out"
# The approvers hear of it through the gateway's notices (its R3), where
# the gateway takes them.
if gw /v1/notices | jq -e 'type == "array"' >/dev/null 2>&1; then
	check "evaluate told the approvers through the gateway" grep -q 'category patch-errata: approvers notified (notice ' "$work/evaluate.out" ||
		sed 's/^/    | evaluate: /' "$work/evaluate.out"
	check "the gateway lists the notice for t1's approvers" bash -c "$(declare -f gw); gw /v1/notices | jq -e 'any(.[]; .principal.sub == \"agent-t1\" and
		.title == \"t1: patch-errata demoted to supervised\" and .severity == \"warning\" and (.text | contains(\"rollback health_check_failed\")) and
		(.servers == [\"custos-test\"]))' >/dev/null"
	check "the gateway audited the notice (mcp-notice)" eventually 10 gw_audit_has '.event == "mcp-notice"'
else
	skip "the demotion's notice to the approvers (this mcp-gateway takes no notices)"
fi
check "the category is supervised again" bash -c "agent-admin show t1 | jq -e '.agents.t1.categories[\"patch-errata\"].mode == \"supervised\"' >/dev/null"
check "journal: category.demoted by custos-demote, with the reason" eventually 10 journal_has t1 category.demoted \
	'.AGENT_BY == "custos-demote" and (.AGENT_REASON | startswith("rollback health_check_failed"))'
# The rollback condition tool_error from what the gateway saw come back
# (its mcp-result, requirement R2), when the gateway audits outcomes.
if gw_audit_has '.event == "mcp-result"'; then
	printf 'windows:\n  - {days: [%s], from: "00:00", to: "23:59", tz: UTC}\nrollback: {on: [tool_error]}\n' "$today" >"$work/rollback-error.yaml"
	check "promote with rollback on tool_error" agent-admin promote t1 patch-errata --envelope "$work/rollback-error.yaml" --despite-readiness --reason "functional test"
	check "bounded: a change that fails runs without an approval" eventually 30 runs_with t1 "CHANGE FAIL" 'change failed'
	check "the gateway's audit: its mcp-result is a tool_error, with the agent's progress token" eventually 10 gw_audit_has \
		'.event == "mcp-result" and .sub == "agent-t1" and .name == "change" and .outcome == "tool_error" and (.approval_id | not) and (.progress_token | startswith("custos:"))'
	quiet_evaluator
	check "evaluate demotes it on the gateway's tool_error" bash -c \
		"agent-admin evaluate | grep -q 'category patch-errata demoted: rollback tool_error: custos-test__change failed .*(the gateway: tool_error'"
	check "the category is supervised again after the tool error" bash -c "agent-admin show t1 | jq -e '.agents.t1.categories[\"patch-errata\"].mode == \"supervised\"' >/dev/null"
else
	skip "rollback on the gateway's tool_error (this mcp-gateway does not audit call outcomes, mcp-result)"
fi
# The envelope's rate (O12): the gateway's approval_over_rate (its R6) on
# the category's calls without an approval: over it a call waits for an
# approval, scope once; three in a day demote the category.
printf 'windows:\n  - {days: [%s], from: "00:00", to: "23:59", tz: UTC}\nrate: {calls: 1, per: 1h}\n' "$today" >"$work/rate.yaml"
check "promote with a rate of 1 call per hour" agent-admin promote t1 patch-errata --envelope "$work/rate.yaml" --despite-readiness --reason "functional test"
check "the gateway's policy has the rate as its approval_over_rate" eventually 30 bash -c \
	"$(declare -f opa_decision); opa_decision t1 change | jq -e '.result.effect == \"allow\" and .result.obligations.approval_over_rate == [\"1/h\"] and (.result.obligations.rate_limit | not)' >/dev/null"
check "within the rate, the change runs without an approval" eventually 30 runs_with t1 CHANGE 'changed test'
over_id=$(start_activity t1 CHANGE)
check "over the rate, the change waits for an approval" eventually 30 activity_state_is t1 "$over_id" waiting_approval
over_aid=$(approval_of t1 "$over_id")
check "the approval names the rate reached, for this call only" bash -c \
	"$(declare -f gw); gw /v1/approvals/$over_aid | jq -e '.over_rate == \"1/h\" and .scopes == [\"once\"]' >/dev/null"
gw "/v1/approvals/$over_aid" -X POST -d '{"decision":"approve","scope":"once"}' >/dev/null
check "approved, the change over the rate runs" eventually 30 activity_state_is t1 "$over_id" finished
check "the gateway's decision record names the rate" eventually 10 gw_audit_has \
	'.sub == "agent-t1" and .name == "change" and .over_rate == "1/h" and (.event | not)'
check "the gateway's mcp-approval names it too" eventually 10 gw_audit_has '.event == "mcp-approval" and .over_rate == "1/h"'
# An approved call is not counted: the rate is still reached. Approved,
# not declined: declines would raise the override rate, which demotes
# first.
asks_and_approve() {
	local id aid
	id=$(start_activity t1 CHANGE)
	eventually 30 activity_state_is t1 "$id" waiting_approval || return 1
	aid=$(approval_of t1 "$id")
	gw "/v1/approvals/$aid" -X POST -d '{"decision":"approve","scope":"once"}' >/dev/null
	eventually 30 activity_state_is t1 "$id" finished
}
check "over the rate again: an approval, given" asks_and_approve
check "and a third time" asks_and_approve
quiet_evaluator
check "evaluate demotes it after reaching the rate three times within a day" bash -c \
	"agent-admin evaluate | grep -q 'category patch-errata demoted: rate (1 calls per 1h per tool) reached 3 times within a day'"
check "the category is supervised again after reaching its rate" bash -c "agent-admin show t1 | jq -e '.agents.t1.categories[\"patch-errata\"].mode == \"supervised\"' >/dev/null"
check "custos-demote.service runs the evaluator" systemctl start custos-demote.service
systemctl disable --now custos-demote.timer >/dev/null 2>&1
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change}' | agent-admin permissions t1
