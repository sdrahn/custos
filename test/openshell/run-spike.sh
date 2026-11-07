#!/bin/bash
# The OpenShell backend (CONTRACT.md §3a, §16a) with a real NVIDIA
# OpenShell: an agent created with agent-admin runs in an OpenShell
# sandbox, reaches mcp-gateway's HTTPS listener on the host with an OAuth
# access token that only OpenShell's supervisor holds, and is supervised
# through its relayed socket. CI runs it on a GitHub runner with Docker
# (job openshell); it needs root, systemd, Docker, the openshell CLI with
# a local gateway, opa, the binaries from `make build build-tests`, and an
# mcp-gateway checkout built with `make build` (MCPGW_SRC), and the user
# whose local OpenShell gateway it is (OPENSHELL_GATEWAY_USER).
#
# Usage: MCPGW_SRC=DIR OPENSHELL_GATEWAY_USER=USER test/openshell/run-spike.sh --disposable [--gateway-host name|alias] [--results DIR]
#        OPENSHELL_GATEWAY_USER=USER test/openshell/run-spike.sh --system --disposable [--gateway-host name|alias] [--results DIR]
#
# --gateway-host: the name the agent reaches the gateway by (#131). name
# (the default): a host name of its own, custos-gw.test, which a local
# dnsmasq resolves to the host's network address, as a host's DNS name
# does in a deployment with an official certificate (needs dnsmasq). alias: host.openshell.internal,
# the sandbox's name for the host's loopback.
#
# --system: on a system with the packages installed (custos, custos-selinux,
# custos-tests, mcp-gateway), e.g. the Leap 16 VM with SELinux enforcing
# (test/vm/run-vm.sh with SUITE=openshell): the installed programs, the
# system's OPA (mcp-opa.service) and the installed mcp-gateway, no build.
# The container runtime is Docker if it runs, else OPENSHELL_GATEWAY_USER's
# rootless Podman (OpenShell's RPM default).
#
# mcp-gateway runs in its development mode (no systemd, no SELinux; the
# exec supervisor) with its own policy, custos's agents module
# and the agents' data from agent-admin, as installed. Its HTTPS listener
# trusts test/vm/testidp; agents/_http.yaml lists that issuer for agents.
# agent-sb is an OAuth client there: OpenShell's gateway mints its tokens
# (subject agent-sb, no local-user claim, 90 s) with the client
# credentials grant and renews them while the agent runs.
set -u
here=$(cd "$(dirname "$0")/../.." && pwd)
disposable= results= system= gateway_host=name
while [ $# -gt 0 ]; do
	case $1 in
	--disposable) disposable=1 ;;
	--results) results=${2:?}; shift ;;
	--system) system=1 ;;
	--gateway-host) gateway_host=${2:?}; shift ;;
	*) echo "usage: $0 [--system] --disposable [--gateway-host name|alias] [--results DIR]" >&2; exit 2 ;;
	esac
	shift
done
[ -n "$disposable" ] || { echo "$0: installs agents, units and users on this system; use --disposable" >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo "$0: runs as root" >&2; exit 2; }
case $gateway_host in name | alias) ;; *) echo "$0: --gateway-host name or alias" >&2; exit 2 ;; esac
work=$(mktemp -d)
t0=$(date +%s)
failed=0
results_list=()
current_section=start
pass() { echo "PASS: $*"; results_list+=("$current_section"$'\t'pass$'\t'"$*"); }
fail() { echo "FAIL: $*"; failed=1; results_list+=("$current_section"$'\t'fail$'\t'"$*"); }
skip() { echo "SKIP: $*"; results_list+=("$current_section"$'\t'skip$'\t'"$*"); }
check() { local d=$1; shift; if "$@"; then pass "$d"; else fail "$d"; return 1; fi; }
section() { printf '\n=== %s\n' "$*"; current_section=$*; }
eventually() { local deadline=$((SECONDS + $1)); shift; until "$@"; do [ "$SECONDS" -ge "$deadline" ] && return 1; sleep 1; done; }
not() { ! "$@"; }
json_ok() { jq -e "$2" >/dev/null 2>&1 <<<"$1"; }
api() { local p=$1; shift; curl -sS --max-time 180 --unix-socket /run/agents/sb/api.sock "http://agent$p" "$@"; }
# gw <path> [curl args...]: the gateway's control API.
gw() { local p=$1; shift; curl -sS --max-time 30 --unix-socket "$gw_ctl" "http://localhost$p" "$@"; }
# gw_audit_has <jq condition>: the gateway's audit records since the start.
gw_audit_has() { journalctl -u "$gw_unit" -o cat --no-pager --since "@$t0" | grep '"audit":true' | jq -e "select($1)" >/dev/null 2>&1; }
activity() { api "/v1/activities/$1"; }
# On a failed check of the sandbox: its phase, the forwards, and what the
# relay unit logged (agent-openshell-start's refusals among them).
sandbox_state() {
	echo "    | sandbox: $(openshell -g openshell sandbox get agent-sb -o json 2>&1 | grep -oE '"phase": *"[A-Za-z]*"' | tr '\n' ' ')"
	openshell -g openshell forward list 2>&1 | tail -n 5 | sed 's/^/    | forward: /'
	journalctl -u agent-openshell@sb.service --since -5min --no-pager -o cat 2>&1 | tail -n 25 | sed 's/^/    | unit: /'
}
run_activity() { api /v1/activities -X POST -d "$(jq -nc --arg p "$1" '{prompt: $p, wait: true}')"; }
summary_has() { grep -qF -- "$2" <<<"$(jq -r .summary <<<"$1")"; }
journal_has() { journalctl -o json --no-pager --since "@$t0" AGENT_NAME=sb "AGENT_EVENT=$1" 2>/dev/null | jq -e "select($2)" >/dev/null 2>&1; }
osh() { openshell -g openshell "$@"; }

