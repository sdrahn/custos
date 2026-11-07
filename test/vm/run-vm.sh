#!/bin/bash
# Boots an openSUSE cloud image with QEMU/KVM, installs the packages built
# from this tree (test/vm/prepare.sh) and runs the functional tests of
# custos-tests in it as root, as openQA does on a product system:
# /usr/libexec/custos/tests/run-tests.sh (test/vm/run-tests.sh).
# Adapted from mcp-gateway's test/vm/run-vm.sh.
#
# Usage: test/vm/run-vm.sh <rpm dir> <opa binary>
#
# Environment:
#   SUITE      functional (default): test/vm/run-tests.sh; openshell: the
#              OpenShell backend with rootless Podman and SELinux
#              enforcing (test/openshell/vm-setup.sh, then
#              test/openshell/run-spike.sh --system); cockpit: the
#              Cockpit page in a real Cockpit (test/cockpit/vm-setup.sh in
#              the VM, then test/cockpit/vm-test.js here, with node and
#              playwright-core; Cockpit's port 9090 is forwarded to
#              127.0.0.1:19090; SHOTS=DIR saves the page's screenshots);
#              video: the demonstration videos (test/video/vm-setup.sh in
#              the VM, then test/video/tour.js here for each version and
#              test/video/render.sh; also forwards the gateway's HTTPS
#              port 18443 and the test identity provider's 18445; the
#              videos and subtitles go to VIDEOS, default $WORK/videos);
#              airgap: the air-gapped release gate (CONTRACT.md O15): the
#              first boot installs the dependencies, the second has no
#              network (QEMU's restrict=on: only the forwarded ssh) and
#              installs the packages, Ollama and the model from the
#              offline mirror MIRROR (test/airgap/mirror.sh), then runs
#              the functional tests with --model against it
#   MIRROR     airgap: the offline mirror's directory
#   IMAGE_URL  cloud image (default: openSUSE Tumbleweed Minimal-VM, Cloud)
#   WORK       working directory (default: a new temporary directory); the
#              serial console log, test output and results (TAP, JUnit;
#              injection-*.json, the prompt-injection evaluation)
#              are left there
#
# If SELinux is not enforcing in the image, it is installed and enabled
# first (with a relabelling reboot). Needs qemu-system-x86_64, qemu-img,
# cloud-localds (cloud-image-utils), ssh and /dev/kvm.
set -euo pipefail

[ $# = 2 ] || {
	echo "usage: $0 <rpm dir> <opa binary>" >&2
	exit 2
}
rpms=$(realpath "$1")
opa=$(realpath "$2")
here=$(cd "$(dirname "$0")" && pwd)
image_url=${IMAGE_URL:-https://download.opensuse.org/distribution/leap/16.0/appliances/Leap-16.0-Minimal-VM.x86_64-Cloud.qcow2}
work=${WORK:-$(mktemp -d)}
mkdir -p "$work"
port=2222
log() { echo "[$(date +%T)] $*"; }

[ -w /dev/kvm ] || {
	echo "/dev/kvm is not available or not writable" >&2
	exit 1
}

log "work directory: $work"
# download_image: the image to $work/base.qcow2, failing fast on a
# stalled or crawling transfer (download.opensuse.org redirects to a
# mirror, and a slow one can take most of the job's time): each of three
# attempts gives up below 1 MB/s for a minute or after 10 minutes, well
# within the job's hour. An image from download.opensuse.org is tried
# there first, then from the origin server (no redirect), then from a
# full mirror. A partial file is never taken for the image; an image
# already there (the CI cache) is used as it is.
download_image() {
	local url urls=("$image_url")
	case $image_url in
	https://download.opensuse.org/*)
		urls+=("https://downloadcontent.opensuse.org/${image_url#https://download.opensuse.org/}"
			"https://ftp.gwdg.de/pub/opensuse/${image_url#https://download.opensuse.org/}") ;;
	*) urls+=("$image_url" "$image_url") ;;
	esac
	for url in "${urls[@]}"; do
		log "downloading $url"
		if curl -fsSL --connect-timeout 30 --speed-limit 1000000 --speed-time 60 --max-time 600 \
			-o "$work/base.qcow2.part" "$url" &&
			qemu-img info "$work/base.qcow2.part" >/dev/null; then
			mv "$work/base.qcow2.part" "$work/base.qcow2"
			log "downloaded $(du -h "$work/base.qcow2" | cut -f1)"
			return 0
		fi
		rm -f "$work/base.qcow2.part"
		sleep 10
	done
	echo "could not download $image_url" >&2
	return 1
}

