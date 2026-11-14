# shellcheck shell=bash
# Diagnostics-only hosts and category enablement (CONTRACT.md §6b): the
# release gate.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section "Diagnostics-only (release gate)"
# CONTRACT.md §6b: on a diagnostics-only host only the diagnostics
# category is usable, enforced by the gateway; enable is the action out of
# it, disable the way back (#110), and enabling every category gives the
# broader install's result.
# The gateway names a module's reasons (deny_reasons, unmatched_reasons:
# its requirement R1) when its shipped policy has them.
gateway_reasons() { grep -rqs unmatched_reason /usr/share/mcp-gateway/policy/; }
effect_is() { [ "$(opa_effect "$1" "$2")" = "$3" ]; }
# change_asks <agent>: a CHANGE waits for an approval, which is declined.
change_asks() {
	local id aid
	id=$(start_activity "$1" CHANGE)
	eventually 30 activity_state_is "$1" "$id" waiting_approval || return 1
	aid=$(approval_of "$1" "$id")
	gw "/v1/approvals/$aid" -X POST -d '{"decision":"deny","scope":"once"}' >/dev/null
	eventually 30 activity_state_is "$1" "$id" finished
}
dg_perms() { printf '%s\n' '- {server: custos-test, tool: probe, read_only: true, category: diagnostics}' "$@" | agent-admin permissions dg; }
enablement_is() { agent-admin show | jq -e ".enablement | $1" >/dev/null; }
promotion_record() { journalctl -o json --no-pager --since "@$t0" SYSLOG_IDENTIFIER=custos-promotion "AGENT_EVENT=$1" | jq -e "select($2)" >/dev/null; }
enablement=/etc/mcp-gateway/policy/agents/_enablement.yaml
dgdata=/etc/mcp-gateway/policy/agents/dg.yaml
check "the catalog has diagnostics and compliance-audit" bash -c "agent-admin categories | jq -e '.diagnostics.title and .\"compliance-audit\".title' >/dev/null"
check "no enablement state before: every category enabled" not test -e "$enablement"
agent-admin delete dg >/dev/null 2>&1
check "agent-admin create dg" agent-admin create dg --from "$(config dg custos-test)"
check "diagnostics refuses a permission that is not read-only" not dg_perms '- {server: custos-test, tool: change, category: diagnostics}'
check "dg gets a diagnostics permission" dg_perms
check "agent-admin diagnostics-only" agent-admin diagnostics-only
check "show: diagnostics-only, recorded, not tampered" enablement_is \
	'.state == "diagnostics-only" and .enabled == ["diagnostics"] and (.record | startswith("n-")) and (has("tampered") | not)'
check "journal: categories.enabled by root" eventually 10 promotion_record categories.enabled '.AGENT_BY == "root" and (.AGENT_ENABLEMENT_TO | contains("diagnostics-only"))'
check "diagnostics: PROBE runs" eventually 30 runs_with dg PROBE 'probe ok'
# The seal of the agents' data (#158): a new seal covers it from the
# start. agent-admin signs dg's data and the agents' token issuers; a
# hand edit denies dg everything until agent-admin writes it again.
seal=/etc/mcp-gateway/policy/agents/_seal.yaml
issuers=/etc/mcp-gateway/policy/agents/_http.yaml
posture_is() { agent-admin show | jq -e "$1" >/dev/null; }
check "the seal covers the agents' data (agents: true)" grep -q '^  agents: true' "$seal"
check "dg's data is signed" grep -Eq '^ +signature: ' "$dgdata"
check "the agents' token issuers are signed" grep -Eq '^ +signature: ' "$issuers"
check "show: the seal covers agent data, nothing tampered" posture_is \
	'.agents_sealed == true and (.agents.dg | has("tampered") | not) and (has("issuers_tampered") | not)'
check "journal: gateway.data for dg, by root" eventually 10 promotion_record gateway.data '.AGENT_DATA == "dg" and (.AGENT_DATA_SHA256 | length == 64)'
cp "$dgdata" "$work/dg-signed.yaml"
sed -i -E 's/^( +)mode: supervised$/\1mode: advisory/' "$dgdata"
grep -Eq '^ +mode: advisory$' "$dgdata" || echo "the hand edit of $dgdata did not apply" >&2
check "show: dg's data changed by hand does not match its seal" posture_is '.agents.dg.tampered | contains("does not match its seal")'
check "the gateway denies dg everything, its diagnostics call too" eventually 30 effect_is dg probe deny
if gateway_reasons; then
	check "the denial's reason: the agent's data does not match its seal" reason_is dg probe \
		"the agent's data does not match its seal (changed outside agent-admin): every call is denied until an administrator checks it and accepts it (agent-admin seal --agents --accept dg)"
