#!/bin/bash
# Prepares the CI VM for the functional tests (run-vm.sh runs it as root):
# the tests' dependencies, OPA, mcp-gateway's packages and the packages
# built from this tree, custos-tests among them. On a product
# system (openQA), the packages come from its repositories instead.
#
# Usage: prepare.sh <dir> [all|deps|packages], with <dir>/rpms (the
# packages) and <dir>/opa (OPA, used if the distribution has no opa
# package).
#   all       (default) both, below
#   deps      the dependencies and OPA, from the network
#   packages  mcp-gateway's and ours from <dir>/rpms, without the network
#             (the air-gapped gate's second boot, test/airgap)
set -euo pipefail
dir=$1
mode=${2:-all}
die() {
	echo "FATAL: $*"
	exit 1
}

case $mode in
all | deps | packages) ;;
*) die "mode $mode: want all, deps or packages" ;;
esac

if [ "$mode" != packages ]; then
	# The image's repositories that the tests do not need: the codec
	# repository (codecs.opensuse.org) is outside openSUSE's mirrors, and its
	# outages failed the refresh. Absent in other images: nothing to do.
	zypper -n mr -d repo-openh264 >/dev/null 2>&1 || true
	zypper -n --gpg-auto-import-keys ref >/tmp/zypper.log 2>&1 ||
		{ tail -n 30 /tmp/zypper.log; die "refreshing the repositories failed"; }
	# policycoreutils-python-utils: the %selinux_* macros of the -selinux packages.
	deps="audit policycoreutils policycoreutils-python-utils selinux-tools polkit jq curl util-linux shadow nftables"
	# shellcheck disable=SC2086 # deps is a list of package names
	zypper -n in --no-recommends $deps selinux-policy-targeted >/tmp/zypper.log 2>&1 ||
		{ tail -n 30 /tmp/zypper.log; die "installing dependencies failed"; }
	if zypper -n in --no-recommends opa >/dev/null 2>&1; then
		echo "  opa: distribution package"
	else
		install -m 0755 "$dir/opa" /usr/bin/opa
		restorecon /usr/bin/opa
		echo "  opa: release binary ($(opa version | head -1))"
	fi
fi
[ "$mode" = deps ] && exit 0

# mcp-gateway first (its SELinux module defines the interfaces ours uses),
# then custos. Nothing here needs the network: the packages are files, and
# zypper (--no-remote) resolves custos's dependencies against what is
# installed. mcp-gateway needs --nodeps for its opa, a binary above.
rpm -Uvh --nodeps "$dir"/rpms/mcp-gateway-[0-9]*.rpm "$dir"/rpms/mcp-gateway-selinux-*.rpm ||
	die "installing mcp-gateway failed"
# The exec server, for the shipped diagnostic commands (if it was built).
for f in "$dir"/rpms/mcp-gateway-exec-server-*.rpm; do
	[ -f "$f" ] && { rpm -Uvh --nodeps "$f" || die "installing mcp-gateway-exec-server failed"; }
done
# The server restarts, for update-status's services to restart (if built).
for f in "$dir"/rpms/mcp-gateway-restarts-server-*.rpm; do
	[ -f "$f" ] && { rpm -Uvh --nodeps "$f" || die "installing mcp-gateway-restarts-server failed"; }
done
zypper -n --no-remote --no-refresh in --no-recommends --allow-unsigned-rpm \
	"$dir"/rpms/custos-[0-9]*.rpm "$dir"/rpms/custos-selinux-*.rpm "$dir"/rpms/custos-tests-*.rpm "$dir"/rpms/custos-diagnostics-*.rpm \
	>/tmp/zypper-custos.log 2>&1 ||
	{ tail -n 30 /tmp/zypper-custos.log; die "installing custos failed"; }
semodule -l | grep -qx custos || die "the custos SELinux module is not loaded"
echo "  installed: $(rpm -q mcp-gateway custos custos-selinux custos-tests | tr '\n' ' ')"
