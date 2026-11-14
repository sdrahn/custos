# shellcheck shell=bash
# Removing agents.
# Needs: t1, t2; they are gone afterwards.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section Removing
check "agent-admin delete t2" agent-admin delete t2
check "agent-admin delete t1" agent-admin delete t1
check "t1's configuration is gone" not test -e /etc/custos/agents/t1
check "t1's gateway data is gone" not test -e /etc/mcp-gateway/policy/agents/t1.yaml
check "t1's state is gone" not test -e /var/lib/agents/t1
check "t1's socket directory is gone (the Cockpit page lists /run/agents)" not test -e /run/agents/t1
check "t1's schedule is gone" not systemctl is-enabled -q 'agent-schedule@t1_check.timer'