else
	skip "the denial's reason (this mcp-gateway's policy has no deny_reasons)"
fi
# agent-admin does not build on data that does not match the seal
# (security review 0.6, N1): a change is refused, a pause is written
# unsigned; putting back what agent-admin wrote restores dg.
check "agent-admin refuses to change dg's data that does not match the seal" not dg_perms
check "agent-admin pause dg still works, written unsigned" bash -c "agent-admin pause dg && ! grep -Eq '^ +signature: ' '$dgdata'"
cp "$work/dg-signed.yaml" "$dgdata"
check "show: put back as agent-admin wrote it, dg's data matches its seal" posture_is '.agents.dg | has("tampered") | not'
check "agent-admin resume dg (signed again)" agent-admin resume dg
check "the gateway allows dg's diagnostics call again" eventually 30 effect_is dg probe allow
check "agent-admin mode dg advisory" agent-admin mode dg advisory
cp "$work/dg-signed.yaml" "$dgdata"
check "show: an earlier signed version of dg's data put back is reported" posture_is '.agents.dg.tampered | contains("an earlier version was put back")'
check "agent-admin mode dg supervised" agent-admin mode dg supervised
check "show: dg's data is the latest again" posture_is '.agents.dg | has("tampered") | not'
check "agent-admin issuers --add" agent-admin issuers --add https://agents.example.test
check "the issuers are signed, with the issuer" bash -c "grep -q 'agents.example.test' '$issuers' && grep -Eq '^ +signature: ' '$issuers'"
cp "$issuers" "$work/issuers.yaml"
sed -i 's|agents.example.test|evil.example.test|' "$issuers"
check "show: issuers changed by hand do not match their seal" posture_is '.issuers_tampered | contains("trusts no issuer")'
cp "$work/issuers.yaml" "$issuers"
check "agent-admin issuers --remove" agent-admin issuers --remove https://agents.example.test
check "show: nothing tampered" posture_is '(has("issuers_tampered") | not) and (.agents.dg | has("tampered") | not)'
# The emergency stop is never sealed: written by hand, it stops dg.
printf '_control:\n  stopped: true\n' >/etc/mcp-gateway/policy/agents/_control.yaml
check "an emergency stop written by hand stops a sealed host's agent" eventually 30 effect_is dg probe deny
check "agent-admin release-all" agent-admin release-all
check "released, dg's diagnostics call is allowed" eventually 30 effect_is dg probe allow
# The checks below write dg's data past agent-admin, to test the
# categories' gate on its own: the seal's agent part off for them (show
# reports it), on again with agent-admin seal --agents.
sed -i '/^  agents: true$/d' "$seal"
check "show: the seal's agent part removed is reported" posture_is '.agents_sealed == false and (.seal_tampered | contains("no longer covers the agents'"'"' data"))'
check "agent-admin refuses a permission of a disabled category" not dg_perms '- {server: custos-test, tool: change, category: patch-errata}'
check "agent-admin refuses a permission without a category" not dg_perms '- {server: custos-test, tool: change}'
check "journal: category.refused, with the person" eventually 10 promotion_record category.refused '.AGENT_NAME == "dg" and .AGENT_CATEGORY == "patch-errata" and .AGENT_BY == "root"'
check "agent-admin refuses a template of a disabled category" bash -c "agent-admin create pp --template patch-pilot 2>&1 | grep -q 'patch-errata.* not enabled'"
check "no agent pp was created" not test -e /etc/custos/agents/pp
# The bundled diagnostics agent's template is what a diagnostics-only host
# can create (§20b): read-only, in diagnostics, with its schedules.
agent-admin delete hc >/dev/null 2>&1
check "agent-admin create hc --template health-check on a diagnostics-only host" agent-admin create hc --template health-check
check "hc: supervised, every permission read-only in diagnostics" bash -c \
	"agent-admin show hc | jq -e '.agents.hc | .mode == \"supervised\" and (.permissions | length > 0) and all(.permissions[]; .read_only == true and .category == \"diagnostics\")' >/dev/null"