section Preconditions
check "openshell is installed" command -v openshell
check "the local OpenShell gateway answers" eventually 60 osh status
osh_user=${OPENSHELL_GATEWAY_USER:-}
osh_home=$(getent passwd "$osh_user" | cut -d: -f6)
osh_uid=$(id -u "$osh_user" 2>/dev/null)
export osh_user osh_home osh_uid
# The container runtime OpenShell's driver uses: Docker, or the gateway
# user's rootless Podman.
if docker info >/dev/null 2>&1; then
	runtime=docker
	ctr() { docker "$@"; }
else
	runtime=podman
	# A clean environment: root's XDG_CONFIG_HOME would point podman at
	# root's configuration.
	ctr() { (cd / && setpriv --reuid="$osh_uid" --regid="$(id -g "$osh_user")" --init-groups -- \
		env -i HOME="$osh_home" USER="$osh_user" LOGNAME="$osh_user" PATH=/usr/sbin:/usr/bin:/sbin:/bin \
		XDG_RUNTIME_DIR="/run/user/$osh_uid" podman "$@"); }
fi
ctr_ok() { ctr "$@" >"$work/ctr.out" 2>&1 || { tail -n 8 "$work/ctr.out" | sed 's/^/    | /'; return 1; }; }
check "the container runtime runs ($runtime)" ctr_ok info
gw_ctl=$work/gw/control.sock gw_unit=osh-mcp-gateway.service
if [ -n "$system" ]; then
	# The system's mcp-gateway (confined, systemd supervisor) gets the
	# HTTPS listener (a gateway.d drop-in); its control socket is the system's.
	gw_ctl=/run/mcp-gateway/control.sock gw_unit=mcp-gateway.service
	gw_bin=/usr/bin/mcp-gateway
	gw_policy=/usr/share/mcp-gateway/policy
	check "the packages are installed" rpm -q custos custos-tests mcp-gateway
	# As the functional tests do; mcp-gateway.service brings mcp-opa.service.
	check "the system's mcp-gateway and OPA run" systemctl enable --now mcp-opa.service mcp-gateway.service
	check "SELinux is enforcing" bash -c '[ "$(getenforce)" = Enforcing ]'
else
	gw_bin=${MCPGW_SRC:-}/bin/mcp-gateway
	gw_policy=${MCPGW_SRC:-}/policy
	check "an mcp-gateway checkout is built (MCPGW_SRC)" test -x "$gw_bin" -a -d "$gw_policy/mcp"
	check "opa is installed" command -v opa
fi

section Install
if [ -z "$system" ]; then
	# What the package installs (packaging/suse), into this system.
	make -s -C "$here" install install-tests DESTDIR= PREFIX=/usr SYSCONFDIR=/etc UNITDIR=/etc/systemd/system \
		SYSUSERSDIR=/usr/lib/sysusers.d TMPFILESDIR=/usr/lib/tmpfiles.d SBINDIR=/usr/sbin >/dev/null
	getent group mcp-users >/dev/null || groupadd -r mcp-users
	systemd-tmpfiles --create /usr/lib/tmpfiles.d/custos.conf
	systemctl daemon-reload
fi
check "agent-admin is installed" command -v agent-admin
check "the relay is installed" test -x /usr/libexec/custos/agent-openshell-relay
check "its log source is installed" test -x /usr/libexec/custos/agent-openshell-logs
check "its sandbox starter is installed" test -x /usr/libexec/custos/agent-openshell-start

section "Model on the host"
systemd-run --unit=osh-fakemodel /usr/libexec/custos/tests/fakemodel --listen 0.0.0.0:8008 >/dev/null

