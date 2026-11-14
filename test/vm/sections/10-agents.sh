# shellcheck shell=bash
# Creating an agent, discovery, the sandbox its unit gives it.
# Leaves: the agent t1, with its schedule check.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section "Creating an agent"
f=$(config t1 custos-test 'schedules: [{name: check, on_calendar: "*-*-* 03:00", prompt: "PROBE (scheduled)"}]')
check "agent-admin check accepts the configuration" agent-admin check "$f"
printf 'version: 1\nname: t1\nmodel: {provider: openai, name: m, endpoint: "http://x"}\nlimitz: {}\n' >"$work/bad.yaml"
check "agent-admin check refuses an unknown key" not agent-admin check "$work/bad.yaml"
# As the Cockpit page does: the configuration on stdin, checked without root.
check "agent-admin check - (stdin), as nobody, accepts it" bash -c "$(declare -f as_nobody); as_nobody /usr/sbin/agent-admin check - <'$f'"
check "agent-admin check - refuses an unknown key" not bash -c "agent-admin check - <'$work/bad.yaml'"
agent-admin delete t1 >/dev/null 2>&1
check "agent-admin create t1 --from - (stdin)" bash -c "agent-admin create t1 --from - <'$f'"
check "agent-admin refuses to create t1 twice" not agent-admin create t1 --from "$f"
check "user agent-t1 exists, without a login shell" bash -c 'getent passwd agent-t1 | grep -q nologin'
check "agent-t1 is in mcp-users" in_group agent-t1 mcp-users
check "agent-t1 is in agent-clients" in_group agent-t1 agent-clients
check "the configuration is root:agent-t1 0640" stat_is /etc/custos/agents/t1/agent.yaml root:agent-t1:640
check "the socket unit is active" systemctl is-active -q agent@t1.socket
check "the gateway data says supervised" grep -q "mode: supervised" /etc/mcp-gateway/policy/agents/t1.yaml
check "the gateway data grants nothing" grep -q "permissions: \[\]" /etc/mcp-gateway/policy/agents/t1.yaml
check "the schedule's timer is enabled" systemctl is-enabled -q 'agent-schedule@t1_check.timer'
check "agent-admin list names t1" bash -c 'agent-admin list | grep -qx t1'
# Editing, as the Cockpit page does: the new configuration on stdin.
sed -e 's/^description: .*/description: edited test agent t1/' -e 's/03:00/04:00/' "$f" >"$work/t1-edited.yaml"
check "agent-admin edit t1 --from - (stdin)" bash -c "agent-admin edit t1 --from - <'$work/t1-edited.yaml'"
check "the edited configuration is installed" grep -q 'description: edited test agent t1' /etc/custos/agents/t1/agent.yaml
check "it is still root:agent-t1 0640" stat_is /etc/custos/agents/t1/agent.yaml root:agent-t1:640
check "the schedule's timer has the new calendar" grep -q 'OnCalendar=\*-\*-\* 04:00' /etc/systemd/system/agent-schedule@t1_check.timer.d/schedule.conf
check "agent-admin edit refuses a configuration for another agent" \
	not bash -c "sed 's/^name: t1/name: t9/' '$f' | agent-admin edit t1 --from -"
check "the socket unit is still active" systemctl is-active -q agent@t1.socket
agent-admin edit t1 --from "$f" >/dev/null

section Discovery
st=$(api t1 /v1/status)
if json_ok "$st" '.contract == 1 and .name == "t1"'; then
	pass "GET /v1/status answers contract 1 (starts the agent)"
else
	fail "GET /v1/status answers contract 1 (starts the agent)"
	no_agent=1
	systemctl status --no-pager agent@t1.service 2>&1 | tail -n 15 | sed 's/^/    | /'
