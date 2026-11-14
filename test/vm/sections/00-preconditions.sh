# shellcheck shell=bash
# Preconditions, labels, the documentation package, and the setup every
# later section needs: the fake model and the test MCP server.
# Leaves: custos-fakemodel.service running, custos-test registered.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section Preconditions
for p in mcp-gateway custos custos-tests jq curl; do
	check "package or program $p is installed" bash -c "rpm -q $p >/dev/null 2>&1 || command -v $p >/dev/null"
done
systemctl enable --now mcp-gateway.service >/dev/null 2>&1
check "the gateway is running" eventually 30 test -S /run/mcp-gateway/mcp.sock
check "the agents' policy module is installed for the gateway" test -f /usr/share/mcp-gateway/policy/custos/agents.rego
check "group agent-clients exists" getent group agent-clients

section Labels
for d in /run/agents /var/lib/agents /etc/custos; do
	if enforcing; then
		check "$d has the module's label" grep -qE 'agent_(runtime|var_lib|conf)_t' <<<"$(stat -c %C "$d" 2>/dev/null)"
	else
		skip "$d has the module's label (SELinux is not enforcing)"
	fi
done

section Documentation
if rpm -q custos-docs >/dev/null 2>&1; then
	check "the user guide is installed" test -f /usr/share/custos/docs/user-guide/README.md
	check "the contract is installed beside it" test -f /usr/share/custos/docs/CONTRACT.md
	check "the mcp-docs collection file names the guide" \
		grep -qx 'root: /usr/share/custos/docs' /usr/share/mcp-docs/collections.d/custos.yaml
	if command -v mcp-docs >/dev/null; then
		check "mcp-docs serves the collection custos" bash -c \
			"timeout 10 mcp-docs </dev/null 2>&1 >/dev/null | grep -q 'serving .*custos=/usr/share/custos/docs'"
	else
		skip "mcp-docs serves the collection custos (mcp-docs is not installed)"
	fi
else
	skip "the user guide is installed (custos-docs is not installed)"
fi

section Setup
systemctl stop custos-fakemodel.service >/dev/null 2>&1
systemd-run --unit=custos-fakemodel --property=DynamicUser=yes "$dir/fakemodel" --listen 127.0.0.1:8008 >/dev/null 2>&1
check "the fake model answers" eventually 20 bash -c 'curl -sf -X POST -d "{\"messages\":[]}" http://127.0.0.1:8008/v1/chat/completions | grep -q ok'
cat >/etc/mcp-gateway/servers.d/custos-test.yaml <<EOF
# Custos functional tests (run-tests.sh)
version: 1
name: custos-test
command: ["$dir/testsrv"]
run_as: principal
EOF
check "the gateway takes the test server" eventually 30 servers_have custos-test