section "mcp-gateway on the host (HTTPS)"
# The name the agent reaches the gateway by, the certificate's name and
# the gateway's audience (#131). By name: a host name of its own for the
# host's network address, which OpenShell's proxy (in the host's network)
# resolves and connects to; the gateway listens on every address. By the
# alias: host.openshell.internal, which OpenShell maps to the host.
if [ "$gateway_host" = name ]; then
	# The name in DNS, as a deployment's is: OpenShell's policy DNS asks the
	# nameserver of the host's resolv.conf and never reads /etc/hosts. A
	# local dnsmasq answers custos-gw.test and forwards the rest to the
	# host's nameserver; resolv.conf points at it for the run.
	gw_host=custos-gw.test
	host_ip=$(ip -4 route get 192.0.2.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
	upstream=$(sed -n 's/^nameserver[[:space:]]*\([0-9.]*\).*/\1/p' /etc/resolv.conf | head -n1)
	check "dnsmasq is installed" command -v dnsmasq
	systemd-run --unit=osh-dnsmasq dnsmasq --keep-in-foreground --no-resolv --no-hosts --bind-interfaces \
		--listen-address=127.0.0.1 --port=53 --local="/$gw_host/" --address="/$gw_host/$host_ip" --server="${upstream:-127.0.0.53}" >/dev/null
	mv /etc/resolv.conf "$work/resolv.conf.orig"
	printf 'nameserver 127.0.0.1\n' >/etc/resolv.conf
	restore_resolv() { if [ -e "$work/resolv.conf.orig" ] || [ -L "$work/resolv.conf.orig" ]; then rm -f /etc/resolv.conf; mv "$work/resolv.conf.orig" /etc/resolv.conf; fi; }
	trap restore_resolv EXIT
	check "$gw_host resolves by DNS to the host's network address ($host_ip)" eventually 10 bash -c \
		"[ -n '$host_ip' ] && getent ahostsv4 $gw_host | grep -q '^$host_ip '"
	check "and other names still resolve (dnsmasq forwards to $upstream)" eventually 10 getent ahosts registry.opensuse.org
else
	gw_host=host.openshell.internal
	restore_resolv() { :; }
fi
aud=https://$gw_host:18443/mcp
issuer=http://127.0.0.1:18445
mkdir -p "$work/gw/servers.d" "$work/gw/rbac" "$work/tls" /run/mcp-gateway
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj /CN=custos-test-ca \
	-addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign \
	-keyout "$work/tls/ca.key" -out "$work/tls/ca.pem" 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=$gw_host" \
	-keyout "$work/tls/key.pem" -out "$work/tls/req.pem" 2>/dev/null
printf 'subjectAltName=DNS:%s,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' "$gw_host" >"$work/tls/ext.cnf"
openssl x509 -req -in "$work/tls/req.pem" -CA "$work/tls/ca.pem" -CAkey "$work/tls/ca.key" -CAcreateserial -days 2 \
	-extfile "$work/tls/ext.cnf" -out "$work/tls/cert.pem" 2>/dev/null
check "the gateway's certificate names $gw_host (no other name)" bash -c "openssl x509 -in '$work/tls/cert.pem' -noout -ext subjectAltName | grep -q 'DNS:$gw_host, IP'"

# agent-sb's OAuth client; its tokens live 90 s, so the agent outlives one.
head -c 24 /dev/urandom | base64 | tr -d '/+=' >"$work/client-secret"
systemd-run --unit=osh-testidp /usr/libexec/custos/tests/testidp --listen 127.0.0.1:18445 --ttl 90 \
	--client "agent-sb:$(cat "$work/client-secret"):agent-sb:$aud" >/dev/null
check "the token issuer answers" eventually 20 curl -sf -o /dev/null "$issuer/.well-known/openid-configuration"
# Its subjects agent-<name> are agents (agents/_http.yaml, CONTRACT.md §16a).
printf '_http:\n  issuers: ["%s"]\n' "$issuer" >/etc/mcp-gateway/policy/agents/_http.yaml

if [ -n "$system" ]; then
	# The system's OPA (mcp-opa.service) has the gateway's policy, the
	# agents module and /etc/mcp-gateway/policy/agents already.
	check "OPA has the agents' issuer" eventually 30 bash -c "curl -s --unix-socket /run/mcp-gateway/opa.sock http://opa/v1/data/mcp/agents/_http/issuers | grep -qF '$issuer'" || {
		echo "    | OPA says: $(curl -sS -w ' (HTTP %{http_code})' --unix-socket /run/mcp-gateway/opa.sock http://opa/v1/data/mcp/agents/_http 2>&1 | head -c 400)"
		echo "    | the socket: $(ls -lZ /run/mcp-gateway/ 2>&1 | tr '\n' ' ')"
		echo "    | the file: $(ls -lZ /etc/mcp-gateway/policy/agents/ 2>&1 | tr '\n' ' ')"
		journalctl -u mcp-opa.service -o cat --no-pager --since "@$t0" | tail -n 10 | sed 's/^/    | mcp-opa: /'
	}
else
	# OPA as mcp-opa.service runs it: the gateway's policy, the agents module,
	# the agents' data (agent-admin) and the gateway's default role data.
	cp "$gw_policy/mcp/rbac/data.yaml" "$work/gw/rbac/data.yaml"
	systemd-run --unit=osh-opa opa run --server --addr unix:///run/mcp-gateway/opa.sock --watch --log-format json \
		--ignore rbac --ignore '*.json' "$gw_policy" /usr/share/mcp-gateway/policy/custos \
		mcp:/etc/mcp-gateway/policy "mcp.rbac:$work/gw/rbac" >/dev/null
	check "OPA has the agents' issuer" eventually 30 bash -c "curl -s --unix-socket /run/mcp-gateway/opa.sock http://opa/v1/data/mcp/agents/_http/issuers | grep -qF '$issuer'"
fi

if [ -n "$system" ]; then
	# As an administrator would: the certificate for the gateway's group,
	# the listener's port mcp_port_t, the issuer's port one the gateway may
	# reach (http_port_t), the test server, then a restart (http.listen is
	# read at start).
	install -d -m 0750 -o root -g mcp-gateway /etc/mcp-gateway/tls
	install -m 0640 -o root -g mcp-gateway "$work/tls/cert.pem" /etc/mcp-gateway/tls/custos-osh-cert.pem
	install -m 0640 -o root -g mcp-gateway "$work/tls/key.pem" /etc/mcp-gateway/tls/custos-osh-key.pem
	restorecon -R /etc/mcp-gateway/tls
	semanage port -a -t mcp_port_t -p tcp 18443 2>/dev/null || semanage port -m -t mcp_port_t -p tcp 18443
	semanage port -a -t http_port_t -p tcp 18445 2>/dev/null || semanage port -m -t http_port_t -p tcp 18445
	mkdir -p /etc/mcp-gateway/gateway.d /etc/mcp-gateway/servers.d
	printf '%s\n' "# custos OpenShell tests (test/openshell/run-spike.sh --system)" \
		"http:" '  listen: ":18443"' "  cert_file: /etc/mcp-gateway/tls/custos-osh-cert.pem" \
		"  key_file: /etc/mcp-gateway/tls/custos-osh-key.pem" "  issuer: $issuer" "  audience: $aud" \
		>/etc/mcp-gateway/gateway.d/60-custos-openshell.yaml
	printf '%s\n' 'version: 1' 'name: custos-test' 'command: ["/usr/libexec/custos/tests/testsrv"]' 'run_as: principal' \
		>/etc/mcp-gateway/servers.d/custos-test.yaml
	check "the system's mcp-gateway restarts with the HTTPS listener" systemctl restart mcp-gateway.service ||
		journalctl -u mcp-gateway.service -o cat --no-pager --since "@$t0" | tail -n 15 | sed 's/^/    | gateway: /'
else
	printf '%s\n' 'version: 1' 'name: custos-test' 'command: ["/usr/libexec/custos/tests/testsrv"]' 'run_as: gateway' \
		>"$work/gw/servers.d/custos-test.yaml"
	printf '%s\n' "socket: $work/gw/mcp.sock" "servers_dir: $work/gw/servers.d" "state_dir: $work/gw/state" \
		"approvals:" "  control_socket: $work/gw/control.sock" \
		"policy:" "  opa_socket: /run/mcp-gateway/opa.sock" \
		"supervisor:" "  mode: exec" \
		"http:" '  listen: ":18443"' "  cert_file: $work/tls/cert.pem" "  key_file: $work/tls/key.pem" \
		"  issuer: $issuer" "  audience: $aud" >"$work/gw/gateway.yaml"
	# A gateway of its own, in development mode (exec supervisor), with the
	# HTTPS listener.
	systemd-run --unit=osh-mcp-gateway "$gw_bin" --config "$work/gw/gateway.yaml" >/dev/null
fi
check "the gateway's HTTPS listener refuses a request without a token (401)" eventually 30 bash -c \
	"curl -s -o /dev/null -w '%{http_code}' --cacert '$work/tls/ca.pem' --resolve $gw_host:18443:127.0.0.1 -X POST $aud | grep -qx 401" ||
	journalctl -u "$gw_unit" -o cat --no-pager --since "@$t0" | tail -n 15 | sed 's/^/    | gateway: /'
check "the gateway takes the test server" eventually 30 bash -c "curl -s --unix-socket '$gw_ctl' http://localhost/v1/servers | grep -q custos-test"
check "the issuer gives agent-sb's client a token" bash -c \
	"curl -sf -X POST -d grant_type=client_credentials -d client_id=agent-sb --data-urlencode client_secret=\$(cat '$work/client-secret') '$issuer/token' | jq -e '.access_token != null' >/dev/null"
minted() { curl -s "$issuer/minted" | jq -r '."agent-sb" // 0'; }

section "OpenShell trusts the gateway's CA"
# OpenShell's proxy verifies the gateway in each sandbox's supervisor,
# not with the image's trust store: the CA goes into the OpenShell
# gateway's configuration (CONTRACT.md §16a), here the local gateway of
# OPENSHELL_GATEWAY_USER (systemctl --user, ~/.config/openshell).
osh_toml=$osh_home/.config/openshell/gateway.toml
# systemctl --user of the OpenShell user: through systemd-machined where
# it runs, else as the user with its runtime directory.
osh_systemctl() {
	systemctl --user --machine="$osh_user@" "$@" 2>/dev/null ||
		setpriv --reuid="$osh_uid" --regid="$(id -g "$osh_user")" --init-groups -- \
			env HOME="$osh_home" XDG_RUNTIME_DIR="/run/user/$osh_uid" systemctl --user "$@"
}
check "the OpenShell gateway's user is known (OPENSHELL_GATEWAY_USER)" test -n "$osh_user" -a -d "$osh_home/.config/openshell"
install -m 0644 -o "$osh_user" "$work/tls/ca.pem" "$osh_home/.config/openshell/mcp-gateway-ca.pem"
# A gateway.toml must say its format (OpenShell 0.1.7: version 2); the
# package does not ship one for a local gateway.
[ -s "$osh_toml" ] || { printf '[openshell]\nversion = 2\n' >"$osh_toml" && chown "$osh_user" "$osh_toml"; }
if grep -q '^\[openshell\.supervisor\.network\]' "$osh_toml"; then
	sed -i "/^\[openshell\.supervisor\.network\]/a additional_ca_bundle = \"$osh_home/.config/openshell/mcp-gateway-ca.pem\"" "$osh_toml"
else
	printf '\n[openshell.supervisor.network]\nadditional_ca_bundle = "%s"\n' "$osh_home/.config/openshell/mcp-gateway-ca.pem" >>"$osh_toml"
fi
check "OpenShell's configuration check takes it" runuser -u "$osh_user" -- env HOME="$osh_home" XDG_CONFIG_HOME="$osh_home/.config" openshell-gateway config preflight ||
	{
		runuser -u "$osh_user" -- env HOME="$osh_home" XDG_CONFIG_HOME="$osh_home/.config" openshell-gateway config preflight 2>&1 | tail -n 15 | sed 's/^/    | preflight: /'
		sed 's/^/    | gateway.toml: /' "$osh_toml"
	}
check "the OpenShell gateway restarts with the CA" osh_systemctl restart openshell-gateway ||
	osh_systemctl status --no-pager openshell-gateway 2>&1 | tail -n 15 | sed 's/^/    | /'
check "the OpenShell gateway answers again" eventually 90 osh status

section "Agent image"
img=localhost/custos-agent:ci
if [ -n "$system" ]; then
	# The installed launcher and Containerfile (custos package).
	mkdir -p "$work/image/bin" && cp /usr/libexec/custos/agent-launcher "$work/image/bin/"
	chown -R "$osh_user" "$work/image"
	chmod 0755 "$work"
	check "the agent image builds ($runtime)" ctr_ok build -q -t "$img" -f /usr/share/custos/openshell/Containerfile "$work/image"
else
	check "the agent image builds ($runtime)" ctr build -q -t "$img" -f "$here/packaging/openshell/Containerfile" "$here"
fi

check "the image resolves names by DNS first (hosts: dns files)" bash -c \
	"$(declare -f ctr); ctr run --rm --entrypoint /bin/sh '$img' -c 'grep -x \"hosts: dns files\" /etc/nsswitch.conf' >/dev/null"

section "Creating the agent"
cat >"$work/sb.yaml" <<EOF
version: 1
name: sb
description: OpenShell spike agent
model: {provider: openai, name: fake, endpoint: "http://127.0.0.1:8008/v1"}
instruction: You are a test agent.
gateway:
  url: "$aud"
  servers: [custos-test]
  oauth: {token_url: "$issuer/token", client_id: agent-sb, refresh_before_seconds: 30}
limits: {max_invocation_seconds: 300}
interaction: {agui: true, client_tools: deny}
sandbox: {kind: openshell, openshell_gateway: openshell}
EOF
agent-admin delete sb >/dev/null 2>&1
export CUSTOS_OPENSHELL_IMAGE=$img
# Rootless Podman: the relay's log source reads the user's containers.
[ "$runtime" = podman ] && export CUSTOS_OPENSHELL_USER=$osh_user
check "agent-admin create sb (openshell)" agent-admin create sb --from "$work/sb.yaml"
# #131: no note by the host's name (it resolves to a network address and
# is the gateway's audience); the alias's note says what an official
# certificate needs.
gwcfg=()
[ -n "$system" ] || gwcfg=(--gateway-config "$work/gw/gateway.yaml")
if [ "$gateway_host" = name ]; then
	check "agent-admin check sb: no note on the gateway's name" bash -c "! agent-admin check sb | grep -q 'note: gateway.url'"
else
	check "agent-admin check sb: the note on host.openshell.internal and the certificate" bash -c \
		"agent-admin check sb | grep -q 'note: gateway.url $aud: the sandbox reaches the gateway as host.openshell.internal'"
fi
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change}' >"$work/perms.yaml"
check "agent-admin permissions sb (supervised: change needs an approval)" agent-admin permissions sb <"$work/perms.yaml"
check "the sandbox policy is written" test -s /etc/custos/agents/sb/openshell-policy.yaml
check "the provider profile is imported" eventually 10 bash -c 'openshell -g openshell profile list -o json 2>/dev/null | grep -q custos-gateway-sb'
# #111: the client's tokens checked before OpenShell gets the secret. The
# installed gateway's settings as the gateway reads them (its listener is
# a gateway.d drop-in), else the spike's own gateway's file.
idp=$(agent-admin idp-client sb "${gwcfg[@]}")
check "agent-admin idp-client sb: its client id" grep -qF 'client id     agent-sb' <<<"$idp"
check "agent-admin idp-client sb: the gateway's audience" grep -qF "$aud" <<<"$idp"
check "agent-admin check-token sb: the issuer's token is what the gateway expects" \
	agent-admin check-token sb "${gwcfg[@]}" <"$work/client-secret"
