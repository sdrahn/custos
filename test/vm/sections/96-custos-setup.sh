# shellcheck shell=bash
# custos-setup, the bundled diagnostics install (CONTRACT.md §17a).
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section "custos-setup (the bundled diagnostics install)"
# CONTRACT.md §17a: what the installer runs on a fresh host. The host is
# fresh again here: every test agent is gone, and so is the enablement
# state.
if command -v custos-setup >/dev/null; then
	agent-admin delete health-check >/dev/null 2>&1
	printf 'sk-test-key\n' >"$work/model-key"
	setup() { custos-setup --diagnostics-only --backend on-host --model fake --endpoint http://127.0.0.1:8008/v1 --credential-file "$work/model-key" "$@"; }
	check "custos-setup without --diagnostics-only writes nothing" bash -c \
		"custos-setup --backend on-host --model fake | grep -q 'nothing to set up' && ! test -e /etc/mcp-gateway/policy/agents/_enablement.yaml"
	check "custos-setup refuses a backend that is not one of the four" not custos-setup --diagnostics-only --backend cloud --model fake
	setup_once() { setup >"$work/setup.out"; }
	check "custos-setup --diagnostics-only --backend on-host" setup_once
	sed 's/^/  /' "$work/setup.out"
	check "the host is diagnostics-only, by custos-setup" enablement_is '.state == "diagnostics-only" and (.changed_by | startswith("custos-setup "))'
	check "the model defaults are the backend's" bash -c "agent-admin defaults | jq -e '.model | .provider == \"openai\" and .name == \"fake\" and .credential == \"model-key\"' >/dev/null"
	check "health-check: supervised, read-only in diagnostics" bash -c \
		"agent-admin show health-check | jq -e '.agents[\"health-check\"] | .mode == \"supervised\" and all(.permissions[]; .read_only == true and .category == \"diagnostics\")' >/dev/null"
	check "its key is stored, root's alone" stat_is /etc/credstore.encrypted/agent-health-check.model-key root:root:600
	check "health-check starts with it and answers" eventually 30 bash -c "$(declare -f api); api health-check /v1/status | jq -e '.contract == 1' >/dev/null"
	if [ -d /etc/mcp-gateway/exec.d ]; then
		check "the playbooks' exec commands are installed" test -f /etc/mcp-gateway/exec.d/exec.d-diagnostics.yaml -a -f /etc/mcp-gateway/exec.d/exec.d-disk-usage.yaml
		check "custos-setup stopped the exec server's running instances, for the new commands" grep -q 'exec instances: done' "$work/setup.out"
		# Each shipped command runs through the gateway, as the agent, in
		# the exec server's domain (mcpsrv_exec_t): the gateway sees it
		# come back ok (mcp-result), with no SELinux denial.
		exec_t0=$(date +%s)
		exec_failed=
		for tool in $(sed -n 's/^  \([a-z_]*\):$/\1/p' /usr/share/custos/templates/exec.d-diagnostics.yaml /usr/share/custos/templates/exec.d-disk-usage.yaml); do
			answer=$(run_activity health-check "CALL exec__$tool")
			# rpm -Va exits 1 when it finds something: here opa, which the
			# test host has as a binary, not as the package mcp-gateway
			# requires. A finding is the command working.
			if [ "$tool" = package_dependencies ] && jq -e '.summary | contains("is needed by")' <<<"$answer" >/dev/null 2>&1; then
				check "health-check runs exec__package_dependencies: it reports the unsatisfied dependencies" true
				continue
			fi
			check "health-check runs exec__$tool: ok, as the gateway saw it" eventually 10 gw_audit_has \
				".event == \"mcp-result\" and .sub == \"agent-health-check\" and .server == \"exec\" and .name == \"$tool\" and .outcome == \"ok\"" || {
				echo "    | the activity: $(jq -c '{state, summary, reason}' <<<"$answer" 2>/dev/null || echo "$answer")"
				journalctl -u mcp-gateway.service -o cat --no-pager --since "@$exec_t0" | grep '"audit":true' |
					jq -c "select(.sub == \"agent-health-check\" and (.name // \"\") == \"$tool\")" 2>/dev/null | tail -3 | sed 's/^/    | /'
				exec_failed="$exec_failed $tool"
			}
		done
		# reboot_status (update-status): whether a reboot is pending, and
		# the running kernel among the installed ones, as the agent's user.
		# reboot_says <start> [<or>]: its stdout starts so (or has <or>) and
		# names the kernels.
		reboot_says() {
			local a
			a=$(run_activity health-check "CALL exec__reboot_status")
			# The command's stdout, from the whole response (the summary
			# is cut short, and repeats the script's own text), which
			# GET /v1/activities/{id} has.
			a=$(api health-check "/v1/activities/$(jq -r .id <<<"$a")")
			jq -e --arg k "$(uname -r)" --arg start "$1" --arg or "${2:-}" '(.response // "" | ltrimstr("done: ") | fromjson | .output.stdout) |
				(startswith($start) or ($or != "" and contains($or))) and contains("Running kernel: " + $k + "\n") and
				(split("Installed kernels: ")[1] | split(" ") | index($k) != null)' <<<"$a" >/dev/null ||
				{ echo "    | the activity: $(jq -c '{state, response: (.response // "" | .[-400:])}' <<<"$a" 2>/dev/null || echo "$a")"; return 1; }
		}
		if [ -e /run/reboot-needed ]; then
			check "exec__reboot_status: a reboot is pending, with the running kernel among the installed ones" reboot_says "A reboot is pending"
		else
			check "exec__reboot_status: no reboot is pending, with the running kernel among the installed ones" reboot_says "No reboot is pending"
			# /run/reboot-needed, created now, after the exec server's
			# instance started: the command sees it (mcp-gateway 1.16).
			touch /run/reboot-needed
			restorecon /run/reboot-needed 2>/dev/null || true
			check "exec__reboot_status sees /run/reboot-needed: a reboot is pending" reboot_says "A reboot is pending: /run/reboot-needed exists"
			rm -f /run/reboot-needed
		fi
		# mcp-gateway's server restarts (update-status's services to
		# restart), which custos-setup --restarts links when its package is there.
		if [ -e /usr/share/mcp-gateway/profiles/restarts.yaml ]; then
			# It holds CAP_SYS_PTRACE: linked only when asked for
			# (--restarts, security review 0.6, P1).
			check "custos-setup without --restarts does not link the server restarts" bash -c \
				"grep -q 'server restarts: skipped (not offered' '$work/setup.out' && ! test -e /etc/mcp-gateway/servers.d/restarts.yaml"
			restarts_linked() {
				setup --restarts | grep -q 'server restarts: done' &&
					test "$(readlink /etc/mcp-gateway/servers.d/restarts.yaml)" = /usr/share/mcp-gateway/profiles/restarts.yaml
			}
			check "custos-setup --restarts links the server restarts" restarts_linked
			restarts_answers() { # through the gateway, as health-check, without an approval: it names the running kernel
				local a
				a=$(run_activity health-check "CALL restarts__pending_restarts")
				a=$(api health-check "/v1/activities/$(jq -r .id <<<"$a")")
				jq -e --arg k "$(uname -r)" '.state == "finished" and (.response // "" | startswith("done: ") and contains($k) and (contains("\"error\"") | not))' <<<"$a" >/dev/null ||
					{ echo "    | the activity: $(jq -c '{state, response: (.response // "" | .[0:400])}' <<<"$a" 2>/dev/null || echo "$a")"; return 1; }
			}
			check "restarts__pending_restarts answers health-check, naming the running kernel" restarts_answers
		else
			skip "the server restarts (mcp-gateway-restarts-server is not installed)"
		fi
		# Custos's server custos-net: the network commands in the host's
		# network namespace (network_view: host, mcp-gateway R7), which
		# the exec server's instances do not see.
		check "custos-setup installed the server custos-net" grep -q 'server custos-net: done' "$work/setup.out"
		net_has() { # net_has <tool> <text>: the tool's answer, through the gateway, has the text
			local a
			a=$(run_activity health-check "CALL custos-net__$1")
			jq -e --arg t "$2" '.summary | startswith("done: ") and contains($t) and (contains("\"error\"") | not)' <<<"$a" >/dev/null ||
				{ echo "    | the activity: $(jq -c '{state, summary}' <<<"$a" 2>/dev/null || echo "$a")"; return 1; }
		}
		check "custos-net__network_addresses sees the host's interfaces (one up), not an empty namespace" net_has network_addresses UP
		# The host's routes name its interfaces (a private namespace has
		# none); an air-gapped host has no default route.
		check "custos-net__network_routes sees the host's routes (one via an interface)" net_has network_routes " dev "
		check "custos-net__listening_sockets sees the host's listening sockets (the fake model's 8008)" net_has listening_sockets :8008
		if enforcing; then
			exec_avc=$(ausearch -m AVC,USER_AVC,SELINUX_ERR -ts "$(date -d "@$exec_t0" '+%x %T')" 2>/dev/null | grep 'mcpsrv_exec_t' || true)
			check "no SELinux denial for the diagnostic commands (mcpsrv_exec_t)" test -z "$exec_avc"
			[ -n "$exec_avc" ] && sed 's/^/    | /' <<<"$exec_avc" | head -30
			# A command that failed without an audited denial: once more with
			# the policy's dontaudit rules off, to name what it was refused.
			if [ -n "$exec_failed" ] && semodule -DB >/dev/null 2>&1; then
				hidden_t0=$(date +%s)
				for tool in $exec_failed; do run_activity health-check "CALL exec__$tool" >/dev/null; done
				semodule -B >/dev/null 2>&1
				echo "    | every denial, the dontaudit ones included, while running again:$exec_failed"
				ausearch -m AVC,USER_AVC,SELINUX_ERR -ts "$(date -d "@$hidden_t0" '+%x %T')" 2>/dev/null |
					grep -o 'avc: .*' | sed 's/ pid=[0-9]*//' | sort | uniq -c | sort -rn | head -40 | sed 's/^/    | /'
			fi
			if [ -n "$exec_failed" ]; then
				# The same programs as the agent's user, outside the exec server.
				for p in ip timedatectl rpm; do
					f=$(PATH=/usr/sbin:/usr/bin command -v "$p") && ls -lZ "$f" "$(readlink -f "$f")" 2>&1 | sed 's/^/    | /'
				done
				for c in "ip -brief address" "timedatectl show"; do
					echo "    | as agent-health-check: $c"
					runuser -u agent-health-check -- env PATH=/usr/sbin:/usr/bin $c 2>&1 | head -5 | sed 's/^/    |   /'
				done
				journalctl --no-pager -o cat --since "@$exec_t0" -u systemd-timedated.service -u dbus.service -u dbus-broker.service 2>/dev/null |
					tail -15 | sed 's/^/    | /'
				systemctl show -p ProtectSystem,ProtectHome,PrivateNetwork,NoNewPrivileges,ExecPaths,NoExecPaths,RestrictAddressFamilies,SystemCallFilter,CapabilityBoundingSet,ReadOnlyPaths,InaccessiblePaths \
					"$(systemctl list-units --plain --no-legend 'mcp-exec-*' | awk 'NR == 1 {print $1}')" 2>&1 | sed 's/^/    | /'
			fi
		else
			skip "no SELinux denial for the diagnostic commands (SELinux is not enforcing)"
		fi
	else
		skip "the playbooks' exec commands are installed (no mcp-gateway-exec-server)"
	fi
	setup_again() {
		local out
		out=$(setup) && grep -q 'diagnostics-only: already' <<<"$out" && grep -q 'agent health-check: already' <<<"$out" &&
			grep -q 'model defaults: already' <<<"$out"
	}
	check "custos-setup again changes nothing but the key" setup_again
	agent-admin delete health-check >/dev/null 2>&1
	agent-admin defaults --clear >/dev/null 2>&1
	# The state and its seal (#109): the host as fresh as before.
	rm -f /etc/mcp-gateway/policy/agents/_enablement.yaml /etc/mcp-gateway/policy/agents/_seal.yaml /etc/credstore.encrypted/custos-enablement-seal \
		/etc/mcp-gateway/exec.d/exec.d-diagnostics.yaml /etc/mcp-gateway/exec.d/exec.d-disk-usage.yaml
else
	skip "custos-setup (custos-diagnostics is not installed)"
fi
