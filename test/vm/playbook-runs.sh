# shellcheck shell=bash
# The shipped diagnostic playbooks on any backend (#227): sourced by the
# functional tests (sections/85-playbooks.sh, the systemd backend) and by
# the OpenShell spike (test/openshell/run-spike.sh, both compute drivers).
# Uses the caller's check, eventually and skip.
#
# The playbooks call exec, systemd, custos-net and, optionally, snapper,
# tukit, zypp and restarts. The tests stand in for every one of them with
# testsrv --as SERVER (test/vm/testsrv/standin.go), so that every playbook
# runs the same everywhere: what is tested is the backend's path (the
# tools a run offers, the calls through the gateway, its audit, the
# report), not the servers' answers, which other sections test.

# The servers the stand-ins replace (testsrv's StandIns).
standin_servers=(exec systemd custos-net snapper tukit zypp restarts)

standin_marker="# Custos functional tests: a stand-in (test/vm/playbook-runs.sh)"

# standins_register SERVERS_DIR TESTSRV RUN_AS: a definition of each
# stand-in in SERVERS_DIR, named as the package's (<server>.yaml), which
# it overrides; a definition already there is kept aside.
standins_register() {
	local dir=$1 srv=$2 run_as=$3 s
	for s in "${standin_servers[@]}"; do
		[ -e "$dir/$s.yaml" ] && mv "$dir/$s.yaml" "$dir/.$s.yaml.before-standin"
		printf '%s\n' "$standin_marker" 'version: 1' "name: $s" \
			"command: [\"$srv\", \"--as\", \"$s\"]" "run_as: $run_as" >"$dir/$s.yaml"
	done
}

# standins_remove SERVERS_DIR: the stand-ins go (only files that are
# stand-ins: one a later step put there stays), what was there is back.
standins_remove() {
	local dir=$1 s
	for s in "${standin_servers[@]}"; do
		[ "$(head -n1 "$dir/$s.yaml" 2>/dev/null)" = "$standin_marker" ] || continue
		rm -f "$dir/$s.yaml"
		[ -e "$dir/.$s.yaml.before-standin" ] && mv "$dir/.$s.yaml.before-standin" "$dir/$s.yaml"
	done
	return 0
}

# standin_permissions: read-only permissions for every stand-in tool
# (a YAML list for agent-admin permissions).
standin_permissions() {
	"$1" --as-tools | jq -r 'to_entries[] | .key as $s | .value[] | "- {server: \($s), tool: \(.), read_only: true}"'
}

# playbook_runs AGENT AUDIT_UNIT LABEL: every shipped playbook as an
# activity of AGENT, with PLAYBOOK REPORT for the fake model (a playbook
# whose steps name {unit} gets sshd.service). Each must finish, offer the
# model the playbook's tools and only those (the report's summary names
# what was offered), have the gateway audit its steps' calls as
# agent-AGENT (AUDIT_UNIT's journal), and end with its diagnosis report.
playbook_runs() {
	local agent=$1 unit=$2 label=$3 all pb req run t1
	all=$(agent-admin playbooks) || { check "$label: agent-admin playbooks" false; return; }
	for pb in $(jq -r 'to_entries[] | select(.value.file | startswith("/usr/share/custos/playbooks/")) | .key' <<<"$all" | sort); do
		t1=$(date +%s)
		req=$(jq -c --arg p "$pb" '.[$p] as $b | {playbook: $p, prompt: "PLAYBOOK REPORT", wait: true} +
			(if ($b.steps | tostring | contains("{unit")) then {unit: "sshd.service"} else {} end)' <<<"$all")
		run=$(curl -sS --max-time 300 --unix-socket "/run/agents/$agent/api.sock" http://agent/v1/activities -X POST -d "$req")
		check "$label: $pb: the run finishes" jq -e '.state == "finished"' <<<"$run" >/dev/null ||
			{ echo "    | $(jq -c '{state, reason, error}' <<<"$run" 2>/dev/null || head -c 300 <<<"$run")"; continue; }
		check "$label: $pb: its diagnosis report arrives" jq -e --arg p "$pb" '.report.kind == "diagnosis" and .report.playbook == $p' <<<"$run" >/dev/null
		# Offered: the steps' tools and the playbook's tools, and nothing else.
		check "$label: $pb: the model is offered the playbook's tools, and only those" jq -e --argjson b "$(jq -c --arg p "$pb" '.[$p]' <<<"$all")" '
			(.report.summary // "" | ltrimstr("offered: ") | split(", ") | map(select(. != ""))) as $o |
			(($b.steps | map(.tool)) + ($b.tools // []) | unique) as $t |
			($b.steps | map(select(.optional | not) | .tool) | unique) as $r |
			(($o - $t) | length == 0) and (($r - $o) | length == 0)' <<<"$run" >/dev/null ||
			echo "    | $(jq -r '.report.summary // "no report"' <<<"$run")"
		check "$label: $pb: the host made the steps' calls" jq -e '[.calls[] | select(.playbook_step != null)] | length > 0' <<<"$run" >/dev/null
		check "$label: $pb: the gateway audited them as agent-$agent" eventually 20 bash -c \
			"journalctl -u '$unit' -o cat --no-pager --since '@$t1' | grep '\"audit\":true' |
				jq -e --argjson tools '$(jq -c '[.calls[] | select(.playbook_step != null) | .tool]' <<<"$run")' -s '
					[.[] | select(.event == \"mcp-result\" and .sub == \"agent-$agent\" and .outcome == \"ok\") | .server + \"__\" + .name] as \$seen |
					(\$tools - \$seen | length == 0)' >/dev/null"
	done
}