fi
check "status names the units" json_ok "$st" '.units.service == "agent@t1.service"'
check "status says the lifecycle is systemd" json_ok "$st" '.lifecycle.kind == "systemd" and .lifecycle.socket == "agent@t1.socket"'
check "the socket is root:agent-clients 0660" stat_is /run/agents/t1/api.sock root:agent-clients:660
check "the agent runs as agent-t1" user_is "$(main_pid t1)" agent-t1
if enforcing; then
	check "the agent runs in agent_t" grep -q ':agent_t:' <<<"$(label "$(main_pid t1)")"
	grep -q ':agent_t:' <<<"$(label "$(main_pid t1)")" ||
		echo "    | domain $(label "$(main_pid t1)"), program $(stat -c %C /usr/libexec/custos/agent-launcher)"
	check "the launcher is labelled agent_exec_t" grep -q ':agent_exec_t:' <<<"$(stat -c %C /usr/libexec/custos/agent-launcher)"
else
	skip "the agent runs in agent_t (SELinux is not enforcing)"
fi
check "journal: agent.start" eventually 10 journal_has t1 agent.start 'true'

section Sandbox
pid=$(main_pid t1)
# InaccessiblePaths= leaves the name and takes the access away.
check "the agent cannot reach the gateway's control socket" not nsenter -t "$pid" -m \
	curl -s --max-time 5 --unix-socket /run/mcp-gateway/control.sock -o /dev/null http://localhost/v1/whoami
check "the agent sees no agents' sockets" test -z "$(nsenter -t "$pid" -m ls -A /run/agents 2>/dev/null)"
check "the agent cannot write /etc" not nsenter -t "$pid" -m -S "$(id -u agent-t1)" -G "$(id -g agent-t1)" touch /etc/custos-test
for p in NoNewPrivileges=yes ProtectSystem=strict ProtectHome=yes PrivateTmp=yes; do
	check "unit property $p" grep -qx "$p" <<<"$(systemctl show agent@t1.service -p "${p%%=*}")"
done
# The network (security review M4): the unit denies every address but the
# host's own and the model endpoint's (agent-admin fence). A process in
# the agent's cgroup, where systemd's IP filter applies, reaches the fake
# model on localhost and not the host's other address.
in_cgroup() { local cg; cg=$(systemctl show -p ControlGroup --value agent@t1.service)
	bash -c "echo \$\$ >'/sys/fs/cgroup$cg/cgroup.procs' && exec \"\$@\"" sh "$@"; }
check "unit: every address denied but the host's (IPAddressDeny=any)" bash -c \
	"systemctl show -p IPAddressDeny --value agent@t1.service | grep -q '0.0.0.0/0' && systemctl show -p IPAddressAllow --value agent@t1.service | grep -q '127.0.0.0/8'"
check "the agent's cgroup reaches its model on localhost" in_cgroup curl -s -o /dev/null --max-time 5 http://127.0.0.1:8008/v1/models
host_ip=$(ip -4 route get 192.0.2.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
# A host without a route (the air-gapped gate) has no such address: one
# for the test, outside 127.0.0.0/8, which the unit does not allow.
probe_ip=
if [ -z "$host_ip" ]; then
	probe_ip=192.0.2.99
	ip addr add "$probe_ip/32" dev lo && host_ip=$probe_ip
fi
systemd-run --unit=custos-fence-probe --property=DynamicUser=yes "$dir/fakemodel" --listen "$host_ip:18009" >/dev/null 2>&1
check "a listener on the host's network address ($host_ip:18009) answers (the control)" eventually 10 \
	curl -s -o /dev/null --max-time 5 "http://$host_ip:18009/v1/models"
check "the agent's cgroup does not reach it" bash -c \
	"$(declare -f in_cgroup); ! in_cgroup curl -s -o /dev/null --max-time 5 http://$host_ip:18009/v1/models"
systemctl stop custos-fence-probe.service >/dev/null 2>&1
[ -z "$probe_ip" ] || ip addr del "$probe_ip/32" dev lo
# A model elsewhere: its address is added for the start.
printf 'version: 1\nname: tf\nmodel: {provider: openai, name: m, endpoint: "http://192.0.2.10:8000/v1"}\ngateway: {servers: [custos-test]}\n' >"$work/tf.yaml"
agent-admin create tf --from "$work/tf.yaml" >/dev/null
systemctl start agent@tf.service
check "a model elsewhere: agent-admin fence allows its address for the start" eventually 20 bash -c \
	"systemctl show -p IPAddressAllow --value agent@tf.service | grep -qw '192.0.2.10'"
agent-admin delete tf >/dev/null 2>&1
