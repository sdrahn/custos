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

# What is installation stays here (the packages, the user, OpenShell's
# installer, which needs the user's service manager: lingering); the rest
# is agent-admin openshell-setup's (#225), at the end.
log "user $user (lingering)"
id "$user" >/dev/null 2>&1 || useradd -m "$user"
loginctl enable-linger "$user"
uid=$(id -u "$user")
for _ in $(seq 60); do [ -d "/run/user/$uid" ] && break; sleep 1; done
[ -d "/run/user/$uid" ] || { echo "no user manager for $user" >&2; exit 1; }

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

if [ "$driver" = systemd ]; then
	log "the systemd compute driver's packages ($(ls "$driver_rpms"))"
	zypper -n --no-gpg-checks in --allow-unsigned-rpm --no-recommends \
		$(ls "$driver_rpms"/*.rpm | grep -v -- '-tests-') >/tmp/zypper-driver.log 2>&1 ||
		{ tail -n 30 /tmp/zypper-driver.log; exit 1; }
	rpm -q openshell-driver-systemd openshell-driver-systemd-selinux
fi

log "agent-admin openshell-setup (the $driver driver)"
setup_driver=container
[ "$driver" = systemd ] && setup_driver=systemd
agent-admin openshell-setup --user "$user" --driver "$setup_driver" --check
agent-admin openshell-setup --user "$user" --driver "$setup_driver"
# Run again, it finds everything as it should be.
again=$(agent-admin openshell-setup --user "$user" --driver "$setup_driver")
echo "$again"
! grep -q '^set ' <<<"$again" || { echo "openshell-setup changed something on its second run" >&2; exit 1; }
d=/sys/fs/cgroup/user.slice/user-$uid.slice/user@$uid.service
log "controllers for $user: $(cat "$d/cgroup.controllers" 2>&1); swap limit: $([ -e "$d/memory.swap.max" ] && echo yes || echo no)"
openshell gateway list
openshell status
log "SELinux: $(getenforce)"
