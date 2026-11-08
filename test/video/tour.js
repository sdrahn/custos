/* The demonstration videos of mcp-gateway and Custos: a scripted tour of
 * the real pages in the test VM (test/vm/run-vm.sh with SUITE=video, after
 * test/video/vm-setup.sh), recorded by the browser, with subtitles and no
 * audio. Two versions: business (what it does and why it is safe) and
 * technical (how it works).
 *
 *   node tour.js business|technical OUTDIR
 *
 * Writes OUTDIR/<version>.webm (the recording, the pages at 1536×768),
 * and its subtitles with the real timings: <version>.srt and .vtt (for a
 * player), <version>.ass (test/video/render.sh burns them into the MP4,
 * in a band below the pages). Prints PASS/FAIL per scene and exits 1 if a
 * scene failed: a video with a broken scene is not one to show.
 *
 * Environment: COCKPIT_URL, COCKPIT_USER, COCKPIT_PASSWORD, SSH (the ssh
 * command into the VM, as a JSON array), GATEWAY_URL (the gateway's HTTPS
 * origin, its approval page), CHROME (optional).
 */
"use strict";
const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");
const { Subtitles } = require("./subtitles");
let chromium;
try {
    ({ chromium } = require("playwright-core"));
} catch {
    ({ chromium } = require("playwright"));
}

const version = process.argv[2];
const out = process.argv[3];
if (!["business", "technical"].includes(version) || !out) {
    console.error("usage: tour.js business|technical OUTDIR");
    process.exit(2);
}
const env = k => {
    if (!process.env[k]) throw new Error("missing " + k);
    return process.env[k];
};
const url = env("COCKPIT_URL");
const user = env("COCKPIT_USER");
const password = env("COCKPIT_PASSWORD");
const sshCmd = JSON.parse(env("SSH"));
const gateway = env("GATEWAY_URL");
fs.mkdirSync(out, { recursive: true });

const W = 1536, H = 768;
let failed = 0;

/* vm runs a shell command in the VM as root; returns its output, or null. */
function vm(cmd) {
    try {
        return execFileSync(sshCmd[0], [...sshCmd.slice(1), cmd], { encoding: "utf8", timeout: 120000 });
    } catch {
        return null;
    }
}
const sleep = ms => new Promise(r => setTimeout(r, ms));
/* vmEventually waits until cmd succeeds in the VM: its output ("" for
 * none), or null if it never did. */
async function vmEventually(seconds, cmd) {
    for (let i = 0; i < seconds; i++) {
        const o = vm(cmd);
        if (o !== null) return o;
        await sleep(1000);
    }
    return null;
}
/* finished waits until the agent's activity id has finished. */
async function finished(id, what, agent = "web-doctor") {
    const done = await vmEventually(60, `[ "$(${api(agent, "/v1/activities/" + id)} | jq -r .state)" = finished ]`);
    if (done === null) {
        const state = vm(`${api(agent, "/v1/activities/" + id)} | jq -c '{state, error, calls: [.calls[] | {tool, approval_id, error}]}'`);
        throw new Error(what + " did not finish: " + (state || "").trim());
    }
}
const api = (agent, p) => `curl -s --max-time 30 --unix-socket /run/agents/${agent}/api.sock http://a${p}`;

/* Subtitles ---------------------------------------------------------- */

let t0 = 0;
const clock = () => Date.now() - t0;
// The agentic loop's stages, as the strip below the pages shows them.
const STAGES = ["Observe", "Diagnose", "Propose", "Decide", "Act", "Learn", "Promote", "Review", "Demote"];
const subs = new Subtitles(clock, STAGES);
/* caption shows text while it can be read, then ends it: what follows
 * (the next click, a page loading) has no subtitle that is not about it. */
