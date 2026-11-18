#!/bin/bash
# Sets up NVIDIA OpenShell in the Leap 16 test VM (test/vm/run-vm.sh with
# SUITE=openshell) as its RPM intends: rootless Podman, the gateway a
# systemd user service of an ordinary user (osh, lingering), and that
# gateway registered for root too, who runs agent-admin and the relays
# (CONTRACT.md §16a). Run as root, after test/vm/prepare.sh.
#
#   vm-setup.sh [--driver systemd DRIVER-RPMS]
#
# --driver systemd (#215): OpenShell's gateway uses the OpenShell systemd
# compute driver instead of Podman, as the driver's guide sets it up: its
# packages from DRIVER-RPMS, the OpenShell release of
# test/openshell/systemd-driver, the helper serving osh, the driver a
# service of osh, and the gateway's gateway.toml naming its socket.
set -euo pipefail
user=osh
driver=podman driver_rpms=
case ${1:-} in
--driver) [ "${2:-}" = systemd ] && [ -d "${3:-}" ] || { echo "usage: $0 [--driver systemd DRIVER-RPMS]" >&2; exit 2; }
	driver=systemd driver_rpms=$3 ;;
"") ;;
*) echo "usage: $0 [--driver systemd DRIVER-RPMS]" >&2; exit 2 ;;
esac
log() { echo "[vm-setup] $*"; }

log "Podman"
zypper -n in --no-recommends podman curl jq audit dnsmasq >/tmp/zypper-osh.log 2>&1 ||
	{ tail -n 30 /tmp/zypper-osh.log; exit 1; }

log "cgroup controllers for user sessions (rootless Podman sets memory and swap limits)"
# Rootless Podman needs the controllers delegated to the user's systemd;
# OpenShell's sandboxes get memory limits (memory.max, memory.swap.max).
mkdir -p /etc/systemd/system/user@.service.d
printf '[Service]\nDelegate=cpu cpuset io memory pids\n' >/etc/systemd/system/user@.service.d/delegate.conf
systemctl daemon-reload

log "user $user (rootless Podman, lingering)"
id "$user" >/dev/null 2>&1 || useradd -m "$user"
grep -q "^$user:" /etc/subuid || usermod --add-subuids 200000-265535 --add-subgids 200000-265535 "$user"
loginctl enable-linger "$user"
uid=$(id -u "$user")
for _ in $(seq 60); do [ -d "/run/user/$uid" ] && break; sleep 1; done
[ -d "/run/user/$uid" ] || { echo "no user manager for $user" >&2; exit 1; }
# A user manager started before the drop-in runs without the delegation.
systemctl restart "user@$uid.service"
sleep 2
d=/sys/fs/cgroup/user.slice/user-$uid.slice/user@$uid.service
log "controllers for $user: $(cat "$d/cgroup.controllers" 2>&1); swap limit: $([ -e "$d/memory.swap.max" ] && echo yes || echo no)"

# The OpenShell release the tests use (test/openshell/openshell-version),
# not whichever is newest: an update is a change of its own, and a failed
# lookup of the latest release no longer fails the tests.
osh_version=$(head -n1 "$(dirname "$0")/openshell-version")
[ "$driver" = systemd ] && osh_version=$(. "$(dirname "$0")/systemd-driver" && echo "$OPENSHELL_VERSION")
log "OpenShell $osh_version (its installer, for $user)"
curl -LsSf --retry 3 --retry-all-errors "https://raw.githubusercontent.com/NVIDIA/OpenShell/$osh_version/install.sh" -o /tmp/openshell-install.sh
for attempt in 1 2 3; do
	OPENSHELL_VERSION=$osh_version SUDO_USER=$user sh /tmp/openshell-install.sh && break
	[ "$attempt" = 3 ] && { echo "installing OpenShell $osh_version failed" >&2; exit 1; }
	sleep 20
done
openshell --version | grep -qF "${osh_version#v}" || { echo "OpenShell is not $osh_version: $(openshell --version)" >&2; exit 1; }
openshell --version

log "the gateway registration for root"
mkdir -p /root/.config
rm -rf /root/.config/openshell
cp -a "/home/$user/.config/openshell" /root/.config/
# Root's own copy: the relay reads it under SELinux without dac_override,
# which a file of the user's (the mTLS key, 0600) would need.
chown -R root:root /root/.config/openshell
openshell gateway list
openshell status

if [ "$driver" = systemd ]; then
	# The driver's guide, chapter 2: its packages, the helper for the
	# gateway's user, the driver as that user's service with the CA the
	# supervisors verify the gateway with, and gateway.toml.
	log "the systemd compute driver ($(ls "$driver_rpms"))"
	zypper -n --no-gpg-checks in --allow-unsigned-rpm --no-recommends \
		$(ls "$driver_rpms"/*.rpm | grep -v -- '-tests-') >/tmp/zypper-driver.log 2>&1 ||
		{ tail -n 30 /tmp/zypper-driver.log; exit 1; }
	rpm -q openshell-driver-systemd openshell-driver-systemd-selinux
	mkdir -p /etc/systemd/system/openshell-systemd-helper.service.d
	printf '[Service]\nEnvironment=OPENSHELL_SYSTEMD_HELPER_ALLOW_USER=%s\n' "$user" \
		>/etc/systemd/system/openshell-systemd-helper.service.d/user.conf
	systemctl daemon-reload
	systemctl enable --now openshell-systemd-helper.service
	ca=$(find "/home/$user/.local" "/home/$user/.config" -name ca.crt 2>/dev/null | head -n 1)
	[ -n "$ca" ] || { echo "no CA certificate of the OpenShell gateway" >&2; exit 1; }
	install -m 0644 "$ca" /etc/openshell-gateway-ca.crt
	asuser() { runuser -l "$user" -c "export XDG_RUNTIME_DIR=/run/user/$uid; $*"; }
	asuser "mkdir -p ~/.config/systemd/user/openshell-driver-systemd.service.d && printf '[Service]\nEnvironment=OPENSHELL_SYSTEMD_TLS_CA=/etc/openshell-gateway-ca.crt\nEnvironment=OPENSHELL_GATEWAY_NAME=openshell\n' >~/.config/systemd/user/openshell-driver-systemd.service.d/ca.conf"
	asuser "systemctl --user daemon-reload && systemctl --user enable --now openshell-driver-systemd.service"
	sock=/run/user/$uid/openshell/systemd-driver.sock
	for _ in $(seq 30); do [ -S "$sock" ] && break; sleep 1; done
	[ -S "$sock" ] || { asuser "journalctl --user -u openshell-driver-systemd -n 20 --no-pager"; exit 1; }
	cfg=/home/$user/.config/openshell/gateway.toml
	sed -i 's/^compute_driver = .*/compute_driver = "systemd"/' "$cfg"
	grep -q '^compute_driver = "systemd"' "$cfg" || { echo "no compute_driver line in $cfg:" >&2; cat "$cfg" >&2; exit 1; }
	printf '\n[openshell.drivers.systemd]\nsocket_path = "%s"\n' "$sock" >>"$cfg"
	asuser "systemctl --user restart openshell-gateway.service"
	for _ in $(seq 30); do openshell status >/dev/null 2>&1 && break; sleep 2; done
	openshell status
	info=$(openshell gateway info 2>&1) || true
	echo "$info"
	grep -q openshell-driver-systemd <<<"$info" || { echo "the gateway does not name the systemd driver" >&2; exit 1; }
fi
log "SELinux: $(getenforce)"
