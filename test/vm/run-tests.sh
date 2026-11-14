#!/bin/bash
# Functional tests of Custos on a real SLES 16 or Leap 16 system
# with mcp-gateway: agents created with agent-admin, run by systemd,
# governed by the gateway (supervised by default, approvals, autonomy
# modes, pause, emergency stop), schedules, AG-UI, delegation, the
# journal records, the sandbox and SELinux. Package custos-tests
# installs it as /usr/libexec/custos/tests/run-tests.sh for
# openQA and other test systems. Next to it:
#   lib.sh     the framework (check, section, results) and shared helpers
#   sections/  the checks, one file per group of sections, run in the
#              order of their names
#   fakemodel  a scripted OpenAI-compatible model server (test/vm/fakemodel)
#   testsrv    the MCP server custos-test (test/vm/testsrv)
#
# Usage: run-tests.sh --disposable [--results DIR] [--model URL NAME]
#
# It runs as root on a system that is there for testing: it creates
# agents (t1, t2) and a user (custos-tester), registers MCP servers
# with the gateway, triggers the emergency stop, and does not undo all
# of it. --disposable says that this is fine.
#   --results DIR  writes results.tap (TAP) and results.xml (JUnit) there
#   --model URL NAME  also runs an agent on a real model: an
#                  OpenAI-compatible server at URL (such as
#                  http://127.0.0.1:11434/v1) serving NAME. The air-gapped
#                  gate (test/airgap) gives it a local model on a system
#                  without network.
# Every check prints PASS, FAIL or SKIP; the script exits 1 if any failed,
# 2 on wrong usage.
set -u
dir=$(cd "$(dirname "$0")" && pwd)
failed=0
disposable= results= model_url= model_name=
usage() {
	echo "usage: $0 --disposable [--results DIR] [--model URL NAME]" >&2
	exit 2
}
while [ $# -gt 0 ]; do
	case $1 in
	--disposable) disposable=1 ;;
	--results) results=${2:?}; shift ;;
	--model) model_url=${2:?}; model_name=${3:?}; shift 2 ;;
	*) usage ;;
	esac
	shift
done
if [ -z "$disposable" ]; then
	echo "$0: changes this system for good (agents, users, gateway servers, an emergency stop);" >&2
	echo "run it on a system that is there for testing, with --disposable" >&2
	exit 2
fi
[ "$(id -u)" = 0 ] || { echo "$0: runs as root" >&2; exit 2; }
work=/root/custos-test
mkdir -p "$work"
t0=$(date +%s)

# The framework and the helpers.
. "$dir/lib.sh"

# The sections, in the order of their names: a later one uses what an
# earlier one left (the agents t1 and t2, the test server), so they run
# as one test and not by themselves.
for f in "$dir"/sections/[0-9]*.sh; do
	# shellcheck source=/dev/null
	. "$f"
done

rm -f /etc/mcp-gateway/servers.d/custos-test.yaml /etc/mcp-gateway/servers.d/agents.yaml
systemctl stop custos-fakemodel.service >/dev/null 2>&1

# --- results --------------------------------------------------------------------

if [ -n "$results" ]; then
	write_results "$results"
	ausearch -m AVC,USER_AVC,SELINUX_ERR -ts "$(date -d "@$t0" '+%m/%d/%Y %H:%M:%S')" >"$results/avc.txt" 2>&1
	journalctl --no-pager --since "@$t0" -o short-precise >"$results/journal.txt" 2>&1
fi
n_fail=$(printf '%s\n' "${results_list[@]}" | awk -F'\t' '$2 == "fail"' | wc -l)
n_skip=$(printf '%s\n' "${results_list[@]}" | awk -F'\t' '$2 == "skip"' | wc -l)
printf '\n%d checks, %d failed, %d skipped\n' "${#results_list[@]}" "$n_fail" "$n_skip"
exit "$failed"
