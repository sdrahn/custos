# shellcheck shell=bash
# Diagnostic playbooks (CONTRACT.md §14c) and event triggers.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section "Diagnostic playbooks"
# CONTRACT.md §14c: published playbooks; an activity runs one through the
# gateway, refused when the gateway does not offer a step's tool.
check "agent-admin playbooks lists the shipped set, none broken" bash -c \
	"agent-admin playbooks | jq -e '(keys | length) == 11 and .\"failed-units\".title and ([.[] | select(.error)] | length == 0)' >/dev/null"
if [ -x /usr/libexec/mcp-servers/mcp-server-exec ]; then
	check "the shipped diagnostic commands pass mcp-server-exec --check" \
		/usr/libexec/mcp-servers/mcp-server-exec --check --commands /usr/share/custos/templates/exec.d-diagnostics.yaml
else
	skip "the shipped diagnostic commands pass mcp-server-exec --check (mcp-gateway-exec-server is not installed)"
fi
mkdir -p /etc/custos/playbooks
cat >/etc/custos/playbooks/probe-check.yaml <<'EOF'
probe-check:
  title: PROBE REPORT check
  description: The test server's probe.
  steps:
    - {tool: custos-test__probe, why: "The probe."}
  look_for: probe ok.
EOF
cat >/etc/custos/playbooks/change-check.yaml <<'EOF'
change-check:
  title: Change check
  steps:
    - {tool: custos-test__change, why: "Not offered to pb."}
  look_for: nothing.
EOF
agent-admin delete pb >/dev/null 2>&1
check "agent-admin create pb, with a schedule that runs a playbook" agent-admin create pb --from \
	"$(config pb custos-test 'schedules: [{name: probe, on_calendar: "*-*-* 03:30", playbook: probe-check}]')"
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' | agent-admin permissions pb
check "GET /v1/playbooks: pb can run probe-check, not change-check (its tool is not offered)" bash -c \
	"$(declare -f api); api pb /v1/playbooks | jq -e '.playbooks | .\"probe-check\".runnable and (.\"change-check\" | (.runnable | not) and .missing == [\"custos-test__change\"])' >/dev/null"
pb_run() { api pb /v1/activities -X POST -d "$(jq -nc --arg p "$1" '{playbook: $p, wait: true}')"; }
check "an activity runs the playbook: its probe is called" eventually 30 bash -c \
	"$(declare -f api pb_run); pb_run probe-check | jq -e '.playbook == \"probe-check\" and .state == \"finished\" and (.calls | map(.tool) | index(\"custos-test__probe\"))' >/dev/null"
