/* The Cockpit page against a real Cockpit, in the test VM (test/vm/run-vm.sh
 * with SUITE=cockpit, after test/cockpit/vm-setup.sh): logged in as a
 * person (carol), everything through Cockpit's own bridges, checked in the
 * VM over ssh where it matters (the gateway's audit, the agent data, the
 * emergency stop, SELinux). With SHOTS=DIR it saves the screenshots of the
 * real page that the documentation uses.
 *
 * Environment: COCKPIT_URL, COCKPIT_USER, COCKPIT_PASSWORD; SSH (the ssh
 * command to run a command in the VM, as a JSON array); WAITING_ACTIVITY,
 * WAITING_APPROVAL, DECLINED_APPROVAL, MOCK_ACTIVITY, T0 (from vm-setup.sh); CHROME
 * (optional); SHOTS (optional). Prints PASS/FAIL lines and exits 1 if any
 * failed.
 */
"use strict";
const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");
let chromium;
try {
    ({ chromium } = require("playwright-core"));
} catch {
    ({ chromium } = require("playwright"));
}

const env = k => {
    if (!process.env[k]) throw new Error("missing " + k);
    return process.env[k];
};
const url = env("COCKPIT_URL");
const user = env("COCKPIT_USER");
const password = env("COCKPIT_PASSWORD");
const sshCmd = JSON.parse(env("SSH"));
const waiting = env("WAITING_ACTIVITY");
const approval = env("WAITING_APPROVAL");
const declined = env("DECLINED_APPROVAL");
const mockRun = env("MOCK_ACTIVITY");
const t0 = env("T0");
const shots = process.env.SHOTS;
if (shots) fs.mkdirSync(shots, { recursive: true });

let failed = 0, total = 0;
function check(cond, what, detail) {
    total++;
    if (cond) {
        console.log("PASS: " + what);
    } else {
        failed++;
        console.log("FAIL: " + what + (detail ? "\n    | " + String(detail).split("\n").join("\n    | ") : ""));
    }
}

/* vm runs a shell command in the VM as root; returns its output, or null. */
function vm(cmd) {
    try {
        return execFileSync(sshCmd[0], [...sshCmd.slice(1), cmd], { encoding: "utf8", timeout: 60000 });
    } catch {
        return null;
    }
}
/* vmEventually waits until cmd succeeds in the VM. */
async function vmEventually(seconds, cmd) {
    for (let i = 0; i < seconds; i++) {
        if (vm(cmd) !== null) return true;
        await new Promise(r => setTimeout(r, 1000));
    }
    return false;
}
const gwAudit = jq => `journalctl -u mcp-gateway.service -o cat --no-pager --since @${t0} | grep '"audit":true' | jq -e 'select(${jq})' >/dev/null`;

