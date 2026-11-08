#!/bin/bash
# Sets up the system for the demonstration videos (test/video/tour.js, run
# by test/vm/run-vm.sh with SUITE=video), as root, after
# test/vm/prepare.sh installed the packages. What the Cockpit test sets
# up (test/cockpit/vm-setup.sh: Cockpit, custos-cockpit, the person
# carol, the agents and their activities), and before it what the videos
# show of mcp-gateway besides:
#
#   - its Cockpit page (mcp-gateway-cockpit);
#   - its own approval page, on its HTTPS listener (:18443, a
#     self-signed certificate), whose approvers sign in at the test
#     identity provider (custos-tests' testidp with --login, :18445),
#     as a site's identity provider would;
#   - carol, signed in there, as an owner of patch-pilot (agent-admin
#     owners), so that the page offers her its approvals.
#
# The browser reaches both ports, and Cockpit's, on 127.0.0.1 of the
# host running QEMU (run-vm.sh forwards them), so 127.0.0.1 names the
# gateway and the issuer for the browser and in the VM alike.
#
# Usage: vm-setup.sh RPMDIR PASSWORD; prints the KEY=VALUE lines of the
# Cockpit test's setup.
set -euo pipefail
rpms=${1:?usage: vm-setup.sh RPMDIR PASSWORD}
password=${2:?usage: vm-setup.sh RPMDIR PASSWORD}
here=$(cd "$(dirname "$0")" && pwd)
tests=/usr/libexec/custos/tests
die() { echo "video-setup: $*" >&2; exit 1; }
eventually() {
	local deadline=$((SECONDS + $1))
	shift
	until "$@"; do
		[ "$SECONDS" -ge "$deadline" ] && return 1
		sleep 1
	done
}
issuer=http://127.0.0.1:18445
origin=https://127.0.0.1:18443
# Only the Cockpit setup's KEY=VALUE lines go to stdout.
exec 3>&1 >&2

echo "video-setup: mcp-gateway's Cockpit page" >&2
ls "$rpms"/mcp-gateway-cockpit-*.rpm >/dev/null 2>&1 || die "no mcp-gateway-cockpit package in $rpms"
zypper -n --gpg-auto-import-keys in --no-recommends cockpit >/tmp/zypper-cockpit.log 2>&1 ||
	{ tail -n 20 /tmp/zypper-cockpit.log >&2; die "installing cockpit failed"; }
zypper -n --gpg-auto-import-keys in --no-recommends --allow-unsigned-rpm "$rpms"/mcp-gateway-cockpit-*.rpm >/tmp/zypper-gwpage.log 2>&1 ||
	{ tail -n 20 /tmp/zypper-gwpage.log >&2; die "installing mcp-gateway-cockpit failed"; }
test -f /usr/share/cockpit/mcp-gateway/manifest.json || die "mcp-gateway's page is not installed"

echo "video-setup: the identity provider" >&2
semanage port -a -t http_port_t -p tcp 18445 2>/dev/null || semanage port -m -t http_port_t -p tcp 18445
semanage port -a -t mcp_port_t -p tcp 18443 2>/dev/null || semanage port -m -t mcp_port_t -p tcp 18443
systemctl stop custos-video-idp.service >/dev/null 2>&1 || true
systemd-run --quiet --unit=custos-video-idp.service "$tests/testidp" --listen 0.0.0.0:18445 --issuer "$issuer" \
	--login "approvals=$origin/approvals/callback"
eventually 20 curl -sf -o /dev/null "$issuer/.well-known/openid-configuration" || die "the identity provider does not answer"

echo "video-setup: the gateway's HTTPS listener and approval page" >&2
tls=/etc/mcp-gateway/tls
install -d -m 0750 -o root -g mcp-gateway "$tls"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 7 -subj /CN=127.0.0.1 \
	-addext "subjectAltName=IP:127.0.0.1" -keyout "$tls/video-key.pem" -out "$tls/video-cert.pem" 2>/dev/null ||
	die "making the certificate failed"