refused() { local out; out=$(agent-admin check-token sb "${gwcfg[@]}" <<<wrong 2>&1) && return 1; grep -qF 'refused client agent-sb' <<<"$out"; }
check "agent-admin check-token sb with a wrong secret: the issuer refuses it" refused
n0=$(minted)
check "agent-admin set-credential sb gateway-client-secret (OpenShell mints the token, creates the sandbox)" \
	agent-admin set-credential sb gateway-client-secret <"$work/client-secret"
check "OpenShell's gateway minted agent-sb's first token" test "$(minted)" -gt "$n0"
check "OpenShell renews it (oauth2_client_credentials)" bash -c "openshell -g openshell provider refresh status agent-sb-gateway 2>&1 | grep -qi client.credentials" ||
	openshell -g openshell provider refresh status agent-sb-gateway 2>&1 | tail -n 10 | sed 's/^/    | /'
t_first=$SECONDS
check "the sandbox exists" eventually 120 bash -c 'openshell -g openshell sandbox get agent-sb >/dev/null 2>&1'
check "the relay socket is active" systemctl is-active -q agent-openshell@sb.socket

section "Supervising through the relay"
diagnose() {
	# Each step writes to a file, with a timeout and no stdin: a process a
	# step leaves running (forward start -d) must not hold a pipe open.
	local d=$work/diagnose port
	mkdir -p "$d"
	port=$(sed -n 's/^FORWARD_PORT=//p' /etc/custos/agents/sb/openshell.env)
	step() { # step <title> <command...>
		echo "--- $1"
		shift
		timeout -k 5 40 "$@" </dev/null >"$d/out" 2>&1
		tail -n 25 "$d/out"
	}
	{
		step "relay units" systemctl status --no-pager agent-openshell@sb.socket agent-openshell@sb.service
		step "relay journal" bash -c "journalctl --no-pager -o short-precise --since @$t0 -u agent-openshell@sb.service | grep -v 'proxy error'"
		step "openshell forward start $port agent-sb -d (by hand)" openshell -g openshell forward start "$port" agent-sb -d
		step "the agent, started by hand in the sandbox for 10 s" openshell -g openshell sandbox exec -n agent-sb -- /bin/sh -c \
			'timeout 10 /usr/libexec/custos/agent-launcher serve --config-env CUSTOS_CONFIG --listen-tcp 127.0.0.1:47999 --idle-exit 0; echo "exit status $?"'
		step "openshell sandbox get agent-sb" openshell -g openshell sandbox get agent-sb
		step "openshell forward list" openshell -g openshell forward list
		step "openshell logs agent-sb" openshell -g openshell logs agent-sb
		step "containers (the relay's log source)" bash -c "$(declare -f ctr); ctr ps --no-trunc --format '{{.ID}} {{.Names}} {{.Image}}'"
		step "agent-openshell-logs agent-sb" timeout 5 /usr/libexec/custos/agent-openshell-logs agent-sb
		step "listening on the host" bash -c "ss -ltnp | grep -E ':47[0-9]{3}\\b'"
	} >"$d/all" 2>&1
	echo "    | --- why the relay gets no answer"
	sed 's/^/    | /' "$d/all"
}
if eventually 180 bash -c 'curl -sf --max-time 10 --unix-socket /run/agents/sb/api.sock http://agent/v1/status >/dev/null'; then
	pass "the agent answers through the relay"
	relay_ok=1
