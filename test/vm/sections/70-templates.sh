# shellcheck shell=bash
# Templates, and the model's credentials.
# Needs: the fake model.
#
# Sourced by run-tests.sh, in the order of the files' names; uses lib.sh.

section Templates
tdir=/usr/share/custos/templates
check "the templates are installed" test -f "$tdir/triage/template.yaml" -a -f "$tdir/exec.d-disk-usage.yaml"
check "agent-admin templates, as nobody, lists them all, none broken" bash -c \
	"$(declare -f as_nobody); as_nobody /usr/sbin/agent-admin templates | jq -e '(.templates | length) >= 7 and ([.templates[] | select(.error)] | length) == 0' >/dev/null"
if findmnt -no OPTIONS / | tr , '\n' | grep -qx ro; then want=true; else want=false; fi
check "agent-admin templates says whether the system is transactional ($want)" bash -c \
	"agent-admin templates | jq -e '.transactional == $want' >/dev/null"
agent-admin delete tt1 >/dev/null 2>&1
agent-admin delete tt2 >/dev/null 2>&1
# Model defaults (§14b): the fake model, for new agents.
cp -a /etc/custos/defaults.yaml "$work/defaults.yaml.saved" 2>/dev/null
check "agent-admin defaults refuses a model without a name" not bash -c "printf 'model: {provider: openai}\n' | agent-admin defaults --from -"
check "agent-admin defaults --from - sets the model for new agents" bash -c \
	"printf 'model: {provider: openai, name: fake-default, endpoint: \"http://127.0.0.1:8008/v1\"}\n' | agent-admin defaults --from -"
check "the defaults are root 0644" stat_is /etc/custos/defaults.yaml root:root:644
check "agent-admin defaults, as nobody, shows them" bash -c \
	"$(declare -f as_nobody); as_nobody /usr/sbin/agent-admin defaults | jq -e '.model.name == \"fake-default\"' >/dev/null"
check "agent-admin draft, as nobody, has the template with the defaults' model" bash -c \
	"$(declare -f as_nobody); as_nobody /usr/sbin/agent-admin draft x1 --template patch-pilot | grep -q 'name: fake-default' && agent-admin draft x1 --template patch-pilot | agent-admin check -"
check "agent-admin create tt1 --template triage" agent-admin create tt1 --template triage
check "tt1's configuration is the template's, named tt1" bash -c \
	"grep -qx 'name: tt1' /etc/custos/agents/tt1/agent.yaml && grep -q '^# Agent template triage' /etc/custos/agents/tt1/agent.yaml"
check "tt1's configuration has the defaults' model" grep -q 'name: fake-default' /etc/custos/agents/tt1/agent.yaml
check "tt1 is advisory, as the template says" bash -c "agent-admin show tt1 | jq -e '.agents.tt1.mode == \"advisory\"' >/dev/null"
check "tt1 has the template's permissions" bash -c \
	"test \"\$(agent-admin show tt1 | jq '.agents.tt1.permissions | length')\" = \"\$(grep -c '^- ' $tdir/triage/permissions.yaml)\""
# #130: the product documentation through the gateway's suse-docs server
# (mcp-docs): the template shows tt1 its reading tools, and the gateway's
# policy allows them, advisory as tt1 is; a docs tool it does not list it
# does not get.
check "tt1 is shown the suse-docs tools" bash -c \
	"grep -q '^  servers: \[.*suse-docs\]' /etc/custos/agents/tt1/agent.yaml && grep -qx '    - suse-docs__find_sections' /etc/custos/agents/tt1/agent.yaml"
check "the gateway's policy allows tt1 suse-docs find_sections" eventually 20 bash -c \
	"$(declare -f opa_decision opa_effect); [ \"\$(opa_effect tt1 find_sections suse-docs)\" = allow ]"
check "and read_section" bash -c "$(declare -f opa_decision opa_effect); [ \"\$(opa_effect tt1 read_section suse-docs)\" = allow ]"
check "but not a docs tool it does not list (read_lines)" bash -c "$(declare -f opa_decision opa_effect); [ \"\$(opa_effect tt1 read_lines suse-docs)\" != allow ]"
# As the Cockpit page does: the edited configuration on stdin.
check "agent-admin create tt2 --template service-doctor --from - (stdin)" bash -c \
	"sed -e 's/^name: .*/name: tt2/' -e 's/qwen3-32b/edited-model/' $tdir/service-doctor/agent.yaml | agent-admin create tt2 --template service-doctor --from -"
check "tt2 has the edited configuration" grep -q edited-model /etc/custos/agents/tt2/agent.yaml
check "tt2 is supervised and its categories are not promoted" bash -c \
	"agent-admin show tt2 | jq -e '.agents.tt2.mode == \"supervised\" and ((.agents.tt2.categories // {}) | length) == 0 and ([.agents.tt2.permissions[] | select(.category == \"config-lifecycle\")] | length) == 1' >/dev/null"
# compare and apply-template (§14a): tt1 records its template and is
# the same as it; edited and with a permission less, it differs; applying
# the template's takes both back and keeps its model and mode.
check "tt1 records its template" grep -qx triage /etc/custos/agents/tt1/template
check "agent-admin compare tt1: the same as triage" bash -c \
	"agent-admin compare tt1 | jq -e '.[0] | .recorded and .configuration.same and .permissions.same' >/dev/null"
check "an edited tt1 with a permission less differs" bash -c \
	"sed 's/^  host. You can only look/  host, nginx only. You can only look/' /etc/custos/agents/tt1/agent.yaml | agent-admin edit tt1 --from - &&
	 agent-admin show tt1 | jq '.agents.tt1.permissions[1:]' | agent-admin permissions tt1 &&
	 agent-admin compare tt1 | jq -e '.[0] | (.configuration.diff | test(\"nginx only\")) and (.permissions.added | length) == 1' >/dev/null"