if [ -f "$work/base.qcow2" ] && qemu-img info "$work/base.qcow2" >/dev/null 2>&1; then
	log "image: $work/base.qcow2 (cached)"
else
	rm -f "$work/base.qcow2"
	download_image
fi
qemu-img create -q -f qcow2 -b "$work/base.qcow2" -F qcow2 "$work/disk.qcow2" 20G

ssh-keygen -q -t ed25519 -N '' -f "$work/key" <<<y >/dev/null
cat >"$work/user-data" <<EOF
#cloud-config
disable_root: false
ssh_pwauth: false
users:
  - name: root
    ssh_authorized_keys:
      - $(cat "$work/key.pub")
growpart:
  mode: auto
  devices: ["/"]
EOF
printf 'instance-id: custos-vmtest\nlocal-hostname: custos-vmtest\n' >"$work/meta-data"
cloud-localds "$work/seed.img" "$work/user-data" "$work/meta-data"

ssh_opts=(-i "$work/key" -p "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
	-o LogLevel=ERROR -o ConnectTimeout=5 -o ServerAliveInterval=15)
vm() { ssh "${ssh_opts[@]}" root@127.0.0.1 "$@"; }

# The videos' browser reaches the gateway's approval page and the
# identity provider by the same 127.0.0.1 address as the VM does.
extra_fwd=
[ "${SUITE:-functional}" = video ] && extra_fwd=,hostfwd=tcp:127.0.0.1:18443-:18443,hostfwd=tcp:127.0.0.1:18445-:18445
# boot [restrict]: starts the VM; with restrict, it has no network but
# the forwarded ports (QEMU user networking's restrict=on).
boot() {
	local restrict=${1:+,restrict=on}
	log "booting the VM${1:+ without network}"
	qemu-system-x86_64 -enable-kvm -cpu host -smp 2 -m 4096 \
		-drive file="$work/disk.qcow2",if=virtio \
		-drive file="$work/seed.img",if=virtio,format=raw \
		-nic user,model=virtio-net-pci$restrict,hostfwd=tcp:127.0.0.1:$port-:22,hostfwd=tcp:127.0.0.1:19090-:9090$extra_fwd \
		-display none -serial file:"$work/console.log" \
		-pidfile "$work/qemu.pid" -daemonize
}
boot
trap 'kill "$(cat "$work/qemu.pid" 2>/dev/null)" 2>/dev/null || true' EXIT

wait_ssh() {
	local i
	for i in $(seq 120); do
		if vm true 2>/dev/null; then
			log "ssh is up"
			return
		fi
		sleep 5
	done
	echo "the VM did not come up; console:" >&2
	tail -50 "$work/console.log" >&2
	exit 1
}
wait_ssh
vm 'cloud-init status --wait >/dev/null 2>&1 || true'

if [ "$(vm 'getenforce 2>/dev/null || echo Missing')" != Enforcing ]; then
	log "enabling SELinux (state: $(vm 'getenforce 2>/dev/null || echo missing'))"
	vm bash -s <"$here/enable-selinux.sh"
	vm 'systemctl reboot' || true
	sleep 20
	wait_ssh
	# The first boot may relabel and reboot once more.
	for _ in $(seq 30); do
		[ "$(vm 'getenforce 2>/dev/null' || true)" = Enforcing ] && break
		sleep 10
		wait_ssh
	done
fi

log "copying the test payload"
vm 'rm -rf /root/custos-vm && mkdir -p /root/custos-vm/rpms'
scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
	"$here/prepare.sh" "$opa" root@127.0.0.1:/root/custos-vm/
scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
	"$rpms"/*.rpm root@127.0.0.1:/root/custos-vm/rpms/

args="--disposable --results /root/custos-vm/results"
if [ "${SUITE:-functional}" = airgap ]; then
	[ -n "${MIRROR:-}" ] && [ -f "$MIRROR/ollama.tar" ] && [ -f "$MIRROR/models.tar" ] ||
		{ echo "SUITE=airgap needs MIRROR with ollama.tar and models.tar (test/airgap/mirror.sh)" >&2; exit 1; }
	log "first boot: the dependencies, with network"
	vm 'bash /root/custos-vm/prepare.sh /root/custos-vm deps' 2>&1 | tee "$work/prepare.log"
	[ "${PIPESTATUS[0]}" = 0 ] || exit 1
	log "the offline mirror and the gate's setup"
	vm 'mkdir -p /root/custos-vm/mirror'
	scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
		"$here/../airgap/versions" "$here/../airgap/vm-setup.sh" root@127.0.0.1:/root/custos-vm/
	scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
		"$MIRROR/ollama.tar" "$MIRROR/models.tar" root@127.0.0.1:/root/custos-vm/mirror/
	vm 'systemctl poweroff' || true
	for _ in $(seq 60); do kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null || break; sleep 2; done
	boot restrict
	wait_ssh
	vm 'bash /root/custos-vm/vm-setup.sh /root/custos-vm' 2>&1 | tee "$work/airgap-setup.log"
	if [ "${PIPESTATUS[0]}" != 0 ]; then
		log "setting up the air-gapped system failed"
		exit 1
	fi
	model=$(. "$here/../airgap/versions" && echo "$MODEL")
	log "running the tests, without network, with $model"
	set +e
	vm "/usr/libexec/custos/tests/run-tests.sh $args --model http://127.0.0.1:11434/v1 '$model'" 2>&1 | tee "$work/test.log"
	status=${PIPESTATUS[0]}
	set -e
	# What tried to leave the host: an agent's attempt fails the gate.
	vm "journalctl -k -b --no-pager -o short-precise | grep 'custos-egress' || true" >"$work/egress.txt" 2>&1
	if grep -q 'custos-egress agent:' "$work/egress.txt"; then
		log "an agent tried to reach a host outside (egress.txt):"
		grep 'custos-egress agent:' "$work/egress.txt" | head -20
		status=1
	fi
	log "other processes' attempts to leave the host: $(grep -c 'custos-egress other:' "$work/egress.txt" || true) (egress.txt)"
	scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
		root@127.0.0.1:/root/custos-vm/results/results.tap root@127.0.0.1:/root/custos-vm/results/results.xml \
		root@127.0.0.1:/root/custos-vm/results/avc.txt root@127.0.0.1:/root/custos-vm/results/journal.txt "$work/" || true
	# The prompt-injection evaluation (O14): how often each model followed.
	scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
		'root@127.0.0.1:/root/custos-vm/results/injection-*.json' "$work/" || true
	log "tests exited with $status"
	exit "$status"
fi
log "installing the packages"
vm 'bash /root/custos-vm/prepare.sh /root/custos-vm' 2>&1 | tee "$work/prepare.log"
[ "${PIPESTATUS[0]}" = 0 ] || exit 1
log "running the tests"
set +e
if [ "${SUITE:-functional}" = openshell ]; then
	vm 'mkdir -p /root/custos-vm/src/test/openshell'
	scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
		"$here/../openshell/run-spike.sh" "$here/../openshell/vm-setup.sh" "$here/../openshell/openshell-version" \
		root@127.0.0.1:/root/custos-vm/src/test/openshell/
	vm 'bash /root/custos-vm/src/test/openshell/vm-setup.sh' 2>&1 | tee "$work/openshell-setup.log"
	if [ "${PIPESTATUS[0]}" != 0 ]; then
		log "setting up OpenShell failed"
		exit 1
	fi
	vm "OPENSHELL_GATEWAY_USER=osh bash /root/custos-vm/src/test/openshell/run-spike.sh --system $args" 2>&1 | tee "$work/test.log"
elif [ "${SUITE:-functional}" = cockpit ]; then
	password=$(head -c 12 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')
	scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
		"$here/../cockpit/vm-setup.sh" root@127.0.0.1:/root/custos-vm/
	vm "bash /root/custos-vm/vm-setup.sh /root/custos-vm/rpms '$password'" >"$work/cockpit-setup.env" 2>"$work/cockpit-setup.log"
	setup=$?
	cat "$work/cockpit-setup.log"
	if [ "$setup" != 0 ]; then
		log "setting up the Cockpit test failed"
		exit 1
	fi
	cat "$work/cockpit-setup.env"
	ssh_json=$(printf '%s\n' ssh "${ssh_opts[@]}" root@127.0.0.1 | jq -R . | jq -sc .)
	(set -a; . "$work/cockpit-setup.env"; set +a
		COCKPIT_URL=https://127.0.0.1:19090 COCKPIT_USER=carol COCKPIT_PASSWORD=$password SSH=$ssh_json \
			node "$here/../cockpit/vm-test.js") 2>&1 | tee "$work/test.log"
	status=${PIPESTATUS[0]}
	vm 'journalctl -b --no-pager -o short-precise -u cockpit.service -u mcp-gateway.service -t agent-patch-pilot -t agent-triage -t agent-disk-watch' >"$work/journal.txt" 2>&1
	log "tests exited with $status"
	exit "$status"
elif [ "${SUITE:-functional}" = video ]; then
	password=$(head -c 12 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')
	videos=${VIDEOS:-$work/videos}
	mkdir -p "$videos"
	scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
		"$here/../video/vm-setup.sh" root@127.0.0.1:/root/custos-vm/
	scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
		"$here/../cockpit/vm-setup.sh" root@127.0.0.1:/root/custos-vm/cockpit-vm-setup.sh
	vm "bash /root/custos-vm/vm-setup.sh /root/custos-vm/rpms '$password'" >"$work/video-setup.env" 2>"$work/video-setup.log"
	setup=$?
	cat "$work/video-setup.log"
	if [ "$setup" != 0 ]; then
		log "setting up the videos' system failed"
		exit 1
	fi
	ssh_json=$(printf '%s\n' ssh "${ssh_opts[@]}" root@127.0.0.1 | jq -R . | jq -sc .)
	status=0
	for version in business technical; do
		log "recording the $version video"
		(set -a; . "$work/video-setup.env"; set +a
			COCKPIT_URL=https://127.0.0.1:19090 COCKPIT_USER=carol COCKPIT_PASSWORD=$password SSH=$ssh_json \
				GATEWAY_URL=https://127.0.0.1:18443 node "$here/../video/tour.js" "$version" "$videos") 2>&1 | tee -a "$work/test.log"
		[ "${PIPESTATUS[0]}" = 0 ] || status=1
		if [ -s "$videos/$version.webm" ]; then
			"$here/../video/render.sh" "$videos" "$version" 2>&1 | tee -a "$work/test.log"
			[ "${PIPESTATUS[0]}" = 0 ] || status=1
		else
			status=1
		fi
	done
	vm 'journalctl -b --no-pager -o short-precise -u cockpit.service -u mcp-gateway.service -u custos-video-idp.service -t agent-patch-pilot -t agent-triage' >"$work/journal.txt" 2>&1
	log "recording exited with $status"
	exit "$status"
else
	vm "/usr/libexec/custos/tests/run-tests.sh $args" 2>&1 | tee "$work/test.log"
fi
status=${PIPESTATUS[0]}
scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
	root@127.0.0.1:/root/custos-vm/results/results.tap root@127.0.0.1:/root/custos-vm/results/results.xml \
	root@127.0.0.1:/root/custos-vm/results/avc.txt root@127.0.0.1:/root/custos-vm/results/journal.txt "$work/"
scp -q -i "$work/key" -P "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
	'root@127.0.0.1:/root/custos-vm/results/injection-*.json' "$work/" || true
set -e
log "tests exited with $status"
exit "$status"