else
	fail "the agent answers through the relay"
	diagnose
	relay_ok=
fi
check "the relay unit's openshell knows the gateway (HOME=/root)" not bash -c "journalctl --no-pager -o cat --since @$t0 -u agent-openshell@sb.service | grep -q 'Unknown gateway'"
if [ -n "$relay_ok" ]; then
	st=$(api /v1/status)
	check "status through the relay says lifecycle openshell" json_ok "$st" '.lifecycle.kind == "openshell" and .lifecycle.sandbox == "agent-sb"'
	check "the relay passed root on (status is for supervisors)" json_ok "$st" '.contract == 1'
	a=$(run_activity PROBE)
	check "a read-only call runs" summary_has "$a" "probe ok" ||
		{
			echo "    | the agent answered: $(jq -c '{state, summary, reason, calls: [.calls[]? | {tool, state, reason}]}' <<<"$a" 2>/dev/null || head -c 400 <<<"$a")"
			journalctl --no-pager -o cat --since "@$t0" AGENT_NAME=sb 2>/dev/null | tail -n 15 | sed 's/^/    | agent: /'
			# Where OpenShell's proxy takes the gateway's CA from.
			echo "    | OpenShell's gateway.toml: $(grep -A2 '^\[openshell\.supervisor\.network\]' "$osh_toml" 2>&1 | tr '\n' ' ')"
			journalctl --user --machine="$osh_user@" -u openshell-gateway --no-pager -o cat --since "@$t0" 2>/dev/null | grep -iE 'additional|ca bundle|trust' | tail -n 5 | sed 's/^/    | openshell-gateway: /'
			timeout 5 /usr/libexec/custos/agent-openshell-logs agent-sb </dev/null 2>&1 | grep -iE '18443|certificate|tls|trust' | tail -n 6 | sed 's/^/    | supervisor: /'
		}
	check "the gateway audited the call as agent-sb over HTTPS" eventually 10 gw_audit_has '.sub == "agent-sb" and .name == "probe" and .effect == "allow"' ||
		journalctl -u "$gw_unit" -o cat --no-pager --since "@$t0" | tail -n 15 | sed 's/^/    | gateway: /'
	check "and refused no request of the agent's (placeholder or no token)" not bash -c \
		"journalctl -u "$gw_unit" -o cat --no-pager --since @$t0 | grep -iE 'invalid_token|missing bearer|no subject'"
	check "the sandbox's environment has no client secret" bash -c "! openshell -g openshell sandbox exec -n agent-sb -- env 2>/dev/null | grep -qF \"\$(cat $work/client-secret)\""

	section "Token renewal"
	# The first token expires 90 s after it was minted; OpenShell renews
	# 30 s before. A call after that runs on a renewed token, without
	# restarting the agent.
	m1=$(minted)
	starts() { journalctl -o cat --no-pager --since "@$t0" AGENT_NAME=sb AGENT_EVENT=agent.start 2>/dev/null | wc -l; }
	s1=$(starts)
	check "OpenShell mints a new token before the first expires" eventually 120 bash -c "[ \$(curl -s $issuer/minted | jq -r '.\"agent-sb\" // 0') -gt $m1 ]"
	while [ $((SECONDS - t_first)) -lt 95 ]; do sleep 5; done
	a=$(run_activity PROBE)
	check "a call after the first token expired runs (renewed token)" summary_has "$a" "probe ok" || {
		echo "    | the agent answered: $(jq -c '{state, summary, reason}' <<<"$a" 2>/dev/null || head -c 300 <<<"$a")"
		# Who answered 500: mcp-gateway, or OpenShell's proxy?
		journalctl -u "$gw_unit" -o cat --no-pager --since "-120s" | grep -v '"audit":true' | tail -n 12 | sed 's/^/    | gateway: /'
		timeout 5 /usr/libexec/custos/agent-openshell-logs agent-sb </dev/null 2>&1 | grep -iE '18443|credential|placeholder|refresh|resolve|500' | tail -n 12 | sed 's/^/    | supervisor: /'
		timeout 20 openshell -g openshell provider refresh status agent-sb-gateway </dev/null 2>&1 | tail -n 12 | sed 's/^/    | refresh: /'
		journalctl --user --machine="$osh_user@" -u openshell-gateway --no-pager -o cat --since "-300s" 2>/dev/null | grep -iE 'refresh|mint|credential|error|warn' | tail -n 10 | sed 's/^/    | openshell-gateway: /'
	}
	check "the agent was not restarted for it (no new agent.start)" test "$(starts)" -eq "$s1"
	check "and the gateway refused none of its requests" not bash -c \
		"journalctl -u "$gw_unit" -o cat --no-pager --since @$t0 | grep -iE 'invalid_token|token is expired|token_expired'"

	section "Approval"
	id=$(api /v1/activities -X POST -d '{"prompt":"CHANGE"}' | jq -r .id)
	check "the change waits for an approval" eventually 60 bash -c "curl -s --unix-socket /run/agents/sb/api.sock http://agent/v1/activities/$id | jq -e '.state == \"waiting_approval\"' >/dev/null"
	aid=$(activity "$id" | jq -r '[.calls[]? | select(.approval_id != null and .approval_id != "")][0].approval_id // empty')
	check "its call carries the gateway's approval id" test -n "$aid"
	check "the gateway lists the approval for agent-sb" json_ok "$(gw /v1/approvals)" ".[] | select(.id == \"$aid\" and .principal.sub == \"agent-sb\")"
	check "nothing ran before the decision" not gw_audit_has '.sub == "agent-sb" and .name == "change" and .effect == "allow"'
	gw "/v1/approvals/$aid" -X POST -d '{"decision":"approve","scope":"once"}' >/dev/null
	check "the activity finishes after the approval" eventually 60 bash -c "curl -s --unix-socket /run/agents/sb/api.sock http://agent/v1/activities/$id | jq -e '.state == \"finished\" and (.summary | contains(\"changed test\"))' >/dev/null"
	check "journal (from the sandbox's output): approval.pending" eventually 60 journal_has approval.pending ".AGENT_APPROVAL_ID == \"$aid\"" || {
		# Where the relay's bridge reads the agent's records from.
		echo "    | relay: $(journalctl --no-pager -o cat --since "@$t0" -u agent-openshell@sb.service 2>&1 | grep -v 'proxy error' | tail -n 8 | tr '\n' '|')"
		echo "    | containers: $(ctr ps --format '{{.Names}} ({{.Image}})' </dev/null 2>&1 | tr '\n' ' ')"
		# The helper with the agent's openshell.env, then also with the relay
		# unit's sandboxing and SELinux domain (what differs from a shell).
		echo "    | helper: $(set -a; . /etc/custos/agents/sb/openshell.env; timeout 5 /usr/libexec/custos/agent-openshell-logs agent-sb </dev/null 2>&1 | tail -n 5 | tr '\n' '|')"
		echo "    | helper as the unit: $(timeout 20 systemd-run --wait --pipe --quiet -p EnvironmentFile=/etc/custos/agents/sb/openshell.env \
			-p NoNewPrivileges=yes -p ProtectHome=read-only -p PrivateTmp=yes \
			-p "ReadWritePaths=-$osh_home/.local/share/containers -/run/user/$osh_uid" \
			-p RuntimeMaxSec=5 /usr/libexec/custos/agent-openshell-logs agent-sb </dev/null 2>&1 | tail -n 5 | tr '\n' '|')"
		echo "    | helper as the unit without NoNewPrivileges: $(timeout 20 systemd-run --wait --pipe --quiet -p EnvironmentFile=/etc/custos/agents/sb/openshell.env \
			-p ProtectHome=read-only -p PrivateTmp=yes -p "ReadWritePaths=-$osh_home/.local/share/containers -/run/user/$osh_uid" \
			-p RuntimeMaxSec=5 /usr/libexec/custos/agent-openshell-logs agent-sb </dev/null 2>&1 | tail -n 5 | tr '\n' '|')"
		echo "    | the relay's drop-ins: $(systemctl show -p DropInPaths --value agent-openshell@sb.service)"
		echo "    | journal AGENT_NAME=sb: $(journalctl --no-pager -o cat --since "@$t0" AGENT_NAME=sb 2>&1 | wc -l) records"
	}
	check "journal: approval.resolved approved" eventually 60 journal_has approval.resolved '.AGENT_OUTCOME == "approved"'

	section "Records from the sandbox"
	# Security review H1: the relay journals the sandbox's records as root,
	# and root's records of agent-admin's events are trusted. A record
	# written on the container's output, as a compromised agent would: a
	# host event passes (the control), an admin's event and a field name
	# that would add fields do not.
	osh_container=$(ctr ps --format '{{.Names}}' 2>/dev/null | grep -E -- "--agent-sb-[0-9a-f-]+$" | head -n 1)
	forge() {
		ctr exec "$osh_container" sh -c "printf '%s\n' '$1' >/proc/1/fd/1" </dev/null >/dev/null 2>&1
	}
	if [ -n "$osh_container" ] && forge '{"contract":"1","name":"sb","event":"tool.call","message":"spike: a host record","tool":"spike__control"}'; then
		forge '{"contract":"1","name":"sb","event":"category.review","message":"forged","category":"spike-forged","verdict":"ok","by":"spike"}'
		forge '{"contract":"1","name":"sb","event":"tool.call","message":"forged fields","tool":"spike__fields","x=1\nSYSLOG_IDENTIFIER":"custos-promotion"}'
		check "a host record from the sandbox reaches the journal (the control)" eventually 30 journal_has tool.call '.AGENT_TOOL == "spike__control"'
		# In order: once the last line is in, the review was read too.
		check "a record with a field name that would add fields arrives without them" eventually 30 journal_has tool.call '.AGENT_TOOL == "spike__fields"'
		check "and no field was added (its identifier is the relay's)" not journal_has tool.call '.AGENT_TOOL == "spike__fields" and .SYSLOG_IDENTIFIER != "agent-sb"'
		check "an admin's event from the sandbox is not journalled" not journal_has category.review '.AGENT_CATEGORY == "spike-forged"'
	else
		skip "records from the sandbox (cannot write to the container's output: ${osh_container:-no container})"
	fi

	section "AG-UI through the relay"
	out=$(api /agui -N -X POST -d '{"threadId":"osh-1","messages":[{"role":"user","content":"PROBE"}]}')
	check "the stream comes through the relay" grep -q '"type":"RUN_FINISHED"' <<<"$out"

	section "Editing the agent"
	# A limit is in the configuration the sandbox was created with: edit
	# recreates the sandbox (same provider and token) and the relay (§16a).
	sed 's/max_invocation_seconds: 300/max_invocation_seconds: 301/' "$work/sb.yaml" >"$work/sb-edited.yaml"
	check "agent-admin edit sb (a limit)" bash -c "agent-admin edit sb --from '$work/sb-edited.yaml' >'$work/edit.out'"
	# Cockpit shows what edit prints on stdout: agent-admin's words only,
	# openshell's progress (✓ Sandbox …, Created sandbox) on stderr.
	check "agent-admin edit prints nothing of openshell's on stdout" not grep -qiE 'sandbox agent-sb|created sandbox|CUSTOS_RELAY_SECRET' "$work/edit.out" ||
		sed 's/^/    | /' "$work/edit.out"
	check "the relay runs again" systemctl is-active -q agent-openshell@sb.socket
	check "the agent answers with the new limit (the sandbox was recreated)" eventually 180 bash -c \
		"curl -sf --max-time 10 --unix-socket /run/agents/sb/api.sock http://agent/v1/status | jq -e '.limits.max_invocation_seconds == 301' >/dev/null" ||
		sandbox_state
	a=$(run_activity PROBE)
	check "a read-only call runs after the edit (the provider stayed)" summary_has "$a" "probe ok"
	check "the same file again leaves the sandbox's configuration (try-restart only)" agent-admin edit sb --from "$work/sb-edited.yaml" ||
		sandbox_state
	check "and the agent answers again" eventually 180 bash -c \
		"curl -sf --max-time 10 --unix-socket /run/agents/sb/api.sock http://agent/v1/status | jq -e '.limits.max_invocation_seconds == 301' >/dev/null" ||
		sandbox_state