check "agent-admin apply-template tt1" agent-admin apply-template tt1
check "tt1 is the template's again, with its model and mode" bash -c \
	"agent-admin compare tt1 | jq -e '.[0] | .configuration.same and .permissions.same' >/dev/null &&
	 grep -q 'name: fake-default' /etc/custos/agents/tt1/agent.yaml && agent-admin show tt1 | jq -e '.agents.tt1.mode == \"advisory\"' >/dev/null"
check "agent-admin refuses an unknown template" not agent-admin create tt3 --template no-such
check "agent-admin defaults --clear" agent-admin defaults --clear
check "tt1 keeps its model when the defaults go" grep -q 'name: fake-default' /etc/custos/agents/tt1/agent.yaml
[ -e "$work/defaults.yaml.saved" ] && cp -a "$work/defaults.yaml.saved" /etc/custos/defaults.yaml
check "agent-admin delete tt1 tt2" bash -c "agent-admin delete tt1 && agent-admin delete tt2"

section "Model credentials"
# A model credential, as agent-admin set-credential stores it: sealed to
# the TPM as well where the host has one, which systemd needs while it
# sets up the agent's credentials (DeviceAllow= in agent@.service).
if systemd-analyze has-tpm2 -q 2>/dev/null; then tpm="sealed to the TPM"; else tpm="no TPM: the host key only"; fi
agent-admin delete t3 >/dev/null 2>&1
f=$(config t3 custos-test)
sed 's|^model: .*|model: {provider: openai, name: fake, endpoint: "http://127.0.0.1:8008/v1", credential: sk-litellm-local}|' "$f" > "$f.key"
check "agent-admin refuses a key given as the credential name, without repeating it" bash -c \
	"out=\$(agent-admin create t3 --from '$f.key' 2>&1); [ \$? -ne 0 ] && grep -q 'looks like a key' <<<\"\$out\" && ! grep -q litellm <<<\"\$out\" && ! test -e /etc/custos/agents/t3"
rm -f "$f.key"
sed -i 's|^model: .*|model: {provider: openai, name: fake, endpoint: "http://127.0.0.1:8008/v1", credential: model-key}|' "$f"
check "agent-admin create t3 says its credential is not stored yet" bash -c \
	"agent-admin create t3 --from '$f' | grep -q 'agent-admin set-credential t3 model-key'"
check "without the credential, t3 says how to store it" eventually 30 bash -c \
	"systemctl start agent@t3.service >/dev/null 2>&1; journalctl -u agent@t3.service --since '@$t0' -o cat --no-pager | grep -q 'model.credential model-key is not stored for agent t3: agent-admin set-credential t3 model-key'"
check "agent-admin credentials t3: model-key is missing" bash -c \
	"agent-admin credentials t3 | jq -e '.t3.missing == [\"model-key\"] and (.t3.credentials[0] | .name == \"model-key\" and .stored == false and .used_by == [\"model\"])' >/dev/null"
check "agent-admin set-credential t3 model-key ($tpm)" bash -c "printf 'test-key' | agent-admin set-credential t3 model-key"
check "agent-admin credentials t3: model-key is stored, its value not listed" bash -c \
	"out=\$(agent-admin credentials t3) && jq -e '.t3.missing == [] and .t3.credentials[0].stored and (.t3.credentials[0].stored_at | length > 0)' <<<\"\$out\" >/dev/null && ! grep -q test-key <<<\"\$out\""
check "agent-admin remove-credential refuses the model's credential without --force" not agent-admin remove-credential t3 model-key
check "agent-admin remove-credential removes an unused one" bash -c \
	"printf spare | agent-admin set-credential t3 spare && agent-admin remove-credential t3 spare && ! test -e /etc/credstore.encrypted/agent-t3.spare"
echo "  credential: $(stat -c '%U:%G %a %C' /etc/credstore.encrypted/agent-t3.model-key 2>&1)"
check "the credential is stored encrypted, root's and not readable by others" bash -c \
	"[ \"\$(stat -c %U:%G /etc/credstore.encrypted/agent-t3.model-key)\" = root:root ] && [ \$(( 0\$(stat -c %a /etc/credstore.encrypted/agent-t3.model-key) & 077 )) -eq 0 ]"
t3_t0=$(date +%s)
systemctl reset-failed agent@t3.service >/dev/null 2>&1
check "with it, t3 starts ($tpm)" bash -c "systemctl restart agent@t3.service && sleep 2 && systemctl is-active -q agent@t3.service"
check "systemd set up t3's credentials (no CREDENTIALS failure)" not bash -c \
	"journalctl -u agent@t3.service --since '@$t0' -o cat --no-pager | grep -q 'Failed to set up credentials'"
check "t3 answers on its socket" bash -c "$(declare -f api); api t3 /v1/status | jq -e '.contract == 1' >/dev/null"
check "no ConfigurationDirectory mode warning" not bash -c \
	"journalctl -u agent@t3.service --since '@$t0' -o cat --no-pager | grep -q 'ConfigurationDirectory.*mode is different'"
# SELinux on the credential (the SELinux section above ran before this
# one). The base policy does not audit every denial here: that t3 starts
# and answers is the real check.
t3_avc=$(ausearch -m AVC,USER_AVC,SELINUX_ERR -ts "$(date -d "@$t3_t0" '+%x %T')" 2>/dev/null | grep -E 'denied|SELINUX_ERR' || true)
[ -n "$t3_avc" ] && printf '  %s\n' "$t3_avc" | head -20
check "no SELinux denial while t3 reads its credential" test -z "$t3_avc"
check "agent-admin delete t3" agent-admin delete t3
