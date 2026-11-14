# shellcheck shell=bash
# The emergency stop, and SELinux over everything so far.
# Needs: t1 with its schedule, t2.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section "Emergency stop"
check "agent-admin stop-all" agent-admin stop-all
check "the gateway's emergency stop flag is set" grep -q 'stopped: true' /etc/mcp-gateway/policy/agents/_control.yaml
check "agent-admin show, as nobody, reports the emergency stop" json_ok "$(as_nobody /usr/sbin/agent-admin show)" \
	'.control.stopped == true and (.agents | has("t1")) and (.agents | has("t2"))'
for u in agent@t1.socket agent@t1.service agent@t2.socket agent@t2.service agent-schedule@t1_check.timer; do
	check "$u is stopped" not systemctl is-active -q "$u"
done
systemctl start agent@t1.socket
check "an agent started by hand is still denied by the gateway" not runs_with t1 PROBE 'probe ok'
check "agent-admin release-all" agent-admin release-all
# Root's records of both: readiness counts an approved call whose
# activity the stop ended as an incident (O11).
for e in agents.stopped agents.released; do
	check "journal: $e, root's" eventually 10 bash -c \
		"journalctl -o json --no-pager --since '@$t0' _UID=0 SYSLOG_IDENTIFIER=custos-promotion AGENT_EVENT=$e | jq -e 'select(.AGENT_BY | length > 0)' >/dev/null"
done
check "sockets are back" systemctl is-active -q agent@t1.socket agent@t2.socket
check "and calls are allowed again" eventually 30 runs_with t1 PROBE 'probe ok'
# What an administrator disabled stays so: release-all starts what is
# enabled, and edit enables only new schedules.
systemctl disable --now agent-schedule@t1_check.timer >/dev/null 2>&1
check "agent-admin stop-all (again)" agent-admin stop-all
check "agent-admin release-all (again)" agent-admin release-all
check "release-all leaves a disabled schedule's timer stopped" not systemctl is-active -q agent-schedule@t1_check.timer
check "and the enabled sockets are back" systemctl is-active -q agent@t1.socket agent@t2.socket
check "agent-admin edit t1 leaves the disabled schedule disabled" bash -c \
	"cp /etc/custos/agents/t1/agent.yaml '$work/t1-same.yaml' && agent-admin edit t1 --from '$work/t1-same.yaml' && ! systemctl is-enabled -q agent-schedule@t1_check.timer && ! systemctl is-active -q agent-schedule@t1_check.timer"
systemctl enable --now agent-schedule@t1_check.timer >/dev/null 2>&1

section SELinux
if enforcing; then
	# Every denial that names our domains or types, our programs or our
	# paths, whatever the other side's type (a directory labelled before
	# the module was loaded has a generic one).
	avc=$(ausearch -m AVC,USER_AVC,SELINUX_ERR -ts "$(date -d "@$t0" '+%m/%d/%Y %H:%M:%S')" 2>/dev/null |
		grep -E 'agent_|mcpsrv_agents_t|comm="agent-|name="(agents|api\.sock|t1|t2)"|/run/agents|/var/lib/agents|custos')
	check "no SELinux denials for the agents" test -z "$avc"
	[ -n "$avc" ] && sed 's/^/    | /' <<<"$avc" | head -30
else
	skip "no SELinux denials (SELinux is not enforcing)"
fi
