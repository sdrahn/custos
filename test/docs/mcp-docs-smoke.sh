#!/bin/sh
# mcp-docs-smoke.sh: check an installed custos-docs with mcp-docs
# (github.com/sdrahn/mcp-docs) installed, as root (CI's openSUSE
# container): its files, and mcp-docs serving the collection custos from
# its collection file as an agent asks.
#
#   test/docs/mcp-docs-smoke.sh [MCP-DOCS]
set -eu

mcpdocs=${1:-mcp-docs}
yaml=/usr/share/mcp-docs/collections.d/custos.yaml
docs=/usr/share/custos/docs
out=$(mktemp)
err=$(mktemp)
trap 'rm -f "$out" "$err"' EXIT
fail() {
	echo "mcp-docs-smoke: $*" >&2
	exit 1
}
call() { # id, tool, arguments
	echo "{\"jsonrpc\":\"2.0\",\"id\":$1,\"method\":\"tools/call\",\"params\":{\"name\":\"$2\",\"arguments\":$3}}"
}

for f in "$yaml" "$docs/CONTRACT.md" "$docs/user-guide/README.md" "$docs/user-guide/12-troubleshooting.md"; do
	rpm -qf "$f" | grep -q '^custos-docs-' || fail "$f is not owned by custos-docs"
done

{
	echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}'
	echo '{"jsonrpc":"2.0","method":"notifications/initialized"}'
	# In the collection custos: mcp-gateway's guide is installed next to
	# it, and for the gateway and Keycloak mcp-docs may rank it first,
	# which is right for an agent that asks about the gateway. The test is
	# that Custos's guide answers its own questions.
	call 2 find_sections '{"query":"connect the gateway to Keycloak for an OpenShell agent","collection":"custos"}'
	call 3 find_sections '{"query":"stop all agents at once","collection":"custos"}'
	call 4 search '{"query":"did not load the change within","collection":"custos"}'
	call 5 read_section '{"doc":"custos/user-guide/06-permissions-and-modes.md#modes"}'
	echo '{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"docs://custos/user-guide/README.md"}}'
	sleep 2
} | timeout 30 "$mcpdocs" >"$out" 2>"$err" || fail "the session failed: $(cat "$err")"

grep -q "serving .*custos=$docs" "$err" || fail "not served: $(cat "$err")"
for a in 'Custos documentation' 'Index: custos/user-guide/README.md' 'agent-admin show' \
	'custos/user-guide/11-openshell.md#keycloak' \
	'custos/user-guide/09-administration.md#emergency-stop' \
	'custos/user-guide/12-troubleshooting.md' \
	'supervised' 'Common questions'; do
	grep -q -- "$a" "$out" || fail "no $a in the answers: $(cat "$out")"
done
echo "mcp-docs-smoke: custos-docs is served ($(find "$docs" -name '*.md' | wc -l) documents)"
