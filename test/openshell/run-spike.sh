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
#        OPENSHELL_GATEWAY_USER=USER test/openshell/run-spike.sh --system --disposable [--gateway-host name|alias] [--driver systemd] [--results DIR]
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
# --driver systemd (#215, a spike): OpenShell's gateway uses the OpenShell
# systemd compute driver (test/openshell/vm-setup.sh --driver systemd):
# no container runtime and no image. The sandbox is created --from host,
# the one image name that driver takes, and runs the host's
# agent-launcher. The agent has sandbox.openshell_driver: systemd, and the
# relay reads its records from the journal of the sandbox's workload unit.
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
disposable= results= system= gateway_host=name driver=
while [ $# -gt 0 ]; do
	case $1 in
	--disposable) disposable=1 ;;
	--results) results=${2:?}; shift ;;
	--system) system=1 ;;
	--gateway-host) gateway_host=${2:?}; shift ;;
	--driver) driver=${2:?}; shift ;;
	*) echo "usage: $0 [--system] --disposable [--gateway-host name|alias] [--driver systemd] [--results DIR]" >&2; exit 2 ;;
	esac
	shift
done
[ -n "$disposable" ] || { echo "$0: installs agents, units and users on this system; use --disposable" >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo "$0: runs as root" >&2; exit 2; }
case $gateway_host in name | alias) ;; *) echo "$0: --gateway-host name or alias" >&2; exit 2 ;; esac
case $driver in "" | systemd) ;; *) echo "$0: --driver systemd, or none for Docker or Podman" >&2; exit 2 ;; esac
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
# The shipped playbooks against stand-ins for their servers (#227).
# shellcheck source=../vm/playbook-runs.sh
. "$here/test/vm/playbook-runs.sh"
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
# user's rootless Podman; none with the systemd driver.
if [ "$driver" = systemd ]; then
	runtime=systemd
	ctr() { echo "no container runtime: the systemd compute driver" >&2; return 1; }
elif docker info >/dev/null 2>&1; then
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
if [ "$runtime" = systemd ]; then
	check "the OpenShell gateway uses the systemd compute driver" bash -c 'openshell -g openshell gateway info 2>&1 | grep -q openshell-driver-systemd'
	check "the driver's helper runs" systemctl is-active -q openshell-systemd-helper.service
else
	check "the container runtime runs ($runtime)" ctr_ok info
fi
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
	standins_register /etc/mcp-gateway/servers.d /usr/libexec/custos/tests/testsrv principal
	check "the system's mcp-gateway restarts with the HTTPS listener" systemctl restart mcp-gateway.service ||
		journalctl -u mcp-gateway.service -o cat --no-pager --since "@$t0" | tail -n 15 | sed 's/^/    | gateway: /'
else
	printf '%s\n' 'version: 1' 'name: custos-test' 'command: ["/usr/libexec/custos/tests/testsrv"]' 'run_as: gateway' \
		>"$work/gw/servers.d/custos-test.yaml"
	standins_register "$work/gw/servers.d" /usr/libexec/custos/tests/testsrv gateway
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
check "the gateway takes the playbooks' stand-ins (${standin_servers[*]})" eventually 30 bash -c \
	"s=\$(curl -s --unix-socket '$gw_ctl' http://localhost/v1/servers); for n in ${standin_servers[*]}; do grep -q \"\\\"\$n\\\"\" <<<\"\$s\" || exit 1; done"
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
# agent-admin openshell-setup puts the CA where the driver needs it
# (#225): OpenShell 0.1.8 refuses gateway.toml's
# [openshell.supervisor.network] additional_ca_bundle with an external
# compute driver, whose supervisors run on the host and read its CA
# bundle, so with the systemd driver the CA goes into the host's trust
# store; with the container driver into gateway.toml, which hands it to
# each sandbox's supervisor.
setup_driver=container
[ "$runtime" = systemd ] && setup_driver=systemd
check "agent-admin openshell-setup --ca puts the gateway's CA in place" \
	agent-admin openshell-setup --user "$osh_user" --driver "$setup_driver" --ca "$work/tls/ca.pem" ||
	agent-admin openshell-setup --user "$osh_user" --driver "$setup_driver" --ca "$work/tls/ca.pem" --check 2>&1 | tail -n 15 | sed 's/^/    | /'
if [ "$runtime" = systemd ]; then
	check "the host's CA bundle has it" bash -c "grep -qF \"\$(sed -n 2p '$work/tls/ca.pem')\" /etc/ssl/ca-bundle.pem"
	check "gateway.toml has no CA bundle (refused with this driver)" not grep -q additional_ca_bundle "$osh_toml"