check "hc's schedules run playbooks: their timers are enabled" bash -c \
	"for s in failed-units disk-pressure journal-errors update-status; do systemctl is-enabled -q agent-schedule@hc_\$s.timer || exit 1; done"
check "agent-admin delete hc" agent-admin delete hc
# The gate: the gateway refuses the category even with its permission in
# the agent's data, written past agent-admin as an administrator might.
cp "$dgdata" "$work/dg.yaml"
printf '%s\n' 'dg:' '  mode: supervised' '  permissions:' '    - {server: custos-test, tool: probe, read_only: true, category: diagnostics}' \
	'    - {server: custos-test, tool: change, category: patch-errata}' >"$dgdata"
check "the gateway's OPA denies the disabled category's call" eventually 30 effect_is dg change deny
check "the gateway's OPA allows the diagnostics call" effect_is dg probe allow
if gateway_reasons; then
	# Waited for: before the edit, no permission named change, so the
	# denial above may come from the data OPA had before it reloaded.
	check "the denial's reason: the category is not enabled" eventually 30 reason_is dg change \
		"task category patch-errata is not enabled on this host (diagnostics-only): agent-admin enable patch-errata"
	check "a tool no permission names keeps \"no matching permission\"" reason_is dg wipe "no matching permission"
	sed -i 's/^  mode: supervised$/  mode: supervised\n  paused: true/' "$dgdata"
	check "a paused agent's denial says so" eventually 30 reason_is dg probe "the agent is paused (agent-admin resume)"
	sed -i '/^  paused: true$/d' "$dgdata"
	eventually 30 effect_is dg probe allow
else
	skip "the denial's reasons (this mcp-gateway's policy has no deny_reasons)"
fi
check "the gateway hides the disabled category's tool from the agent" eventually 30 runs_with dg CHANGE 'not offered: custos-test__change'
# Tampering shows, and the seal (#109) makes it ineffective: agent-admin
# signed the state with a key kept as a systemd credential; the gateway
# checks the signature with _seal.yaml's public key, and a state edited by
# hand counts as diagnostics-only.
check "the seal: its public key is gateway data" grep -q 'BEGIN PUBLIC KEY' "$seal"
check "the seal's private key is an encrypted credential, root's alone" stat_is /etc/credstore.encrypted/custos-enablement-seal root:root:600
check "the state is signed" grep -q '^  signature: ' "$enablement"
check "show: sealed" enablement_is '.sealed == true'
cp "$enablement" "$work/enablement.yaml"
sed -i -e 's/state: diagnostics-only/state: categories/' -e 's/^    - diagnostics$/    - diagnostics\n    - patch-errata/' "$enablement"
check "show reports an enablement state changed by hand" enablement_is '.tampered | length > 0'
check "show reports what the gateway enforces: diagnostics-only" enablement_is \
	'.state == "diagnostics-only" and (.tampered | contains("does not match its seal"))'
if gateway_reasons; then
	check "the gateway ignores the edited state: patch-errata is still denied, for the broken seal" eventually 30 reason_is dg change \
		"the task categories' state does not match its seal (changed outside agent-admin): only diagnostics is usable until agent-admin enable or disable writes it again"
else
	sleep 5
	check "the gateway ignores the edited state: patch-errata is still denied" effect_is dg change deny
fi
check "the diagnostics call is still allowed" effect_is dg probe allow
mv "$enablement" "$work/enablement-edited.yaml"
check "show: a removed state on a sealed host is diagnostics-only" enablement_is \
	'.state == "diagnostics-only" and (.tampered | contains("was removed"))'
check "the gateway denies patch-errata without a state, too" eventually 30 effect_is dg change deny
cp "$work/enablement.yaml" "$enablement"
check "show: the restored state matches its record and its seal" enablement_is '(has("tampered") | not) and .sealed == true'
# Enabling: one action, recorded; the category starts supervised.
check "agent-admin enable patch-errata" agent-admin enable patch-errata
check "journal: categories.enabled from diagnostics-only" eventually 10 promotion_record categories.enabled '.AGENT_ENABLEMENT_FROM | contains("diagnostics-only")'
check "the gateway's OPA asks for the enabled category's call" eventually 30 effect_is dg change ask
check "readiness on a host that started diagnostics-only asks for the diagnostics history" bash -c \
	"agent-admin readiness dg patch-errata | jq -e '.diagnostics_apply and (.categories[\"patch-errata\"].unmet | any(startswith(\"diagnosis reports with feedback\")))' >/dev/null"