chown root:mcp-gateway "$tls"/video-*.pem
chmod 0640 "$tls"/video-*.pem
restorecon -R "$tls"
mkdir -p /etc/mcp-gateway/gateway.d
printf '%s\n' "# custos demonstration videos (test/video/vm-setup.sh)" \
	"http:" '  listen: ":18443"' "  cert_file: $tls/video-cert.pem" "  key_file: $tls/video-key.pem" \
	"  issuer: $issuer" "  audience: $origin/mcp" \
	"approvals:" "  page:" "    client_id: approvals" \
	>/etc/mcp-gateway/gateway.d/60-custos-video.yaml
systemctl restart mcp-gateway.service || {
	journalctl -u mcp-gateway.service -o cat --no-pager -n 20 >&2
	die "the gateway does not start with the HTTPS listener"
}
eventually 30 bash -c "curl -sk -o /dev/null -w '%{http_code}' $origin/approvals/ | grep -qE '^(200|302|303)$'" ||
	die "the gateway's approval page does not answer"

# The agents, activities and the person (the Cockpit test's setup); its
# KEY=VALUE lines go to our caller.
bash "$here/cockpit-vm-setup.sh" "$rpms" "$password" >&3

echo "video-setup: carol, signed in at the identity provider, owns patch-pilot" >&2
agent-admin owners patch-pilot remote-user:carol >/dev/null || die "agent-admin owners failed"

# The agentic loop the videos follow (#149): a service that fails, and
# web-doctor, whose event on it starts an activity at once. The scripted
# model answers the event's prompt with a probe, a diagnosis report and a
# restart as a change (test/vm/fakemodel); the change is in the task
# category config-lifecycle, supervised until promoted.
echo "video-setup: the loop's service and its agent" >&2
printf '%s\n' '[Unit]' 'Description=Shop web front (Custos demonstration)' \
	'[Service]' 'Type=oneshot' \
	"ExecStart=/bin/sh -c 'echo \"shop-web: upstream app.internal:8080 is not answering\" >&2; exit 1'" \
	>/etc/systemd/system/shop-web.service
systemctl daemon-reload
cat >/root/custos-cockpit/web-doctor.yaml <<'YAML'
version: 1
name: web-doctor
description: Looks after the shop's web front.
model: {provider: openai, name: qwen3-32b, endpoint: "http://127.0.0.1:8008/v1"}
instruction: When a service fails, find out why, report it, and propose the fix.
gateway: {servers: [custos-test]}
limits: {max_invocation_seconds: 600}
interaction: {agui: true, client_tools: deny}
events:
  - name: web-down
    on_unit_failure: [shop-web.service]
    prompt: "The unit {unit} failed. Find out why and propose a fix."
    min_interval_seconds: 1
YAML
agent-admin delete web-doctor >/dev/null 2>&1 || true
agent-admin create web-doctor --from /root/custos-cockpit/web-doctor.yaml >/dev/null || die "creating web-doctor failed"
printf '%s\n' '- {server: custos-test, tool: probe, read_only: true}' '- {server: custos-test, tool: change, category: config-lifecycle}' |
	agent-admin permissions web-doctor >/dev/null || die "web-doctor's permissions"
agent-admin owners web-doctor remote-user:carol >/dev/null || die "web-doctor's owners"
eventually 30 test -S /run/agents/web-doctor/api.sock || die "web-doctor has no socket"
systemctl show -p OnFailure --value shop-web.service | grep -q 'agent-event@web-doctor_web-down' ||
	die "shop-web.service has no OnFailure= for web-doctor"
# Readiness the demonstration can reach in minutes; the videos say so.
# (The defaults: 14 days, 50 proposals, at most 5 % overrides and 2 %
# incidents.)
printf '%s\n' '# custos demonstration videos (test/video/vm-setup.sh): reachable in minutes' \
	'observation_days: 0' 'proposals: 1' 'override_rate: 0.5' 'incident_rate: 0.5' 'window_days: 30' \
	'diagnostics_feedback: 0' 'diagnostics_confirmed_rate: 0' >/etc/custos/promotion.yaml