else
	check "gateway.toml names it for the supervisors" grep -q "additional_ca_bundle = \"$osh_home/.config/openshell/mcp-gateway-ca.pem\"" "$osh_toml"
	check "OpenShell's configuration check takes it" runuser -u "$osh_user" -- env HOME="$osh_home" XDG_CONFIG_HOME="$osh_home/.config" openshell-gateway config preflight ||
		{
			runuser -u "$osh_user" -- env HOME="$osh_home" XDG_CONFIG_HOME="$osh_home/.config" openshell-gateway config preflight 2>&1 | tail -n 15 | sed 's/^/    | preflight: /'
			sed 's/^/    | gateway.toml: /' "$osh_toml"
		}
fi
# The relay reads root's registration by its own type, not as any
# user's ~/.config (#237).
if [ -n "$system" ] && [ "$(getenforce 2>/dev/null)" = Enforcing ]; then
	check "root's OpenShell registration has the relay's type (agent_openshell_home_t)" bash -c \
		"ls -Zd /root/.config/openshell | grep -q ':agent_openshell_home_t:' && ! find /root/.config/openshell -not -context '*:agent_openshell_home_t:*' | grep -q ." ||
		ls -RZ /root/.config/openshell 2>&1 | head -n 10 | sed 's/^/    | /'
fi
check "the OpenShell gateway answers again" eventually 90 osh status
check "a second openshell-setup changes nothing" bash -c \
	"! agent-admin openshell-setup --user '$osh_user' --driver $setup_driver --ca '$work/tls/ca.pem' | grep -q '^set '"
# A second gateway, as root (systemctl --user as root): it makes a CA of
# its own and writes its client certificates into root's registration,
# and root's openshell then fails with BadSignature. openshell-setup
# stops and disables it and writes root's registration again. Root's
# user manager is reached as agent-admin reaches it (XDG_RUNTIME_DIR).
rootctl() { env XDG_RUNTIME_DIR=/run/user/0 systemctl --user "$@"; }
why=""
if [ "$(id -u)" != 0 ]; then
	why="the test does not run as root"
elif ! systemctl start user@0.service >"$work/user0.out" 2>&1; then
	why="root's user manager does not start: $(tail -n 1 "$work/user0.out")"
elif ! rootctl cat openshell-gateway.service >/dev/null 2>&1; then
	why="root's user manager has no openshell-gateway.service"
fi
if [ -z "$why" ]; then
	rootctl start openshell-gateway.service >/dev/null 2>&1 || true
	rootctl start openshell-driver-systemd.service >/dev/null 2>&1 || true
	# Where root's gateway cannot run here (the CI host's gateway.toml is
	# the runner's, which its unit cannot read), there is no case to repair.
	# The service active, not a process of its name: the unit's pre-start
	# step (openshell-gateway config preflight) has that name too, and runs
	# also where the gateway then fails to start.
	if ! eventually 30 rootctl is-active --quiet openshell-gateway.service; then
		why="root's gateway does not run here: $(journalctl --no-pager -o cat _UID=0 _SYSTEMD_USER_UNIT=openshell-gateway.service 2>&1 | grep -m1 -oE 'preflight failed: [^;]*|Error: .*' | head -c 160)"
		rootctl disable --now openshell-gateway.service openshell-driver-systemd.service >/dev/null 2>&1 || true
		rootctl reset-failed >/dev/null 2>&1 || true
	fi
fi
if [ -z "$why" ]; then
	pass "a gateway runs as root (the case to repair)"
	agent-admin openshell-setup --user "$osh_user" --driver "$setup_driver" --ca "$work/tls/ca.pem" >"$work/second-gw.out" 2>&1
	check "openshell-setup says it stopped them" grep -q "also run.* as root" "$work/second-gw.out" ||
		sed 's/^/    | /' "$work/second-gw.out"
	check "openshell-setup leaves no OpenShell gateway or driver running as root" \
		not pgrep -u 0 -f 'openshell-(gateway|driver-systemd)( |$)'
	check "root's OpenShell services are disabled" not rootctl is-enabled --quiet openshell-gateway.service
	check "root's openshell trusts the gateway again" eventually 60 osh status
else
	skip "a second OpenShell gateway as root ($why)"
fi
systemctl stop user@0.service 2>/dev/null || true

section "Agent image"
img=localhost/custos-agent:ci
if [ "$runtime" = systemd ]; then
	# No image: the host's agent-launcher, from the custos package.
	img=host
	check "the host's agent-launcher is there for the sandbox" test -x /usr/libexec/custos/agent-launcher
elif [ -n "$system" ]; then
	# The installed launcher, playbooks and Containerfile (custos package).
	mkdir -p "$work/image/bin" && cp /usr/libexec/custos/agent-launcher "$work/image/bin/"
	cp -r /usr/share/custos/playbooks "$work/image/"
	chown -R "$osh_user" "$work/image"
	chmod 0755 "$work"
	check "the agent image builds ($runtime)" ctr_ok build -q -t "$img" -f /usr/share/custos/openshell/Containerfile "$work/image"
