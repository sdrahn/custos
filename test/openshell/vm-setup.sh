#!/bin/bash
# Sets up NVIDIA OpenShell in the Leap 16 test VM (test/vm/run-vm.sh with
# SUITE=openshell) as its RPM intends: rootless Podman, the gateway a
# systemd user service of an ordinary user (osh, lingering), and that
# gateway registered for root too, who runs agent-admin and the relays
# (CONTRACT.md §16a). Run as root, after test/vm/prepare.sh.
set -euo pipefail
user=osh
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
openshell gateway list
openshell status
log "SELinux: $(getenforce)"