const caption = async (text, hold) => {
    await sleep(subs.show(text, hold));
    subs.end();
};
const endCaption = () => subs.end();
function writeSubtitles(name) {
    fs.writeFileSync(path.join(out, name + ".srt"), subs.srt());
    fs.writeFileSync(path.join(out, name + ".vtt"), subs.vtt());
    fs.writeFileSync(path.join(out, name + ".ass"), subs.ass());
}

/* The pointer, which a recording does not show: a dot in every frame
 * that follows the mouse, and a ring where it clicks. */
const pointer = () => {
    const install = () => {
        if (document.getElementById("tour-pointer") || !document.body) return;
        const d = document.createElement("div");
        d.id = "tour-pointer";
        d.style.cssText = "position:fixed;z-index:2147483647;width:22px;height:22px;margin:-11px 0 0 -11px;border-radius:50%;" +
            "background:rgba(254,124,63,.55);border:2px solid #fe7c3f;pointer-events:none;left:-40px;top:-40px;transition:transform .15s";
        document.body.append(d);
        addEventListener("mousemove", e => { d.style.left = e.clientX + "px"; d.style.top = e.clientY + "px" }, true);
        addEventListener("mousedown", () => { d.style.transform = "scale(1.6)" }, true);
        addEventListener("mouseup", () => { d.style.transform = "" }, true);
        addEventListener("mouseleave", () => { d.style.left = "-40px" }, true);
    };
    if (document.readyState === "loading") addEventListener("DOMContentLoaded", install);
    else install();
};