else
	mkdir -p "$work/image/bin" && cp "$here/bin/agent-launcher" "$work/image/bin/"
	cp -r "$here/packaging/playbooks" "$work/image/"
	check "the agent image builds ($runtime)" ctr build -q -t "$img" -f "$here/packaging/openshell/Containerfile" "$work/image"
fi

[ "$runtime" = systemd ] ||
	check "the image resolves names by DNS first (hosts: dns files)" bash -c \
		"$(declare -f ctr); ctr run --rm --entrypoint /bin/sh '$img' -c 'grep -x \"hosts: dns files\" /etc/nsswitch.conf' >/dev/null"

section "An agent from a template (#226)"
# Without --system, the gateway's configuration is the test's, not /etc's.
gwcfg=()
[ -n "$system" ] || gwcfg=(--gateway-config "$work/gw/gateway.yaml")
# create --template --sandbox openshell: the template's configuration
# with mcp-gateway's audience, the agent's OAuth client (the test issuer
# is no Keycloak realm: --token-url) and this driver. Its sandbox is made
# by set-credential, which the agent sb below shows; this one is deleted.
tpl_driver=container
[ "$runtime" = systemd ] && tpl_driver=systemd
agent-admin delete tpl >/dev/null 2>&1
check "agent-admin create tpl --template triage --sandbox openshell" bash -c \
	"agent-admin create tpl --template triage --sandbox openshell --openshell-driver $tpl_driver --token-url '$issuer/token' ${gwcfg[*]} >'$work/tpl.out' 2>&1" ||
	sed 's/^/    | /' "$work/tpl.out"
check "it says what is next (idp-client, set-credential)" grep -q "next: agent-admin idp-client tpl" "$work/tpl.out"
check "its configuration names this driver and the gateway's audience" bash -c \
	"grep -Eq 'openshell_driver: \"?$tpl_driver\"?[,}]' /etc/custos/agents/tpl/agent.yaml && grep -qF '$aud' /etc/custos/agents/tpl/agent.yaml"
check "openshell.env names the driver" grep -qx "OPENSHELL_DRIVER=$tpl_driver" /etc/custos/agents/tpl/openshell.env
check "it is the same as its template to compare" bash -c "agent-admin compare tpl | jq -e '.[0].configuration.same' >/dev/null"
check "agent-admin delete tpl" agent-admin delete tpl
# A template for transactional systems as well (#241), on this driver.
agent-admin delete tpt >/dev/null 2>&1
check "agent-admin create tpt --template patch-pilot-transactional --sandbox openshell" bash -c \
	"agent-admin create tpt --template patch-pilot-transactional --sandbox openshell --openshell-driver $tpl_driver --token-url '$issuer/token' ${gwcfg[*]} >'$work/tpt.out' 2>&1" ||
	sed 's/^/    | /' "$work/tpt.out"
check "its configuration names this driver and the template's servers" bash -c \
	"grep -Eq 'openshell_driver: \"?$tpl_driver\"?[,}]' /etc/custos/agents/tpt/agent.yaml && grep -q 'servers: \[tukit' /etc/custos/agents/tpt/agent.yaml"
check "agent-admin delete tpt" agent-admin delete tpt
# A template's name longer than OpenShell takes, as the agent's name: refused (#244).
check "an OpenShell agent named like a long template is refused" not agent-admin create patch-pilot-transactional --template patch-pilot-transactional --sandbox openshell --token-url "$issuer/token" "${gwcfg[@]}"
# A name of 14 to 19 characters: its sandbox is named after the agent,
# since agent-NAME would exceed OpenShell's 19 characters.
agent-admin delete service-doctor >/dev/null 2>&1
check "agent-admin create service-doctor (14 characters) --sandbox openshell" bash -c \
	"agent-admin create service-doctor --template service-doctor --sandbox openshell --openshell-driver $tpl_driver --token-url '$issuer/token' ${gwcfg[*]} >'$work/sd.out' 2>&1" ||
	sed 's/^/    | /' "$work/sd.out"
check "its openshell.env names the sandbox service-doctor" grep -qx "OPENSHELL_SANDBOX=service-doctor" /etc/custos/agents/service-doctor/openshell.env
check "agent-admin delete service-doctor" agent-admin delete service-doctor

section "Creating the agent"
cat >"$work/sb.yaml" <<EOF
version: 1
name: sb
description: OpenShell spike agent
model: {provider: openai, name: fake, endpoint: "http://127.0.0.1:8008/v1"}
instruction: You are a test agent.
gateway:
  url: "$aud"
  servers: [custos-test, exec, systemd, custos-net, snapper, tukit, zypp, restarts, storage$([ -n "$system" ] && echo ", agents")]   # the playbooks' stand-ins, #227; agents: delegation (system gateway)
  oauth: {token_url: "$issuer/token", client_id: agent-sb, refresh_before_seconds: 90}