check "journal: invocation.start names the playbook" eventually 10 journal_has pb invocation.start '.AGENT_PLAYBOOK == "probe-check"'
# The host makes the steps' calls itself, through the gateway, before the
# model's first turn: the probe is called once, as step 1, with the step's
# why as its rationale.
check "the host made the step's call: the probe once, as playbook step 1" bash -c \
	"$(declare -f api); api pb /v1/activities | jq -e '[.activities[] | select(.playbook == \"probe-check\")][0].calls | map(select(.tool == \"custos-test__probe\")) |
		length == 1 and .[0].playbook_step == 1 and (.[0].rationale | startswith(\"Playbook probe-check, step 1 of 1\"))' >/dev/null"
check "journal: tool.call of the step has PLAYBOOK_STEP" eventually 10 journal_has pb tool.call '.AGENT_TOOL == "custos-test__probe" and .AGENT_PLAYBOOK_STEP == "1"'
# The diagnosis report (§4a): the host verifies each quote against the
# output of the tool it names; feedback is recorded outside the agent.
pb_act=$(api pb /v1/activities | jq -r '[.activities[] | select(.playbook == "probe-check" and .report)][0].id // empty')
check "the activity has the playbook's diagnosis report" test -n "$pb_act"
check "the host verified the quote that is in the probe's output, and only that one" bash -c \
	"$(declare -f api); api pb /v1/activities/$pb_act | jq -e '.report | .kind == \"diagnosis\" and .playbook == \"probe-check\" and .hypotheses[0].confidence == \"high\" and
		(.evidence[0] | .verified and (.call_id | length > 0)) and (.evidence[1].verified | not)' >/dev/null"
check "journal: diagnosis.report with the verified evidence" eventually 10 journal_has pb diagnosis.report \
	".AGENT_INVOCATION == \"$pb_act\" and (.AGENT_REPORT | fromjson | .evidence[0].verified)"
check "agent-admin feedback on the report" agent-admin feedback pb "$pb_act" --hypothesis h1=confirmed --plan useful --executed --comment "functional test"
check "journal: diagnosis.feedback by root" eventually 10 bash -c \
	"journalctl -o json --no-pager --since '@$t0' SYSLOG_IDENTIFIER=custos-promotion AGENT_EVENT=diagnosis.feedback AGENT_NAME=pb | jq -e 'select(.AGENT_INVOCATION == \"$pb_act\" and .AGENT_BY == \"root\" and (.AGENT_FEEDBACK | fromjson | .hypotheses.h1 == \"confirmed\"))' >/dev/null"
check "agent-admin feedback refuses a hypothesis the report does not have" not agent-admin feedback pb "$pb_act" --hypothesis h9=confirmed
check "readiness shows the diagnostics history: the report, its verified quote, root's feedback" eventually 10 bash -c \
	"agent-admin readiness pb | jq -e '.diagnostics | .reports >= 1 and .verified_quotes >= 1 and .with_feedback >= 1 and .confirmed >= 1 and .plans_executed >= 1' >/dev/null"
check "a playbook whose tool the gateway does not offer is refused (422)" code_is 422 api pb /v1/activities -X POST -d '{"playbook":"change-check"}'
check "an unknown playbook is refused (404)" code_is 404 api pb /v1/activities -X POST -d '{"playbook":"no-such"}'
n0=$(journal pb invocation.start | jq -s 'map(select(.AGENT_ORIGIN == "schedule")) | length')
check "the schedule runs its playbook" bash -c "agent-admin trigger pb_probe"
check "journal: the scheduled activity ran the playbook" eventually 30 bash -c \
	"[ \$(journalctl -o json --no-pager --since '@$t0' AGENT_NAME=pb AGENT_EVENT=invocation.start | jq -s 'map(select(.AGENT_ORIGIN == \"schedule\" and .AGENT_PLAYBOOK == \"probe-check\")) | length') -gt $n0 ]"
# A step's arguments go to the host's call through the gateway; {unit} is
# the unit the request names, and a unit is only a unit name.
cat >/etc/custos/playbooks/unit-check.yaml <<'EOF'
unit-check:
  title: Unit check
  steps:
    - {tool: custos-test__probe, why: "The probe."}
    - {tool: custos-test__read_note, why: "The unit's note.", args: {name: "{unit}"}}
  look_for: anything.
EOF
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: read_note, read_only: true}' | agent-admin permissions pb
unit_sha=$(printf '%s' '{"name":"web.service"}' | sha256sum | cut -d' ' -f1)
check "a playbook step with the request's unit: the host calls read_note as step 2" eventually 30 bash -c \
	"$(declare -f api); api pb /v1/activities -X POST -d '{\"playbook\":\"unit-check\",\"unit\":\"web.service\",\"wait\":true}' |
		jq -e '.calls | map(select(.tool == \"custos-test__read_note\" and .playbook_step == 2)) | length == 1' >/dev/null"
check "journal: the step's call has the unit in its arguments (ARGS_SHA256)" eventually 10 journal_has pb tool.call \
	".AGENT_TOOL == \"custos-test__read_note\" and .AGENT_PLAYBOOK_STEP == \"2\" and .AGENT_ARGS_SHA256 == \"$unit_sha\""
# The report nudge (#155): that run's model answered in prose, without a
# report, so the host asked it once for one, in the same activity.
check "a playbook run that ends in prose is asked once for its report (report_nudged)" bash -c \
	"$(declare -f api); api pb /v1/activities | jq -e '[.activities[] | select(.playbook == \"unit-check\")][0] | .report_nudged == true and (.report | not)' >/dev/null"
check "journal: its model.call says the host asked for the report (REPORT_NUDGE)" eventually 10 journal_has pb model.call '.AGENT_REPORT_NUDGE == "1"'
check "a unit that is not a unit name is refused (400)" code_is 400 api pb /v1/activities -X POST -d '{"playbook":"unit-check","unit":"../web"}'
check "agent-admin delete pb" agent-admin delete pb
rm -f /etc/custos/playbooks/probe-check.yaml /etc/custos/playbooks/change-check.yaml /etc/custos/playbooks/unit-check.yaml

section "Events"
# CONTRACT.md §14, §15: an event starts an activity when a unit it names
# fails. agent-admin puts an OnFailure= drop-in on the unit; systemd starts
# agent-event@<agent>_<event>_<unit>.service, which starts the activity as
# root with origin event, damped per unit.
printf '%s\n' '[Unit]' 'Description=Custos functional test: a unit that fails' \
	'[Service]' 'Type=oneshot' 'ExecStart=/bin/false' >/etc/systemd/system/evtest-fail.service
systemctl daemon-reload
agent-admin delete ev >/dev/null 2>&1
check "agent-admin create ev, with an event on evtest-fail.service" agent-admin create ev --from \
	"$(config ev custos-test 'events: [{name: down, on_unit_failure: [evtest-fail.service], prompt: "The unit {unit} failed.", min_interval_seconds: 3600}]')"
check "the unit has the event's OnFailure= drop-in" bash -c \
	"systemctl show -p OnFailure --value evtest-fail.service | grep -qw 'agent-event@ev_down_evtest-fail.service.service'"
check "agent-admin check refuses an event on Custos's own units" not agent-admin check \
	"$(config ev2 custos-test 'events: [{name: loop, on_unit_failure: [agent-event@x.service], prompt: x}]')"
systemctl start evtest-fail.service >/dev/null 2>&1
check "the unit's failure starts ev's activity, origin event" eventually 30 journal_has ev invocation.start \
	'.AGENT_ORIGIN == "event" and .AGENT_ORIGIN_BY == "unit-failure:evtest-fail.service" and .AGENT_ORIGIN_EVENT == "down"'
check "the activity ends" eventually 30 bash -c \
	"$(declare -f api); api ev /v1/activities | jq -e '[.activities[] | select(.origin.kind == \"event\" and .state == \"finished\")] | length == 1' >/dev/null"
check "when it last started is kept in /run/custos/events, root's alone" stat_is /run/custos/events/ev_down_evtest-fail.service root:root:600
# Damped: a second failure within the event's interval starts nothing.
systemctl start evtest-fail.service >/dev/null 2>&1
check "a second failure within the interval is skipped and recorded (event.skipped)" eventually 30 journal_has ev event.skipped \
	'.AGENT_ORIGIN_BY == "unit-failure:evtest-fail.service" and (.AGENT_REASON | contains("minimum interval"))'
check "and starts no second activity" bash -c \
	"[ \$(journalctl -o json --no-pager --since '@$t0' AGENT_NAME=ev AGENT_EVENT=invocation.start | jq -s 'map(select(.AGENT_ORIGIN == \"event\")) | length') -eq 1 ]"
# Edit without the event: its drop-in goes, the unit no longer starts it.
check "agent-admin edit ev without the event" agent-admin edit ev --from "$(config ev custos-test)"
check "the drop-in is gone" not test -e /etc/systemd/system/evtest-fail.service.d/50-custos-event-ev_down.conf
check "the unit no longer names the event" bash -c \
	"! systemctl show -p OnFailure --value evtest-fail.service | grep -q agent-event@"
check "agent-admin delete ev" agent-admin delete ev
rm -f /etc/systemd/system/evtest-fail.service
systemctl reset-failed evtest-fail.service >/dev/null 2>&1
systemctl daemon-reload