else
	skip "the checks through the relay (the agent did not answer through it)"
fi
section "Emergency stop"
check "agent-admin stop-all" agent-admin stop-all
check "the relay is stopped" not systemctl is-active -q agent-openshell@sb.socket
# The state, wherever OpenShell's JSON has it (a string or an object).
check "the sandbox is stopped" eventually 60 bash -c 'openshell -g openshell sandbox get agent-sb -o json 2>/dev/null | jq -e "[.status, .state, .phase] | tostring | ascii_downcase | test(\"stop|exit|terminat\")" >/dev/null' ||
	echo "    | OpenShell says: $(timeout 20 openshell -g openshell sandbox get agent-sb -o json </dev/null 2>&1 | jq -c 'del(.spec, .policy, .network_policies)' 2>&1 | head -c 600)"
check "agent-admin release-all" agent-admin release-all
check "the relay is back" systemctl is-active -q agent-openshell@sb.socket

section Removing
check "agent-admin delete sb" agent-admin delete sb
check "the sandbox is gone" eventually 60 not osh sandbox get agent-sb
if [ -n "$system" ]; then
	section SELinux
	# Denials since the start for the domains this repository ships
	# (agent_t, agent_relay_t, mcpsrv_agents_t) and for what they reach.
	# Since boot (the VM's whole run): a start time in the locale's format
	# that ausearch does not take would make the check blind.
	avc=$(ausearch -m AVC,USER_AVC,SELINUX_ERR -ts boot 2>/dev/null)
	[ -n "$avc" ] || avc=$(journalctl -k --no-pager -o cat --since "@$t0" 2>/dev/null | grep -E 'avc: +denied')
	[ -n "$results" ] && mkdir -p "$results" && printf '%s\n' "$avc" >"$results/avc.txt"
	check "no SELinux denial for custos's domains" not grep -qE 'scontext=[^ ]*:(agent_t|agent_relay_t|mcpsrv_agents_t):' <<<"$avc" ||
		grep -E 'scontext=[^ ]*:(agent_t|agent_relay_t|mcpsrv_agents_t):' <<<"$avc" | sed -E 's/^.*(avc: +denied)/\1/' | sort -u | head -n 20 | sed 's/^/    | /'
	echo "    | all denials since the start: $(grep -c 'denied' <<<"$avc")"
	grep 'denied' <<<"$avc" | sed -E 's/^.*(avc: +denied)/\1/; s/ pid=[0-9]+//; s/ ino=[0-9]+//' | sort -u | head -n 15 | sed 's/^/    | /'
fi
systemctl stop osh-fakemodel osh-mcp-gateway osh-opa osh-testidp osh-dnsmasq >/dev/null 2>&1
restore_resolv

if [ -n "$results" ]; then
	mkdir -p "$results"
	n=0
	{
		echo "1..${#results_list[@]}"
		for r in "${results_list[@]}"; do
			IFS=$'\t' read -r sec st desc <<<"$r"
			n=$((n + 1))
			case $st in pass) echo "ok $n - $sec: $desc" ;; skip) echo "ok $n - $sec: $desc # SKIP" ;; *) echo "not ok $n - $sec: $desc" ;; esac
		done
	} >"$results/results.tap"
	journalctl --no-pager --since "@$t0" -u 'agent-openshell@*' -o short-precise >"$results/relay.log" 2>&1
	openshell -g openshell logs agent-sb >"$results/sandbox.log" 2>&1 || true
fi
n_fail=$(printf '%s\n' "${results_list[@]}" | awk -F'\t' '$2 == "fail"' | wc -l)
printf '\n%d checks, %d failed\n' "${#results_list[@]}" "$n_fail"
exit "$failed"