limits: {max_invocation_seconds: 300}
interaction: {agui: true, client_tools: deny}
sandbox: {kind: openshell, openshell_gateway: openshell, openshell_driver: $([ "$runtime" = systemd ] && echo systemd || echo container)}
EOF
agent-admin delete sb >/dev/null 2>&1
# The systemd driver: agent-admin takes no image (sandbox.openshell_driver).
[ "$runtime" = systemd ] || export CUSTOS_OPENSHELL_IMAGE=$img
# Rootless Podman: the relay's log source reads the user's containers.
[ "$runtime" = podman ] && export CUSTOS_OPENSHELL_USER=$osh_user
check "agent-admin create sb (openshell)" agent-admin create sb --from "$work/sb.yaml"
if [ "$runtime" = systemd ]; then
	check "openshell.env names the systemd driver" grep -qx OPENSHELL_DRIVER=systemd /etc/custos/agents/sb/openshell.env
	check "openshell.env: the sandbox has no image (host)" grep -qx AGENT_IMAGE=host /etc/custos/agents/sb/openshell.env
	check "no rootless Podman user for the systemd driver" not grep -q '^OPENSHELL_USER=' /etc/custos/agents/sb/openshell.env
fi
# #131: no note by the host's name (it resolves to a network address and
# is the gateway's audience); the alias's note says what an official
# certificate needs.
if [ "$gateway_host" = name ]; then
	check "agent-admin check sb: no note on the gateway's name" bash -c "! agent-admin check sb | grep -q 'note: gateway.url'"
else
	check "agent-admin check sb: the note on host.openshell.internal and the certificate" bash -c \
		"agent-admin check sb | grep -q 'note: gateway.url $aud: the sandbox reaches the gateway as host.openshell.internal'"
fi
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change}' >"$work/perms.yaml"
standin_permissions /usr/libexec/custos/tests/testsrv >>"$work/perms.yaml"
# Delegation from the agent in OpenShell to dl, a systemd agent (#364).
[ -n "$system" ] && printf '%s\n' "- {server: agents, tool: delegate, read_only: true, args: {agent: '^dl\$'}}" >>"$work/perms.yaml"
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
if [ "$runtime" = systemd ]; then
	section "The sandbox as systemd units"
	sid=$(openshell -g openshell sandbox get agent-sb 2>/dev/null | awk '$1 == "Id:" {print $2}')
	echo "    | sandbox id: ${sid:-none}"
	check "the sandbox's units exist" test -n "$sid"
	check "its workload unit becomes active" eventually 120 systemctl is-active -q "openshell-sandbox-workload@$sid.service" ||
		systemctl status --no-pager "openshell-sandbox-*@$sid.service" 2>&1 | tail -n 20 | sed 's/^/    | /'
	wl=$(systemctl show -p MainPID --value "openshell-sandbox-workload@$sid.service")
	echo "    | workload: pid ${wl:-none}, user $(stat -c %U "/proc/$wl" 2>&1), $(tr -d '\0' <"/proc/$wl/attr/current" 2>&1)"
	check "the workload runs as a user of the sandbox's own" bash -c "u=\$(stat -c %u /proc/$wl) && [ \"\$u\" -ge 70000 ] && [ \"\$u\" -le 79999 ]"
	check "the agent-launcher in the sandbox is the host's" eventually 60 bash -c \
		"pgrep -f -u \"\$(stat -c %u /proc/$wl)\" '^/usr/libexec/custos/agent-launcher serve' >/dev/null" || {
		# What the workload printed: openshell-sandbox's lines and the
		# agent's, before it ended.
		echo "    | the workload unit's journal:"
		journalctl --no-pager -o cat --since "@$t0" -u "openshell-sandbox-workload@$sid.service" 2>&1 |
			grep -v 'network notification denied' | tail -n 40 | cut -c1-300 | sed 's/^/    | /'
		echo "    | its state: $(systemctl show -p ActiveState,SubState,Result,ExecMainStatus --value "openshell-sandbox-workload@$sid.service" | tr '\n' ' ')"
		# The sandbox's own variables (--env, --env-from) as systemd gave
		# them to the supervisor, in OPENSHELL_USER_ENVIRONMENT: names,
		# lengths and whether the configuration decodes. No values: one of
		# them is the relay secret.
		sup=$(systemctl show -p MainPID --value "openshell-sandbox-supervisor@$sid.service")
		ue=$(tr '\0' '\n' <"/proc/$sup/environ" 2>/dev/null | sed -n 's/^OPENSHELL_USER_ENVIRONMENT=//p')
		echo "    | the supervisor's OPENSHELL_USER_ENVIRONMENT: ${#ue} bytes, $(jq -r 'to_entries | map("\(.key) (\(.value | length))") | join(", ")' <<<"$ue" 2>&1 | head -c 300)"
		echo "    | CUSTOS_CONFIG there decodes: $(jq -r '.CUSTOS_CONFIG // empty' <<<"$ue" 2>/dev/null | base64 -d 2>/dev/null | head -c 40 | tr -c '[:print:]' '.')"
		f=/var/lib/openshell/systemd/$sid/supervisor.env
		echo "    | supervisor.env: $(wc -c <"$f" 2>&1) bytes; its line: $(grep -c '^OPENSHELL_USER_ENVIRONMENT=' "$f" 2>&1), $(grep '^OPENSHELL_USER_ENVIRONMENT=' "$f" 2>/dev/null | wc -c) bytes"
		echo "    | SELinux denials so far (audit.log):"
		cat /var/log/audit/audit.log* 2>/dev/null | grep 'avc:  denied' | sed -E 's/^.*(avc: +denied)/\1/; s/ pid=[0-9]+//; s/ ino=[0-9]+//' | sort -u | tail -n 15 | cut -c1-300 | sed 's/^/    | /'
	}