check "enabled, the category starts supervised: its change waits for an approval" change_asks dg
# What the seal does not cover (security review M5), show reports: an
# earlier signed state put back (it verifies; the journal has a later
# record), and the seal removed from a host agent-admin sealed.
cp "$enablement" "$work/enablement-current.yaml"
cp "$work/enablement.yaml" "$enablement"
check "show: an earlier signed state put back is reported" enablement_is '(.tampered | contains("an earlier state was put back"))'
cp "$work/enablement-current.yaml" "$enablement"
mv "$seal" "$work/seal.yaml"
check "show: a removed seal is reported" enablement_is '(.tampered | contains("was removed, though agent-admin sealed this host"))'
mv "$work/seal.yaml" "$seal"
check "show: with both back, nothing is reported" enablement_is '(has("tampered") | not) and .sealed == true'
# The way back (#110): disable is refused while dg has a permission in the
# category; without it, the category is disabled again, recorded, and the
# gateway denies it once more; disable --all returns to diagnostics-only.
cp "$dgdata" "$work/dg-patch.yaml"
check "agent-admin disable refuses a category an agent has a permission in" bash -c \
	"agent-admin disable patch-errata 2>&1 | grep -q 'agent dg has permissions in patch-errata'"
check "journal: category.refused for disable" eventually 10 promotion_record category.refused '.AGENT_CATEGORY == "patch-errata" and .AGENT_REASON == "disable"'
check "dg keeps only its diagnostics permission" dg_perms
check "agent-admin disable patch-errata" agent-admin disable patch-errata
check "show: patch-errata disabled, recorded, not tampered" enablement_is \
	'.state == "categories" and (.enabled | index("patch-errata") | not) and (has("tampered") | not)'
check "journal: categories.disabled by root" eventually 10 promotion_record categories.disabled \
	'.AGENT_BY == "root" and (.AGENT_ENABLEMENT_TO | contains("patch-errata") | not)'
cp "$work/dg-patch.yaml" "$dgdata"
check "disabled again, the gateway denies the category even with its permission" eventually 30 effect_is dg change deny
check "dg keeps only its diagnostics permission, again" dg_perms
check "agent-admin disable --all: back to diagnostics-only" agent-admin disable --all
check "show: diagnostics-only, recorded, not tampered" enablement_is \
	'.state == "diagnostics-only" and .enabled == ["diagnostics"] and (has("tampered") | not)'
check "agent-admin disable on a diagnostics-only host has nothing to do" not agent-admin disable compliance-audit
# Forward again, as below; dg's permission in patch-errata back for it.
cp "$work/dg-patch.yaml" "$dgdata"
# A host whose seal does not cover the agents' data yet (sealed before
# #158, or as here): agent-admin seal --agents signs it as it is on file.
check "agent-admin seal --agents refuses dg's data written past the seal" not agent-admin seal --agents
check "agent-admin seal --agents --accept dg" agent-admin seal --agents --accept dg
check "show: the seal covers agent data again, dg signed, nothing tampered" posture_is \
	'.agents_sealed == true and (has("seal_tampered") | not) and (.agents.dg | has("tampered") | not)'
check "dg's data is signed again" grep -Eq '^ +signature: ' "$dgdata"
# Every category enabled decides as a host without enablement state (the
# broader install).
check "agent-admin enable --all" agent-admin enable --all
all="$(opa_effect dg probe) $(opa_effect dg change)"
# The broader install has neither a state nor a seal (removing only the
# state would leave the host diagnostics-only); the next section needs a
# fresh host, too.
rm -f "$enablement" "$seal" /etc/credstore.encrypted/custos-enablement-seal
broader() { [ "$(opa_effect dg probe) $(opa_effect dg change)" = "$all" ] && [ "$all" = "allow ask" ]; }
sleep 2
check "every category enabled decides as the broader install: $all" eventually 30 broader
cp "$work/dg.yaml" "$dgdata"
check "agent-admin delete dg" agent-admin delete dg