(async () => {
    const browser = await chromium.launch(Object.assign({ args: ["--ignore-certificate-errors"] },
        process.env.CHROME ? { executablePath: process.env.CHROME } : {}));
    // Screenshots a person will look at: 2x.
    const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 });
    const page = await context.newPage();
    // Errors in the page's scripts (its frame's /custos/ files) fail the
    // test; Cockpit's own (the shell's, minified) are listed with where
    // they come from.
    const errors = [], shellErrors = [];
    page.on("pageerror", e => {
        const where = String(e.stack || "").split("\n").slice(0, 4).join(" | ");
        (/\/custos\/[a-z]+\.js/.test(e.stack || "") ? errors : shellErrors).push(e.message + " @ " + where);
    });
    // Cockpit's login sends the remembered choice of administrative access
    // (localStorage "superuser:<user>"): "any" starts the sudo bridge with
    // the login password, as when the person had turned it on before.
    await context.addInitScript(u => { try { localStorage.setItem("superuser:" + u, "any") } catch { /* none */ } }, user);
    const frame = () => page.frameLocator('iframe[name="cockpit1:localhost/custos"]');
    const shot = async name => { if (shots) await page.screenshot({ path: path.join(shots, name + ".png") }) };
    const step = async (name, fn) => {
        try {
            await fn();
        } catch (e) {
            // The locator it waited for, and what the page said then.
            const said = await frame().locator("#error, #defaults-note, #create-note, #agent-created, .config-edit .actions span")
                .evaluateAll(ns => ns.filter(n => n.textContent.trim()).map(n => "#" + n.id + ": " + n.textContent.trim()).join("\n"))
                .catch(() => "");
            check(false, name, e.message.split("\n").filter(l => l.trim()).slice(0, 3).join("\n") + (said ? "\npage: " + said : ""));
            if (shots) await page.screenshot({ path: path.join(shots, "failed-" + name.replace(/\W+/g, "-") + ".png") }).catch(() => {});
        }
    };
    const go = async hash => {
        await page.goto(url + "/custos#" + hash);
        await frame().locator("#tabs").waitFor({ timeout: 60000 });
    };

    await step("log in to Cockpit as " + user, async () => {
        await page.goto(url + "/");
        await page.fill("#login-user-input", user);
        await page.fill("#login-password-input", password);
        await page.click("#login-button");
        await page.waitForSelector("iframe[name^='cockpit1:']", { timeout: 90000 });
        check(true, "log in to Cockpit as " + user);
    });

    await step("the Activity tab", async () => {
        await go("/activity/patch-pilot/" + waiting);
        const row = frame().locator("#activity-patch-pilot-" + waiting);
        await row.waitFor({ timeout: 60000 });
        check(true, "the page finds the agents and patch-pilot's waiting activity, as " + user);
        await frame().locator("#activity-detail h2").waitFor();
        const detail = await frame().locator("#activity-detail").textContent();
        check(detail.includes("schedule · morning-check") && detail.includes("triage"),
              "its chain goes back to triage's schedule", detail.slice(0, 300));
        check((await frame().locator("#whoami").textContent()).includes(user), "signed in as " + user);
        check(await row.evaluate(e => e.classList.contains("child")), "the delegated activity is under triage's");
        await shot("activity");
    });

    await step("the Approvals tab", async () => {
        await go("/approvals/" + approval);
        const item = frame().locator("#approval-" + approval);
        await item.waitFor({ timeout: 60000 });
        const t = await item.textContent();
        check(t.includes("custos-test__change") && t.includes("agent-patch-pilot") && t.includes("self-asserted") &&
              t.includes("Writing the test marker"), "the approval item joins the gateway's call and the agent's account", t.slice(0, 300));
        await frame().locator("#decided .decided").first().waitFor({ timeout: 30000 });
        check((await frame().locator("#decided").textContent()).includes(declined.slice(0, 10)), "today's decline is listed (the journal, as " + user + ")");
        await shot("approvals");
        await item.locator("button:has-text('Approve once')").click();
        await frame().locator("#approvals").getByText("not pending").waitFor({ timeout: 30000 });
        check(await vmEventually(30, gwAudit(`.event == "mcp-approval" and .id == "${approval}" and .ok == true and .by == "${user}" and .via == "cockpit"`)),
              "the gateway's audit names " + user + " as the approver, via cockpit, not root");
        check(await vmEventually(60, `[ "$(curl -s --unix-socket /run/agents/patch-pilot/api.sock http://a/v1/activities/${waiting} | jq -r .state)" = finished ]`),
              "the approved change ran and the activity finished");
    });

    await step("the Audit tab", async () => {
        await go("/audit");
        const a = frame().locator("#action-" + approval);
        await a.waitFor({ timeout: 60000 });
        const t = await a.textContent();
        check(t.includes("approved") && t.includes("by " + user) && t.includes("executed"), "the history: proposed, approved by " + user + ", executed", t);
        check((await frame().locator("#action-" + declined).textContent()).includes("declined"), "and the declined one");
        await shot("audit");
    });

    await step("the Agents tab", async () => {
        await go("/agents/patch-pilot");
        await frame().locator("#boundaries").waitFor({ timeout: 60000 });
        const b = await frame().locator("#boundaries").textContent();
        check(b.includes("custos-test__probe") && b.includes("needs approval"), "patch-pilot's tool boundaries, from agent-admin show", b);
        await shot("agents");
        // Its task category's readiness, from the real journal and the
        // gateway's decision on the change declined above.
        await frame().locator("#category-patch-errata").waitFor({ timeout: 30000 });
        const cat = frame().locator("#category-patch-errata");
        await cat.getByText(/^(not ready|ready)$/).waitFor({ timeout: 60000 });
        const cells = await cat.locator("td").allTextContents();
        check(cells[1] === "supervised" && cells[3].startsWith("not ready") && /^[1-9][0-9]* in /.test(cells[6]),
              "the Agents tab shows patch-errata's readiness from the real records: not ready, with the declined proposal counted", cells.join(" | "));
        await frame().locator("#agent-detail button:has-text('Pause')").click();
        check(await vmEventually(30, `grep -q 'paused_by: ${user} ' /etc/mcp-gateway/policy/agents/patch-pilot.yaml`),
              "Pause, with administrative access, records " + user + " in the gateway data");
        await frame().locator("#agent-detail button:has-text('Resume')").click({ timeout: 30000 });
        check(await vmEventually(30, "! grep -q 'paused: true' /etc/mcp-gateway/policy/agents/patch-pilot.yaml"), "Resume");
        // Restart: a new invocation of the agent's service (started now if
        // it had exited on idle).
        const before = vm("systemctl show -p InvocationID --value agent@patch-pilot.service").trim();
        await frame().locator("#agent-detail button:text-is('Restart')").click({ timeout: 30000 });
        check(await vmEventually(60, `id=$(systemctl show -p InvocationID --value agent@patch-pilot.service); [ -n "$id" ] && [ "$id" != "${before}" ] && systemctl is-active -q agent@patch-pilot.service`),
              "Restart restarts agent@patch-pilot.service");
        // Stop, then Start: on a real system the socket's directory stays
        // (RemoveOnStop= removes only the socket), and the page must still
        // see the agent as stopped and offer Start.
        await frame().locator("#agent-detail button:text-is('Stop')").click({ timeout: 30000 });
        check(await vmEventually(30, "! systemctl is-active -q agent@patch-pilot.socket && ! systemctl is-active -q agent@patch-pilot.service"),
              "Stop stops agent@patch-pilot's socket and service");
        await frame().locator("#agent-detail button:text-is('Start')").waitFor({ timeout: 60000 });
        check((await frame().locator("#agent-detail").textContent()).includes("stopped"), "the page shows patch-pilot stopped, with Start");
        await frame().locator("#agent-detail button:text-is('Start')").click();
        check(await vmEventually(30, "systemctl is-active -q agent@patch-pilot.socket"), "Start starts agent@patch-pilot.socket again");
        await frame().locator("#agent-detail button:text-is('Stop')").waitFor({ timeout: 60000 });
        // Logs: Cockpit's own journal page, for the agent's unit.
        await frame().locator("#agent-detail button:text-is('Logs')").click();
        await page.waitForURL(/\/system\/logs#\/\?.*service=agent%40patch-pilot\.service/, { timeout: 30000 });
        const logs = page.frameLocator("iframe[name='cockpit1:localhost/system/logs']");
        await logs.getByText(/agent-patch-pilot|Agent patch-pilot/).first().waitFor({ timeout: 60000 });
        check(true, "Logs opens Cockpit's journal with agent@patch-pilot.service's entries");
    });

    await step("chat", async () => {
        await go("/agents/patch-pilot");
        const chat = frame().locator("#chat-patch-pilot");
        await chat.locator(".chat-input").fill("PROBE the host, please.");
        await chat.locator("button:has-text('Send')").click();
        await chat.locator(".msg-agent", { hasText: "probe ok" }).waitFor({ timeout: 60000 });
        check(true, "chat over AG-UI through Cockpit: the agent's answer arrives");
        check(await vmEventually(20, `journalctl -o json --no-pager --since @${t0} AGENT_NAME=patch-pilot AGENT_EVENT=invocation.start | jq -e 'select(.AGENT_ORIGIN == "chat" and .AGENT_USER == "${user}")' >/dev/null`),
              "the chat ran as an activity of " + user + " (origin chat)");
        if (shots) await chat.screenshot({ path: path.join(shots, "chat.png") });
    });

    await step("the Policy tab", async () => {
        await go("/policy");
        await frame().locator("#posture-patch-pilot").waitFor({ timeout: 60000 });
        await frame().locator("#whatif select").selectOption("patch-pilot");
        await frame().locator("#whatif-tool").fill("custos-test__change");
        await frame().locator("#whatif-result[data-for='patch-pilot custos-test__change ']").waitFor({ timeout: 30000 });
        const wi = await frame().locator("#whatif-result").textContent();
        check(wi.startsWith("needs approval") && wi.includes("the gateway's policy decided this"),
              "what would the gateway decide: the gateway's own policy, through the root bridge");
        await frame().locator("#whatif-tool").fill("custos-test__wipe");
        await frame().locator("#whatif-result[data-for='patch-pilot custos-test__wipe ']").waitFor({ timeout: 30000 });
        check((await frame().locator("#whatif-result").textContent()).includes("“no matching permission”"),
              "a tool no permission names: the gateway's reason");
        await shot("policy");
    });

    // The paths that broke in 0.1.1 against the real Cockpit, where the
    // stub-based smoke test had passed: a refused action's error, a refused
    // configuration, and an agent whose socket never answers.
    await step("a refused action keeps its error", async () => {
        await go("/agents/patch-pilot");
        await frame().locator("#promote-form").waitFor({ timeout: 60000 });
        await frame().locator("#promote-form > summary").click();
        await frame().locator("#category-select").selectOption("patch-errata");
        await frame().locator("#promote-form button:text-is('Promote')").click();
        const error = frame().locator("#error");
        await error.getByText(/Promoting patch-errata failed: .*not ready/).waitFor({ timeout: 60000 });
        check(true, "promoting a category that is not ready shows agent-admin's refusal");
        await page.waitForTimeout(12000); // two refreshes of the Agents tab
        check(await error.isVisible() && (await error.textContent()).includes("Promoting patch-errata failed"),
              "the error survives the page's refreshes");
        await error.locator("button:text-is('Dismiss')").click();
        check(!(await error.isVisible()), "Dismiss clears it");
        check(vm("agent-admin show patch-pilot | jq -e '(.agents[\"patch-pilot\"].categories // {})[\"patch-errata\"].mode != \"bounded\"' >/dev/null") !== null,
              "patch-errata stayed supervised");
    });

    // The envelope as a form (#163): what it builds is what agent-admin
    // promote stores, in the host's time zone.
    await step("promote with the envelope form", async () => {
        await go("/agents/patch-pilot");
        await frame().locator("#category-patch-errata button:text-is('Promote…')").click({ timeout: 60000 });
        const zone = (vm("timedatectl show -p Timezone --value") || "UTC").trim() || "UTC";
        await frame().locator("#envelope-windows .tz").waitFor({ timeout: 30000 });
        // The zone is read once from timedatectl, then preset.
        for (let i = 0; i < 30 && (await frame().locator("#envelope-windows .tz").inputValue()) !== zone; i++) await page.waitForTimeout(1000);
        await frame().locator("#envelope-windows .envelope-window button:text-is('weekend')").click();
        await frame().locator("#envelope-windows .from").fill("01:00");
        await frame().locator("#envelope-rate").check();
        await frame().locator("#envelope-rate-calls").fill("10");
        await frame().locator("#envelope-rollback-tool_error").check();
        await frame().locator("#category-despite").check();
        await frame().locator("#category-reason").fill("the form, in a real Cockpit");
        await frame().locator("#promote-form button:text-is('Promote')").click();
        await frame().locator("#category-patch-errata").getByText("bounded (promoted)").waitFor({ timeout: 60000 });
        await shot("promote-form");
        const env = vm("agent-admin show patch-pilot | jq -c '.agents[\"patch-pilot\"].categories[\"patch-errata\"].envelope'");
        check(env !== null && env.trim() === JSON.stringify({ windows: [{ days: ["sat", "sun"], from: "01:00", to: "04:00", tz: zone }],
            rate: { calls: 10, per: "1h" }, rollback: { on: ["tool_error"] } }),
              "agent-admin promote stored the envelope the form built, in the host's time zone (" + (env || "").trim() + ")");
        await frame().locator("#category-patch-errata button:text-is('Demote')").click();
        await frame().locator("#category-patch-errata").getByText("supervised").waitFor({ timeout: 60000 });
        check(true, "and Demote returns it to supervised");
    });

    await step("a refused configuration shows its error", async () => {
        await go("/agents/patch-pilot");
        const ed = frame().locator("#config-patch-pilot");
        await ed.locator("button:text-is('Edit configuration')").click({ timeout: 60000 });
        const text = ed.locator(".config-yaml");
        await text.waitFor();
        const before = vm("sha256sum /etc/custos/agents/patch-pilot/agent.yaml");
        await text.fill((await text.inputValue()) + "limitz: {}\n");
        await ed.locator("button:text-is('Check')").click();
        await ed.getByText(/limitz/).waitFor({ timeout: 30000 });
        check(true, "Check shows why the configuration is refused");
        await ed.locator("button:text-is('Save and apply')").click();
        // Hung, the note stayed at "Saving patch-pilot…" (0.1.1).
        const saved = await ed.locator(".config-edit .actions span").evaluate(n => new Promise(r => {
            const end = Date.now() + 30000;
            const tick = () => (!n.textContent.startsWith("Saving") && n.textContent.includes("limitz")) || Date.now() > end
                ? r(n.textContent) : setTimeout(tick, 200);
            tick();
        }));
        check(saved.includes("limitz") && await ed.locator("button:text-is('Save and apply')").isEnabled(),
              "Save and apply shows the refusal and can be used again", saved);
        check(vm("sha256sum /etc/custos/agents/patch-pilot/agent.yaml") === before, "the installed configuration is unchanged");
        await ed.locator("button:text-is('Cancel')").click();
    });

    await step("model defaults and an agent from a template", async () => {
        await go("/agents");
        await frame().locator("#defaults-open").click({ timeout: 60000 });
        const d = frame().locator("#model-defaults");
        await d.locator("select[name=provider]").selectOption("openai");
        await d.locator("input[name=model]").fill("qwen3-32b");
        await d.locator("input[name=endpoint]").fill("http://127.0.0.1:8008/v1");
        await d.locator("input[name=credential]").fill("");
        await d.locator("button[name=save]").click();
        await frame().locator("#defaults-note").getByText("Saved").waitFor({ timeout: 60000 });
        check(vm("grep -q 'endpoint: http://127.0.0.1:8008/v1' /etc/custos/defaults.yaml && stat -c %a /etc/custos/defaults.yaml | grep -qx 644") !== null,
              "Model defaults, with administrative access, writes /etc/custos/defaults.yaml");
        await d.locator("button[name=cancel]").click();
        await frame().locator("#create-open").click();
        const form = frame().locator("#create-agent");
        await form.locator("#create-from option[value=service-doctor]").waitFor({ state: "attached", timeout: 60000 });
        await form.locator("#create-from").selectOption("service-doctor");
        await form.locator("#create-name").fill("doctor");
        const yaml = form.locator("textarea");
        await frame().locator("#create-agent textarea").evaluate(t => new Promise(r => {
            const ok = () => t.value.includes("name: doctor") && t.value.includes("127.0.0.1:8008");
            const tick = () => ok() ? r() : setTimeout(tick, 200);
            tick();
        }));
        check((await yaml.inputValue()).includes("# Agent template service-doctor"), "the template's configuration, with the defaults' model, fills the editor");
        check((await frame().locator("#create-template").textContent()).includes("config-lifecycle"), "the template's categories are shown");
        await shot("create-template");
        await form.locator("button[name=create]").click();
        await frame().locator("#agent-created").getByText("from the template").waitFor({ timeout: 90000 });
        check(vm("grep -qx 'name: doctor' /etc/custos/agents/doctor/agent.yaml && grep -q '127.0.0.1:8008' /etc/custos/agents/doctor/agent.yaml && " +
                 "agent-admin show doctor | jq -e '.agents.doctor.mode == \"supervised\" and (.agents.doctor.permissions | length) > 3' >/dev/null && " +
                 "systemctl is-active -q agent@doctor.socket") !== null,
              "the agent is created from the template: its configuration, permissions and mode, its socket");
        vm("agent-admin delete doctor; agent-admin defaults --clear");
    });

    await step("a second host (the contract alone)", async () => {
        // #112: custos-second-host serves the control API and nothing of
        // Custos's launcher; the page lists it and works with it as with
        // the launcher's agents.
        await go("/agents/mock");
        const detail = frame().locator("#agent-detail");
        await detail.getByText("custos-test/second-host").waitFor({ timeout: 60000 });
        const d = await detail.textContent();
        check(d.includes("custos-test/second-host · contract 1") && d.includes("custos-second-host-mock.service"),
              "the page lists the second host: its implementation, contract and unit", d.slice(0, 300));
        check(d.includes("The gateway has no data for this agent"), "and says the gateway has no data for it");
        check(!d.includes("agent@mock."), "its lifecycle names only a service: the page invents no agent@mock unit", d.slice(0, 300));
        // Pause: the gateway has no data for it, so the host's part (§8).
        await detail.locator("button:text-is('Pause')").click();
        check(await vmEventually(30, "curl -s --unix-socket /run/agents/mock/api.sock http://a/v1/status | jq -e '.paused == true' >/dev/null"),
              "Pause pauses the second host (POST /v1/pause)");
        await detail.locator("button:text-is('Resume')").waitFor({ timeout: 60000 });
        await detail.locator("button:text-is('Resume')").click();
        check(await vmEventually(30, "curl -s --unix-socket /run/agents/mock/api.sock http://a/v1/status | jq -e '.paused == false' >/dev/null"),
              "and Resume resumes it");
        await go("/activity/mock/" + mockRun);
        await frame().locator("#activity-mock-" + mockRun).waitFor({ timeout: 60000 });
        check((await frame().locator("#activity-detail").textContent()).includes("api · root"), "its running activity, started by root");
        await frame().locator("#activity-detail button:has-text('Cancel activity')").click();
        check(await vmEventually(30, `[ "$(curl -s --unix-socket /run/agents/mock/api.sock http://a/v1/activities/${mockRun} | jq -r .state)" = cancelled ]`),
              "Cancel activity ends it, through the root bridge (root started it)");
        await shot("second-host");
        vm("systemctl stop custos-second-host-mock.service; rm -rf /run/agents/mock");
    });

    await step("an agent that does not answer", async () => {
        // Its model names a credential that was never stored: the service
        // fails at start and systemd restarts it, holding the socket.
        vm("agent-admin delete nokey >/dev/null 2>&1; printf '%s\\n' 'version: 1' 'name: nokey' " +
           "'model: {provider: openai, name: m, endpoint: \"http://127.0.0.1:8008/v1\", credential: missing}' " +
           "'gateway: {servers: [custos-test]}' | agent-admin create nokey --from -");
        await go("/agents");
        const row = frame().locator("#agent-row-nokey");
        await row.getByText(/not answering/).waitFor({ timeout: 60000 });
        check(true, "an agent whose service fails at start is listed as not answering");
        check(await frame().locator("#agent-row-patch-pilot").isVisible() && await frame().locator("#agent-row-triage").isVisible(),
              "the other agents are listed as usual");
        vm("agent-admin delete nokey");
    });

    await step("the emergency stop", async () => {
        await go("/activity");
        await frame().locator("#stop-all").click();
        await frame().locator("#stop-go").click();
        await frame().locator("#stopped:not([hidden])").waitFor({ timeout: 90000 });
        check((await frame().locator("#stopped").textContent()).includes(user), "the banner names " + user);
        check(vm(`grep -q 'stopped: true' /etc/mcp-gateway/policy/agents/_control.yaml && grep -q 'by: "${user}"' /etc/mcp-gateway/policy/agents/_control.yaml`) !== null,
              "stop-all, with administrative access, records " + user);
        check(vm("! systemctl is-active -q agent@patch-pilot.socket") !== null, "systemd stopped the agents");
        await shot("stopped");
        await frame().locator("#stopped button:has-text('Release…')").click();
        await frame().locator("#stopped button:has-text('Release all agents')").click();
        await frame().locator("#stopped").waitFor({ state: "hidden", timeout: 90000 });
        check(vm("grep -q 'stopped: false' /etc/mcp-gateway/policy/agents/_control.yaml && systemctl is-active -q agent@patch-pilot.socket") !== null,
              "Release: allowed again, the sockets are back");
    });

    check(errors.length === 0, "no errors in the page's scripts", errors.join("\n"));
    if (shellErrors.length) console.log("    | errors outside the page (Cockpit's shell):\n    | " + shellErrors.join("\n    | "));
    // SELinux: no denial for the domains this test drove (enforcing).
    const avc = vm(`ausearch -m AVC,USER_AVC,SELINUX_ERR -ts $(date -d @${t0} '+%x %T') 2>/dev/null | grep -E 'denied|SELINUX_ERR' || true`) || "";
    const ours = avc.split("\n").filter(l => /scontext=[^ ]*:(agent_t|agent_relay_t|mcpsrv_agents_t|mcpgw_t|cockpit_[a-z_]+t):/.test(l));
    check(vm("[ \"$(getenforce)\" = Enforcing ]") !== null, "SELinux is enforcing");
    check(ours.length === 0, "no SELinux denial for the agents, the gateway or Cockpit", ours.slice(0, 10).join("\n"));

    await browser.close();
    console.log(total + " checks, " + failed + " failed");
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