fi

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

	# Delegation to an agent in OpenShell: the gateway's bridge (the server
	# agents, SELinux mcpsrv_agents_t) connects to the relay's socket,
	# which systemd labels with the relay's domain (agent_relay_t), not
	# the agent's. On the system gateway only, where SELinux confines the
	# bridge.
	if [ -n "$system" ]; then
		agents_added=
		if [ ! -e /etc/mcp-gateway/servers.d/agents.yaml ]; then
			cp /usr/share/custos/gateway/servers.d/agents.yaml /etc/mcp-gateway/servers.d/agents.yaml && agents_added=1
		fi
		gw_has_agents() { gw /v1/servers | grep -q '"agents"'; }
		check "the gateway takes the server agents" eventually 30 gw_has_agents
		printf '%s\n' 'version: 1' 'name: dl' 'description: OpenShell spike, the delegating agent (systemd)' \
			'model: {provider: openai, name: fake, endpoint: "http://127.0.0.1:8008/v1"}' \
			'instruction: You are a test agent.' 'gateway: {servers: [agents, custos-test]}' \
			'limits: {max_invocation_seconds: 300}' 'interaction: {agui: true, client_tools: deny}' >"$work/dl.yaml"
		printf '%s\n' "- {server: agents, tool: delegate, read_only: true, args: {agent: '^sb\$'}}" \
			'- {server: custos-test, tool: probe, read_only: true}' >"$work/dl-perms.yaml"
		agent-admin delete dl >/dev/null 2>&1
		dl_create() { agent-admin create dl --from "$work/dl.yaml" >/dev/null && agent-admin permissions dl <"$work/dl-perms.yaml"; }
		check "agent-admin create dl (systemd), which may delegate to sb" dl_create
		d=$(curl -sS --max-time 240 --unix-socket /run/agents/dl/api.sock http://agent/v1/activities \
			-d '{"prompt":"DELEGATE sb PROBE","wait":true}')
		check "dl's delegation to sb, in OpenShell, returns sb's answer" summary_has "$d" "probe ok" ||
			{
				echo "    | dl answered: $(jq -c '{state, summary, reason}' <<<"$d" 2>/dev/null || head -c 300 <<<"$d")"
				ausearch -m AVC -ts recent 2>/dev/null | grep -E 'mcpsrv_agents_t|agent_relay_t' | tail -n 3 | sed 's/^/    | avc: /'
			}
		check "sb recorded the delegated activity from agent-dl" eventually 20 journal_has invocation.start \
			'.AGENT_ORIGIN == "delegation" and .AGENT_ORIGIN_BY == "agent-dl" and .AGENT_PARENT_AGENT == "dl"'
		# The other way (#364): sb calls the gateway with a token, so it has
		# no local user there; the bridge runs as custos-delegate and names
		# sb from what the gateway says (MCP_GATEWAY_PRINCIPAL).
		d=$(run_activity "DELEGATE dl PROBE")
		check "sb's delegation, from OpenShell, to dl returns dl's answer" summary_has "$d" "probe ok" ||
			{
				echo "    | sb answered: $(jq -c '{state, summary, reason}' <<<"$d" 2>/dev/null || head -c 300 <<<"$d")"
				journalctl -u "$gw_unit" -o cat --no-pager --since "@$t0" | grep -i 'agent-delegate\|agents' | tail -n 5 | sed 's/^/    | gateway: /'
			}
		dl_has() { journalctl -o json --all --no-pager --since "@$t0" AGENT_NAME=dl AGENT_EVENT=invocation.start | jq -e "select($1)" >/dev/null 2>&1; }
		check "dl recorded the delegated activity from agent-sb" eventually 20 dl_has \
			'.AGENT_ORIGIN == "delegation" and .AGENT_ORIGIN_BY == "agent-sb" and .AGENT_PARENT_AGENT == "sb"'
		check "custos-delegate is a member of agent-clients" bash -c "id -nG custos-delegate | tr ' ' '\\n' | grep -qx agent-clients"
		check "the gateway audited agents__delegate by agent-sb (a token) as allowed" eventually 10 gw_audit_has \
			'.sub == "agent-sb" and .server == "agents" and .name == "delegate" and .effect == "allow"'
		agent-admin delete dl >/dev/null 2>&1
		[ -n "$agents_added" ] && rm -f /etc/mcp-gateway/servers.d/agents.yaml
	fi

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
		# What the relay's domain was refused (from the audit log: #221).
		echo "    | agent_relay_t denials: $(cat /var/log/audit/audit.log* 2>/dev/null | grep -E 'avc: +denied' | grep 'scontext=[^ ]*:agent_relay_t:' |
			sed -E 's/^.*(avc: +denied)/\1/; s/ pid=[0-9]+//; s/ ino=[0-9]+//' | sort -u | head -n 10 | tr '\n' '|')"
	}
	check "journal: approval.resolved approved" eventually 60 journal_has approval.resolved '.AGENT_OUTCOME == "approved"'
	if [ "$runtime" = systemd ]; then
		# The agent's own output, as journald has it from the workload's
		# unit: what the relay's log source reads. In the
		# sandbox there is no journal socket, so the agent writes JSON
		# lines (internal/journal).
		unit_log() { journalctl --no-pager -o cat --since "@$t0" -u "openshell-sandbox-workload@$sid.service" 2>/dev/null; }
		check "the agent's records are in its workload unit's journal" bash -c "$(declare -f unit_log); t0=$t0 sid=$sid; unit_log | grep -q '\"event\":\"approval.pending\"'" || {
			echo "    | the workload unit's last lines:"
			unit_log | tail -n 8 | cut -c1-200 | sed 's/^/    | /'
		}
		echo "    | the agent's records there: $(unit_log | grep -c '"event":')"
	fi

	# Security review H1: the relay journals the agent's records as root,
	# so it passes only the events an agent host writes, with field names
	# that cannot add fields (internal/relay's and internal/journal's
	# tests). Only the agent process itself writes on its output: a
	# process the spike runs in the sandbox (openshell sandbox exec) may
	# not (/proc/<launcher>/fd/1: permission denied), nor can docker or
	# podman exec reach it, so this is not tried here.

	section "The shipped playbooks in the sandbox (#227)"
	# Every shipped playbook, as on the systemd backend (the functional
	# tests' section 85), here through the sandbox's supervisor, OpenShell's
	# proxy and the gateway's HTTPS listener.
	playbook_runs sb "$gw_unit" "openshell ($runtime)"

	section "AG-UI through the relay"
	out=$(api /agui -N -X POST -d '{"threadId":"osh-1","messages":[{"role":"user","content":"PROBE"}]}')
	check "the stream comes through the relay" grep -q '"type":"RUN_FINISHED"' <<<"$out"

	section "Editing the agent"
	# A limit is in the configuration the sandbox was created with: edit
	# recreates the sandbox (same provider and token) and the relay (§16a).
	sed 's/max_invocation_seconds: 300/max_invocation_seconds: 301/' "$work/sb.yaml" >"$work/sb-edited.yaml"
	# Security review H2: while edit recreates the sandbox, every command
	# line (what any local user reads in /proc) is sampled; the relay
	# secret and the configuration must be in none.
	(while :; do cat /proc/[0-9]*/cmdline 2>/dev/null | tr '\0' ' '; echo; done) >"$work/cmdlines" 2>/dev/null &
	sampler=$!
	te=$(date +%s)
	check "agent-admin edit sb (a limit)" bash -c "agent-admin edit sb --from '$work/sb-edited.yaml' >'$work/edit.out'" || {
		if [ "$runtime" = systemd ]; then
			# What the driver did with the old sandbox and the new one.
			journalctl --no-pager -o cat --since "@$te" -u openshell-systemd-helper.service 2>&1 | tail -n 15 | cut -c1-240 | sed 's/^/    | helper: /'
			journalctl --no-pager -o cat --since "@$te" _SYSTEMD_USER_UNIT=openshell-gateway.service 2>&1 |
				grep -iE 'error|warn|sandbox|driver' | tail -n 15 | cut -c1-240 | sed 's/^/    | gateway: /'
			journalctl --no-pager -o short-precise --since "@$te" -u 'openshell-sandbox-*' 2>&1 | tail -n 25 | cut -c1-240 | sed 's/^/    | units: /'
		fi
	}
	kill "$sampler" 2>/dev/null; wait "$sampler" 2>/dev/null
	check "the sampler saw openshell's sandbox create (the control)" grep -q "sandbox create --name agent-sb" "$work/cmdlines"
	check "no command line had the relay secret" not grep -qF "$(cat /etc/custos/agents/sb/relay.secret)" "$work/cmdlines"
	check "nor the configuration (CUSTOS_CONFIG=)" not grep -q "CUSTOS_CONFIG=" "$work/cmdlines"
	rm -f "$work/cmdlines"
	# Cockpit shows what edit prints on stdout: agent-admin's words only,
	# openshell's progress (✓ Sandbox …, Created sandbox) on stderr.
	check "agent-admin edit prints nothing of openshell's on stdout" not grep -qiE 'sandbox agent-sb|created sandbox|CUSTOS_RELAY_SECRET' "$work/edit.out" ||
		sed 's/^/    | /' "$work/edit.out"
	check "the relay runs again" systemctl is-active -q agent-openshell@sb.socket
	check "the agent answers with the new limit (the sandbox was recreated)" eventually 180 bash -c \
		"curl -sf --max-time 10 --unix-socket /run/agents/sb/api.sock http://agent/v1/status | jq -e '.limits.max_invocation_seconds == 301' >/dev/null" ||
		sandbox_state
	a=$(run_activity PROBE)
	check "a read-only call runs after the edit (the provider stayed)" summary_has "$a" "probe ok" || {
		echo "    | the agent answered: $(jq -c '{state, summary, reason}' <<<"$a" 2>/dev/null | head -c 600 || head -c 400 <<<"$a")"
		journalctl -u "$gw_unit" -o cat --no-pager --since "-180s" | grep -v '"audit":true' | tail -n 8 | cut -c1-240 | sed 's/^/    | gateway: /'
		if [ "$runtime" = systemd ]; then
			sid2=$(openshell -g openshell sandbox get agent-sb 2>/dev/null | awk '$1 == "Id:" {print $2}')
			echo "    | the new sandbox: ${sid2:-none}, user $(cat "/var/lib/openshell/systemd/$sid2/uid" 2>&1)"
			journalctl --no-pager -o cat --since "-180s" -u "openshell-sandbox-supervisor@$sid2.service" 2>&1 |
				grep -iE 'error|warn|denied|tls|certificate|credential|provider' | tail -n 10 | cut -c1-240 | sed 's/^/    | supervisor: /'
			# The workload started without AGENT_GATEWAY_TOKEN (the agent sent
			# no Authorization header): what the supervisor did up to the
			# workload's launch, unfiltered, and what the OpenShell gateway
			# said of the sandbox's provider environment.
			journalctl --no-pager -o short-precise --since "-180s" -u "openshell-sandbox-supervisor@$sid2.service" \
				-u "openshell-sandbox-workload@$sid2.service" 2>&1 | head -n 60 | cut -c1-260 | sed 's/^/    | start: /'
			# Whole lines, without colours and the request span's fields, so
			# the message of a warning is not cut off.
			journalctl --no-pager -o cat --since "-180s" _SYSTEMD_USER_UNIT=openshell-gateway.service 2>&1 |
				grep -iE 'provider|environment|credential|identity' | sed -E 's/\x1b\[[0-9;]*m//g; s/request\{[^}]*\}//' |
				grep -vE ' INFO +(:|openshell_server::grpc)? *$' | tail -n 25 | cut -c1-600 | sed 's/^/    | gateway: /'
		fi
	}
	check "the same file again leaves the sandbox's configuration (try-restart only)" agent-admin edit sb --from "$work/sb-edited.yaml" ||
		sandbox_state
	check "and the agent answers again" eventually 180 bash -c \
		"curl -sf --max-time 10 --unix-socket /run/agents/sb/api.sock http://agent/v1/status | jq -e '.limits.max_invocation_seconds == 301' >/dev/null" ||
		sandbox_state