(async () => {
    const browser = await chromium.launch(Object.assign({ args: ["--ignore-certificate-errors"] },
        process.env.CHROME ? { executablePath: process.env.CHROME } : {}));
    const context = await browser.newContext({
        ignoreHTTPSErrors: true, viewport: { width: W, height: H },
        recordVideo: { dir: out, size: { width: W, height: H } },
    });
    await context.addInitScript(pointer);
    // Administrative access in Cockpit, as remembered from before (the
    // stop, the terminal's sudo are the person's own).
    await context.addInitScript(u => { try { localStorage.setItem("superuser:" + u, "any") } catch { /* none */ } }, user);
    const page = await context.newPage();
    t0 = Date.now();
    let keeper = null;

    const custos = () => page.frameLocator('iframe[name="cockpit1:localhost/custos"]');
    const gw = () => page.frameLocator('iframe[name="cockpit1:localhost/mcp-gateway"]');
    const terminal = () => page.frameLocator('iframe[name="cockpit1:localhost/system/terminal"]');

    const scene = async (name, fn) => {
        try {
            await fn();
            console.log("PASS: " + name);
        } catch (e) {
            failed++;
            console.log("FAIL: " + name + "\n    | " + e.message.split("\n").slice(0, 3).join("\n    | "));
            await page.screenshot({ path: path.join(out, `${version}-failed-${name.replace(/\W+/g, "-")}.png`) }).catch(() => {});
            // What the job's log needs to tell why: where the browser was,
            // what it showed, web-doctor's latest activities, the gateway.
            const shown = await page.evaluate(() => document.body ? document.body.innerText.slice(0, 400) : "").catch(() => "");
            const frames = page.frames().map(f => f.name() || f.url()).join(", ");
            const acts = vm(`${api("web-doctor", "/v1/activities")} | jq -c '[.activities[-3:][] | {id, state, error, origin: .origin.kind}]'`) || "";
            const last = [...acts.matchAll(/"id":"([^"]+)"/g)].map(m => m[1]).pop();
            const calls = last ? vm(`${api("web-doctor", "/v1/activities/" + last)} | jq -c '[.calls[] | {tool, approval_id, error}]'`) || "" : "";
            const gwlog = vm("journalctl -u mcp-gateway.service -o cat --no-pager --since -3min | grep -v '\"audit\":true' | tail -n 12") || "";
            const audit = vm("journalctl -u mcp-gateway.service -o cat --no-pager --since -3min | grep '\"audit\":true' | " +
                "jq -c '{event, id, ok, by, via, error, reason}' 2>/dev/null | tail -n 8") || "";
            for (const [k, v] of [["url", page.url()], ["frames", frames], ["page", shown], ["activities", acts], ["calls", calls],
                                  ["gateway", gwlog], ["audit", audit]])
                for (const l of String(v).trim().split("\n").filter(Boolean)) console.log(`    | ${k}: ${l}`);
        }
    };
    /* point moves the pointer to the element, visibly, and outlines it. */
    const point = async (loc, ms = 600) => {
        await loc.scrollIntoViewIfNeeded({ timeout: 30000 });
        const b = await loc.boundingBox();
        if (!b) return;
        await page.mouse.move(b.x + Math.min(b.width / 2, 120), b.y + b.height / 2, { steps: 25 });
        await loc.evaluate(e => { e.dataset.tourOutline = e.style.outline; e.style.outline = "3px solid #fe7c3f"; e.style.outlineOffset = "2px" }).catch(() => {});
        await sleep(ms);
    };
    const unpoint = loc => loc.evaluate(e => { e.style.outline = e.dataset.tourOutline || "" }).catch(() => {});
    const click = async (loc, ms = 400) => {
        await point(loc, ms);
        await unpoint(loc);
        await loc.click();
        await sleep(500);
    };
    const type = async (loc, text) => {
        await click(loc, 200);
        await loc.pressSequentially(text, { delay: 55 });
        await sleep(400);
    };
    const tab = async (frame, name) => click(frame().locator(`#tabs button[data-tab="${name}"]`));
    /* card shows a title card over the whole page, for ms. */
    const card = async (title, sub, ms = 4500) => {
        // A blank page first: written into Cockpit's, its content security
        // policy would drop the card's styles.
        await page.goto("about:blank");
        await page.setContent(`<!DOCTYPE html><html><body style="margin:0;height:100vh;display:grid;place-items:center;background:#0c322c;` +
            `font-family:'SUSE','DejaVu Sans',sans-serif"><div style="text-align:center;max-width:80%">` +
            `<div style="color:#30ba78;font-size:64px;font-weight:600;line-height:1.15">${title}</div>` +
            (sub ? `<div style="color:#c0efde;font-size:30px;margin-top:28px">${sub}</div>` : "") + `</div></body></html>`);
        endCaption();
        await sleep(ms);
    };
    const openCustos = async () => {
        await page.goto(url + "/custos");
        await custos().locator("#tabs").waitFor({ timeout: 60000 });
        await sleep(800);
    };
    const openGateway = async hash => {
        await page.goto(url + "/mcp-gateway" + (hash ? "#" + hash : ""));
        await gw().locator("#tabs").waitFor({ timeout: 60000 });
        await sleep(800);
    };

    /* Scenes both versions share ----------------------------------- */

    const login = async text => {
        await page.goto(url + "/");
        await caption(text, 1500);
        await type(page.locator("#login-user-input"), user);
        await type(page.locator("#login-password-input"), password);
        await click(page.locator("#login-button"));
        await page.waitForSelector("iframe[name^='cockpit1:']", { timeout: 90000 });
        // Cockpit ends a session some seconds after its last connection
        // closed: while the recorded page is on the gateway's approval
        // page or the identity provider, a second page (not in the
        // videos) keeps carol's session open.
        keeper = await context.newPage();
        await keeper.goto(url + "/system");
        await page.bringToFront();
        await sleep(1500);
    };
    /* show points at loc while text is read. */
    const show = async (loc, text, hold) => {
        await point(loc, 300);
        await caption(text, hold);
        await unpoint(loc);
    };
    const agentView = async name => {
        await tab(custos, "agents");
        await click(custos().locator(`#agent-row-${name} button.link`));
        await custos().locator("#boundaries").waitFor({ timeout: 60000 });
    };
    const category = () => custos().locator("#category-config-lifecycle");
    /* categoryMode waits until config-lifecycle's mode cell says mode. */
    const categoryMode = async mode => {
        await category().waitFor({ timeout: 60000 });
        await category().locator("td").nth(1).getByText(mode, { exact: true }).waitFor({ timeout: 90000 });
    };
    /* failure fails shop-web.service (by systemctl here, or by fail(),
     * typing it in the terminal) and returns web-doctor's activity its
     * event started. */
    const failure = async fail => {
        const before = (vm(`${api("web-doctor", "/v1/activities")} | jq -r '.activities[].id'`) || "").split("\n").filter(Boolean);
        if (fail) await fail();
        else vm("systemctl reset-failed shop-web.service; systemctl start --no-block shop-web.service");
        const id = await vmEventually(60, `${api("web-doctor", "/v1/activities")} | jq -er '[.activities[] | ` +
            `select(.origin.kind == "event") | .id] - ${JSON.stringify(before)} | .[0]'`);
        if (!id) throw new Error("shop-web.service's failure started no activity of web-doctor");
        return id.trim();
    };
    const waitingApproval = async id => {
        const a = await vmEventually(90, `${api("web-doctor", "/v1/activities/" + id)} | jq -er 'select(.state == "waiting_approval") | ` +
            `[.calls[] | select(.approval_id != null and .approval_id != "")][0].approval_id'`);
        if (!a) throw new Error("web-doctor's restart does not wait for an approval");
        return a.trim();
    };
    const openActivity = async id => {
        await tab(custos, "activity");
        const row = custos().locator(`#activity-web-doctor-${id}`);
        await row.waitFor({ timeout: 60000 });
        await click(row);
        await custos().locator("#activity-detail h2").waitFor({ timeout: 30000 });
    };
    /* The terminal, as root: run takes [command, caption] pairs. */
    const terminalRun = async steps => {
        await page.goto(url + "/system/terminal");
        const term = terminal().locator(".xterm-screen, .terminal").first();
        await term.waitFor({ timeout: 60000 });
        // Readable in the video: the terminal's own font size buttons.
        const bigger = terminal().locator("button[aria-label*='ncrease'], button:has-text('+')").first();
        for (let i = 0; i < 4; i++) await bigger.click({ timeout: 5000 }).catch(() => {});
        await click(term);
        await page.keyboard.type("sudo -i", { delay: 45 });
        await page.keyboard.press("Enter");
        await sleep(1500);
        await page.keyboard.type(password, { delay: 30 });
        await page.keyboard.press("Enter");
        await sleep(1500);
        for (const [cmd, text] of steps) {
            await page.keyboard.type("clear; " + cmd, { delay: 40 });
            await page.keyboard.press("Enter");
            await sleep(2500);
            if (text) await caption(text);
        }
        await page.keyboard.type("exit", { delay: 45 });
        await page.keyboard.press("Enter");
    };
    const emergencyStop = async texts => {
        await openCustos();
        await tab(custos, "activity");
        await show(custos().locator("#stop-all"), texts.button);
        await click(custos().locator("#stop-all"));
        await click(custos().locator("#stop-go"));
        await custos().locator("#stopped:not([hidden])").waitFor({ timeout: 90000 });
        await show(custos().locator("#stopped"), texts.stopped);
        await click(custos().locator("#stopped button:has-text('Release…')"));
        await click(custos().locator("#stopped button:has-text('Release all agents')"));
        await custos().locator("#stopped").waitFor({ state: "hidden", timeout: 90000 });
        await caption(texts.released);
    };

    /* The loop (#149), with each version's words. t: the texts. */
    const loop = async t => {
        let first, approval, second;
        await scene("meet web-doctor", async () => {
            await openCustos();
            await agentView("web-doctor");
            await show(custos().locator("#agent-detail"), t.meet);
            await show(custos().locator("#boundaries"), t.supervised);
        });
        await scene("observe: a service fails", async () => {
            subs.stage("Observe");
            first = await failure(t.observeTerminal ? () => terminalRun(t.observeTerminal) : null);
            if (t.observeTerminal) await openCustos();
            await openActivity(first);
            await show(custos().locator("#activity-detail"), t.observe);
        });
        await scene("diagnose: the report and its feedback", async () => {
            subs.stage("Diagnose");
            const report = custos().locator("#report");
            await report.waitFor({ timeout: 90000 });
            await show(report.locator(".hypothesis").first(), t.diagnose);
            const form = custos().locator(".report-feedback");
            await point(form, 200);
            await form.locator("select[name='h-h1']").selectOption("confirmed");
            await form.locator("select[name='plan']").selectOption("useful");
            await click(form.locator("button:has-text('Record feedback')"));
            await form.getByText("Recorded").waitFor({ timeout: 60000 });
            await show(form, t.feedback);
        });
        await scene("propose: the fix waits for a person", async () => {
            subs.stage("Propose");
            approval = await waitingApproval(first);
            await tab(custos, "approvals");
            const item = custos().locator("#approval-" + approval);
            await item.waitFor({ timeout: 60000 });
            await show(item, t.propose);
        });
        await scene("decide: a person approves", async () => {
            subs.stage("Decide");
            await t.decide(approval);
            await finished(first, "web-doctor's approved restart " + first, "web-doctor");
        });
        await scene("act: the fix runs, on the record", async () => {
            subs.stage("Act");
            await openCustos();
            await tab(custos, "audit");
            const a = custos().locator("#action-" + approval);
            await a.waitFor({ timeout: 60000 });
            await a.getByText("executed").waitFor({ timeout: 60000 });
            await show(a, t.act);
            await openGateway("/audit");
            await show(gw().locator("#audit"), t.actGateway);
        });
        await scene("learn: readiness", async () => {
            subs.stage("Learn");
            if (t.learnTerminal) await terminalRun(t.learnTerminal);
            await openCustos();
            await agentView("web-doctor");
            await category().getByText(/^ready$/).waitFor({ timeout: 90000 });
            await show(category(), t.learn);
        });
        await scene("promote: autonomy within an envelope", async () => {
            subs.stage("Promote");
            await click(custos().locator("#promote-form summary"));
            await custos().locator("#category-select").selectOption("config-lifecycle");
            const env = custos().locator("#category-envelope");
            await env.fill("");
            await type(env, "windows: [{days: [mon, tue, wed, thu, fri, sat, sun], from: '00:00', to: '23:59', tz: UTC}]\n" +
                "rate: {calls: 5, per: 1h}");
            await show(env, t.envelope);
            await click(custos().locator("#promote-form button:text-is('Promote')"));
            await categoryMode("bounded");
            await show(category(), t.promote);
        });
        await scene("act alone: the next failure", async () => {
            subs.stage("Act");
            second = await failure(null);
            await finished(second, "web-doctor's autonomous restart " + second, "web-doctor");
            const asked = vm(`${api("web-doctor", "/v1/activities/" + second)} | jq -r '[.calls[] | select(.approval_id != null and .approval_id != "")] | length'`);
            if ((asked || "").trim() !== "0") throw new Error("the promoted category's restart asked for an approval");
            await openCustos();
            await openActivity(second);
            await show(custos().locator("#activity-detail"), t.alone);
        });
        await scene("review: what it did alone", async () => {
            subs.stage("Review");
            await agentView("web-doctor");
            const item = custos().locator(`#category-reviews [id^='review-${second}-']`).first();
            await item.waitFor({ timeout: 90000 });
            await show(item, t.review);
        });
        await scene("demote: back to supervised", async () => {
            subs.stage("Demote");
            await type(custos().locator("#category-reason"), t.reason);
            const item = custos().locator(`#category-reviews [id^='review-${second}-']`).first();
            await click(item.locator("button:has-text('Should not have run')"));
            await categoryMode("supervised");
            await show(category(), t.demote);
            if (t.demoteTerminal) await terminalRun(t.demoteTerminal);
        });
        subs.stage(null);
    };

    /* The versions ---------------------------------------------------- */

    const demo = "A demonstration on a test system: a scripted model and test tools stand in for a real model and real tools.";
    if (version === "business") {
        await card("mcp-gateway and Custos", "The agentic loop: how an AI agent earns trust, and loses it", 5000);
        await caption(demo);
        await scene("log in", () => login("Custos runs on the customer's own SUSE Linux host, managed in Cockpit, the web console administrators already use."));
        await loop({
            meet: "web-doctor looks after the shop's web front. When the service fails, it finds out why and proposes a fix.",
            supervised: "It is supervised: it may look freely, but every change waits for a person.",
            observe: "shop-web.service just failed. systemd started web-doctor at once: nobody had to notice.",
            diagnose: "Its diagnosis: a hypothesis, the evidence for it, and a plan. Quotes Custos checked against the tool's output are marked verified.",
            feedback: "The administrator confirms the diagnosis. That verdict is recorded, and counts as evidence later.",
            propose: "web-doctor proposes the fix from its plan: restart shop-web.service. It must ask, and explains why.",
            decide: async approval => {
                await page.goto(gateway + "/approvals/");
                await page.locator("#username").waitFor({ timeout: 60000 });
                await caption("The approver needs no Cockpit: a mail links to mcp-gateway's approval page, behind the company's sign-in.");
                await type(page.locator("#username"), user);
                await type(page.locator("#password"), password);
                await click(page.locator("button[type=submit]"));
                const decide = page.locator(`a[href="/approvals/${approval}"]`);
                await decide.waitFor({ timeout: 60000 });
                await click(decide);
                await page.locator("button[name=decision][value=approve]").waitFor({ timeout: 30000 });
                await caption("The exact call, for whom, and the agent's reasoning. A person decides; the agent never sees this page.");
                await click(page.locator("button[name=decision][value=approve]"));
                await sleep(1500);
            },
            act: "Approved, the restart runs. Every step is on the record: proposed, approved by carol, executed.",
            actGateway: "mcp-gateway keeps its own record of every decision and its outcome, outside the agent.",
            learn: "Each decision builds a track record per kind of task. Here it is ready after one, because this demo asks little: normally 14 days and 50 proposals.",
            envelope: "The administrator grants autonomy for this kind of task only: within time windows, at most 5 restarts an hour.",
            promote: "Configuration and lifecycle tasks may now run without asking, inside that envelope. Every other kind still asks.",
            alone: "shop-web fails again. This time web-doctor diagnoses and restarts it on its own: no approval needed, inside its envelope.",
            review: "Every action taken alone is put before a person afterwards.",
            reason: "Restarts hide that the upstream app keeps failing.",
            demote: "\"Should not have run\": the task is demoted at once, back to supervised. The next restart asks again. The loop goes on.",
        });
        await scene("the emergency stop", () => emergencyStop({
            button: "And if anything looks wrong: one button stops every agent.",
            stopped: "Stopped by systemd and refused by mcp-gateway at the same time. The banner says who stopped them.",
            released: "Released again, by a person, when it is safe.",
        }));
        await card("AI proposes. People decide.<br>The platform enforces.", "mcp-gateway and Custos · SUSE Linux Enterprise Server 16 and openSUSE Leap 16", 6000);
    } else {
        await card("mcp-gateway and Custos", "The agentic loop: a technical walkthrough", 5000);
        await caption(demo);
        await scene("log in", () => login("The installed packages in openSUSE Leap 16 with SELinux enforcing, driven as the person carol."));
        await loop({
            meet: "web-doctor: a systemd unit (agent@web-doctor) with its own user and SELinux domain. Its event names shop-web.service.",
            supervised: "Its permissions are gateway data: probe is read-only; change is in task category config-lifecycle and needs an approval.",
            observeTerminal: [
                ["systemctl show -p OnFailure --value shop-web.service", "agent-admin put an OnFailure= drop-in on the unit: its failure starts agent-event@."],
                ["systemctl start shop-web.service", "Failing it, as root, in the terminal."],
            ],
            observe: "The event's activity: origin event, by unit-failure:shop-web.service, started as root and damped per unit.",
            diagnose: "The report (report_diagnosis): hypotheses cite evidence; the host verifies each quote against the tool calls' output.",
            feedback: "Feedback through the root bridge (agent-admin feedback), recorded outside the agent as diagnosis.feedback.",
            propose: "The change waits at the gateway: its approval item joins the call with the agent's proposal (R5), marked self-asserted.",
            decide: async approval => {
                await openGateway("/approvals/" + approval);
                const item = gw().locator("#approval-" + approval);
                await item.waitFor({ timeout: 60000 });
                await show(item, "Decided on mcp-gateway's own Cockpit page this time: the same broker as Custos's page, the approval page, mail and the CLI.");
                await click(item.locator("button:not(.danger)").first());
                await gw().locator("#approvals").getByText(/not pending|Nothing to decide/).waitFor({ timeout: 30000 });
            },
            act: "The history joins the agent's records and the gateway's audit by approval id: proposed, approved by carol via cockpit, executed.",
            actGateway: "The gateway's audit: decision, mcp-approval and mcp-result, in mcp-gateway.service's journal.",
            learnTerminal: [
                ["agent-admin readiness web-doctor config-lifecycle | jq -c '.categories[\"config-lifecycle\"] | {ready, proposals, override_rate, unmet}'",
                 "Readiness from the gateway's decisions, not the agent's records. This demo's thresholds are low; the defaults are 14 days and 50 proposals."],
            ],
            learn: "The same measures in Cockpit, read as root through the bridge.",
            envelope: "The envelope: windows the gateway's policy checks, and a rate it enforces per tool (approval_over_rate, its R6).",
            promote: "agent-admin promote: recorded as category.promoted, with the measures, by carol through the root bridge.",
            alone: "The next failure: the change ran without an approval, inside the window and under the rate.",
            review: "Autonomous calls become review items: here, and for the gateway's approvers (its R4).",
            reason: "Restarts hide that the upstream app keeps failing.",
            demote: "An incident verdict demotes the category at once (category.review, category.demoted); custos-demote.timer also demotes on overrides and rollbacks.",
            demoteTerminal: [
                ["journalctl -o json --no-pager -n 3 SYSLOG_IDENTIFIER=custos-promotion AGENT_NAME=web-doctor | jq -c '{AGENT_EVENT, AGENT_CATEGORY, AGENT_MODE_TO, AGENT_VERDICT}'",
                 "root's records of each step, which readiness and the evaluator trust: never the agent's own."],
            ],
        });
        await scene("the emergency stop", () => emergencyStop({
            button: "Stop all agents: agent-admin stop-all, as carol with administrative access.",
            stopped: "The flag first (the gateway denies every agent), then systemd stops every unit and sandbox. The banner names who stopped them.",
            released: "release-all lets them go again; the sockets are back.",
        }));
        await card("Enforced outside the agent", "mcp-gateway · systemd · SELinux · journald — CONTRACT.md is the reference", 6000);
    }

    endCaption();
    const video = page.video();
    const keeperVideo = keeper && keeper.video();
    await context.close();
    await video.saveAs(path.join(out, version + ".webm"));
    await video.delete().catch(() => {});
    if (keeperVideo) await keeperVideo.delete().catch(() => {});
    writeSubtitles(version);
    await browser.close();
    console.log(`${version}: ${(clock() / 1000).toFixed(0)} s, ${subs.cues.length} captions, ${failed} scenes failed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