else
	skip "the checks through the relay (the agent did not answer through it)"
fi
if [ -n "$relay_ok" ]; then
	section "The forward's port held by another user"
	# Security review H3: the relay sends its secret through the forward.
	# A process of another user holding the port first: the forward
	# fails, and so does the unit before its relay starts, so nothing is
	# sent there (the relay's own check of the socket's owner is in
	# internal/relay's tests). Once the port is free, the agent answers.
	fport=$(sed -n 's/^FORWARD_PORT=//p' /etc/custos/agents/sb/openshell.env)
	systemctl stop agent-openshell@sb.socket agent-openshell@sb.service
	systemd-run --unit=osh-squatter --uid=nobody --gid="$(id -g nobody)" \
		/usr/libexec/custos/tests/fakemodel --listen "127.0.0.1:$fport" >/dev/null
	check "nobody holds 127.0.0.1:$fport" eventually 20 bash -c "ss -ltn 'sport = :$fport' | grep -q LISTEN"
	check "the relay unit does not start with the port held (the forward fails)" not timeout 200 systemctl start agent-openshell@sb.service
	systemctl stop osh-squatter agent-openshell@sb.service >/dev/null 2>&1
	systemctl reset-failed agent-openshell@sb.service osh-squatter >/dev/null 2>&1
	systemctl start agent-openshell@sb.socket
	check "with the port free again, the agent answers through the relay" eventually 240 bash -c \
		'curl -sf --max-time 10 --unix-socket /run/agents/sb/api.sock http://agent/v1/status >/dev/null' || sandbox_state
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
	# From auditd's log itself, by the records' own time (msg=audit(EPOCH…)):
	# on the Leap 16 VM, ausearch -ts boot gave nothing where the log had
	# denials, and the kernel's journal has none while auditd takes them,
	# which left this check blind (#221). The kernel's journal only where
	# there is no audit log.
	if ls /var/log/audit/audit.log* >/dev/null 2>&1; then
		avc=$(cat /var/log/audit/audit.log* | grep -E '^type=(AVC|USER_AVC|SELINUX_ERR) ' |
			awk -v t0="$t0" '{ s = $0; sub(/^.*msg=audit\(/, "", s); sub(/[.:].*$/, "", s); if (s + 0 >= t0) print }')
		# The log has the run's records (service starts at least), so that
		# the check below sees what was denied.
		check "auditd's log has this run's records" bash -c \
			"cat /var/log/audit/audit.log* | grep -E '^type=SERVICE_START ' | awk -v t0=$t0 '{ s = \$0; sub(/^.*msg=audit\\(/, \"\", s); sub(/[.:].*\$/, \"\", s); if (s + 0 >= t0) n++ } END { exit !n }'"
		# Whether ausearch with a start time (as the VM tests ask it) sees them.
		echo "    | denials in audit.log since the start: $(grep -c denied <<<"$avc"); ausearch -ts <start> finds: $(ausearch -m AVC,USER_AVC,SELINUX_ERR -ts "$(date -d "@$t0" '+%m/%d/%Y %H:%M:%S')" 2>/dev/null | grep -c denied)"
	else
		avc=$(journalctl -k --no-pager -o cat --since "@$t0" 2>/dev/null | grep -E 'avc: +denied')
	fi
	[ -n "$results" ] && mkdir -p "$results" && printf '%s\n' "$avc" >"$results/avc.txt"
	if [ "$runtime" = systemd ]; then
		# Not a check yet: what the driver's domains were refused while a
		# Custos agent ran in them. From the audit log itself: on the Leap 16
		# VM ausearch -ts boot gives nothing where the log has denials.
		drv=$(cat /var/log/audit/audit.log* 2>/dev/null | grep -E 'avc: +denied' | grep -E 'scontext=[^ ]*:openshell_[a-z_]*_t:')
		echo "    | denials for the driver's domains (openshell_*, audit.log): $(grep -c . <<<"$drv")"
		printf '%s\n' "$drv" | sed -E 's/^.*(avc: +denied)/\1/; s/ pid=[0-9]+//; s/ ino=[0-9]+//' | sort -u | head -n 20 | sed 's/^/    | /'
	fi
	check "no SELinux denial for custos's domains" not grep -qE 'scontext=[^ ]*:(agent_t|agent_relay_t|mcpsrv_agents_t):' <<<"$avc" ||
		grep -E 'scontext=[^ ]*:(agent_t|agent_relay_t|mcpsrv_agents_t):' <<<"$avc" | sed -E 's/^.*(avc: +denied)/\1/' | sort -u | head -n 20 | sed 's/^/    | /'
	echo "    | all denials since the start: $(grep -c 'denied' <<<"$avc")"
	grep 'denied' <<<"$avc" | sed -E 's/^.*(avc: +denied)/\1/; s/ pid=[0-9]+//; s/ ino=[0-9]+//' | sort -u | head -n 15 | sed 's/^/    | /'
fi
systemctl stop osh-fakemodel osh-mcp-gateway osh-opa osh-testidp osh-dnsmasq >/dev/null 2>&1
restore_resolv
if [ "$runtime" = systemd ]; then
	rm -f /etc/pki/trust/anchors/custos-mcp-gateway-ca.pem
	update-ca-certificates >/dev/null 2>&1
fi

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
	[ "$runtime" = systemd ] &&
		journalctl --no-pager --since "@$t0" -u 'openshell-sandbox-*' -u openshell-systemd-helper.service -o short-precise >"$results/sandbox-units.log" 2>&1
fi
[ -n "$system" ] && standins_remove /etc/mcp-gateway/servers.d
n_fail=$(printf '%s\n' "${results_list[@]}" | awk -F'\t' '$2 == "fail"' | wc -l)
printf '\n%d checks, %d failed\n' "${#results_list[@]}" "$n_fail"
exit "$failed"
