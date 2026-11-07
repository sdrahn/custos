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
/* finished waits until patch-pilot's activity id has finished. */
async function finished(id, what) {
    const done = await vmEventually(60, `[ "$(${api("patch-pilot", "/v1/activities/" + id)} | jq -r .state)" = finished ]`);
    if (done === null) {
        const state = vm(`${api("patch-pilot", "/v1/activities/" + id)} | jq -c '{state, error, calls: [.calls[] | {tool, approval_id, error}]}'`);
        throw new Error(what + " did not finish: " + (state || "").trim());
    }
}
const api = (agent, p) => `curl -s --max-time 30 --unix-socket /run/agents/${agent}/api.sock http://a${p}`;

/* waitingChange starts patch-pilot's change (prompt) and waits until it
 * waits for an approval: its activity and approval ids. */
async function waitingChange(prompt) {
    const id = (vm(`${api("patch-pilot", "/v1/activities")} -X POST -d '${JSON.stringify({ prompt })}' | jq -r .id`) || "").trim();
    if (!id) throw new Error("patch-pilot started no activity");
    const approval = await vmEventually(90, `${api("patch-pilot", "/v1/activities/" + id)} | jq -er 'select(.state == "waiting_approval") | ` +
        `[.calls[] | select(.approval_id != null and .approval_id != "")][0].approval_id'`);
    if (!approval) throw new Error("the change does not wait for an approval");
    return { id, approval: approval.trim() };
}
/* scheduledChange runs triage's schedule, which delegates a change to
 * patch-pilot; returns patch-pilot's waiting activity and its approval. */
async function scheduledChange() {
    const before = new Set((vm(`${api("patch-pilot", "/v1/activities")} | jq -r '.activities[].id'`) || "").split("\n"));
    vm("systemctl start --no-block agent-schedule@triage_morning-check.service");
    const id = await vmEventually(90, `${api("patch-pilot", "/v1/activities")} | jq -er '[.activities[] | ` +
        `select(.state == "waiting_approval" and .origin.kind == "delegation") | .id] - ${JSON.stringify([...before])} | .[0]'`);
    if (!id) throw new Error("triage's delegated change does not wait");
    const approval = (vm(`${api("patch-pilot", "/v1/activities/" + id.trim())} | jq -r '[.calls[] | select(.approval_id != null and .approval_id != "")][0].approval_id'`) || "").trim();
    return { id: id.trim(), approval };
}

/* Subtitles ---------------------------------------------------------- */

let t0 = 0;
const clock = () => Date.now() - t0;
const subs = new Subtitles(clock);
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
            // what it showed, patch-pilot's latest activities, the gateway.
            const shown = await page.evaluate(() => document.body ? document.body.innerText.slice(0, 400) : "").catch(() => "");
            const frames = page.frames().map(f => f.name() || f.url()).join(", ");
            const acts = vm(`${api("patch-pilot", "/v1/activities")} | jq -c '[.activities[:3][] | {id, state, error, prompt}]'`) || "";
            const last = (acts.match(/"id":"([^"]+)"/) || [])[1];
            const calls = last ? vm(`${api("patch-pilot", "/v1/activities/" + last)} | jq -c '[.calls[] | {tool, approval_id, outcome, error}]'`) || "" : "";
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
    const approveInCustos = async (change, texts) => {
        await tab(custos, "activity");
        const row = custos().locator(`#activity-patch-pilot-${change.id}`);
        await row.waitFor({ timeout: 60000 });
        await click(row);
        await custos().locator("#activity-detail h2").waitFor({ timeout: 30000 });
        await point(custos().locator("#activity-detail"), 300);
        await caption(texts.chain);
        await unpoint(custos().locator("#activity-detail"));
        await tab(custos, "approvals");
        const item = custos().locator("#approval-" + change.approval);
        await item.waitFor({ timeout: 60000 });
        await point(item, 300);
        await caption(texts.item);
        await unpoint(item);
        const approve = item.locator("button:has-text('Approve once')");
        await point(approve, 300);
        await caption(texts.decide);
        await unpoint(approve);
        await approve.click();
        await item.waitFor({ state: "detached", timeout: 30000 });
        await sleep(1000);
        await finished(change.id, "the approved change " + change.id);
        return change;
    };
    const auditInCustos = async (change, text) => {
        await tab(custos, "audit");
        const a = custos().locator("#action-" + change.approval);
        await a.waitFor({ timeout: 60000 });
        await a.getByText("executed").waitFor({ timeout: 60000 });
        await point(a, 300);
        await caption(text);
        await unpoint(a);
    };
    const gatewayPage = async texts => {
        const change = await waitingChange("CHANGE: install the pending security patches");
        await page.goto(gateway + "/approvals/");
        // Not signed in: on to the identity provider.
        await page.locator("#username").waitFor({ timeout: 60000 });
        await caption(texts.signIn);
        await type(page.locator("#username"), user);
        await type(page.locator("#password"), password);
        await click(page.locator("button[type=submit]"));
        const decide = page.locator(`a[href="/approvals/${change.approval}"]`);
        await decide.waitFor({ timeout: 60000 });
        await caption(texts.list);
        await click(decide);
        await page.locator("button[name=decision][value=approve]").waitFor({ timeout: 30000 });
        await point(page.locator("main, body").first(), 200);
        await caption(texts.detail);
        await click(page.locator("button[name=decision][value=approve]"));
        await sleep(1500);
        await caption(texts.done);
        await finished(change.id, "the change approved on the gateway's page, " + change.id + ",");
    };
    const emergencyStop = async texts => {
        await openCustos();
        await tab(custos, "activity");
        await point(custos().locator("#stop-all"), 300);
        await caption(texts.button);
        await click(custos().locator("#stop-all"));
        await click(custos().locator("#stop-go"));
        await custos().locator("#stopped:not([hidden])").waitFor({ timeout: 90000 });
        await point(custos().locator("#stopped"), 300);
        await caption(texts.stopped);
        await unpoint(custos().locator("#stopped"));
        await click(custos().locator("#stopped button:has-text('Release…')"));
        await click(custos().locator("#stopped button:has-text('Release all agents')"));
        await custos().locator("#stopped").waitFor({ state: "hidden", timeout: 90000 });
        await caption(texts.released);
    };
    const readiness = async text => {
        await tab(custos, "agents");
        await click(custos().locator("#agent-row-patch-pilot button.link"));
        const cat = custos().locator("#category-patch-errata");
        await cat.waitFor({ timeout: 60000 });
        await cat.getByText(/^(not ready|ready)$/).waitFor({ timeout: 60000 });
        await point(cat, 300);
        await caption(text);
        await unpoint(cat);
    };

    /* The versions ---------------------------------------------------- */

    if (version === "business") {
        await card("mcp-gateway and Custos", "Governed AI agents for SUSE Linux");
        await scene("log in", () => login("Custos runs on the customer's own SUSE Linux host, managed in Cockpit, the web console administrators already use."));
        await scene("the agents", async () => {
            await openCustos();
            await tab(custos, "agents");
            await custos().locator("#agent-row-patch-pilot").waitFor({ timeout: 60000 });
            await point(custos().locator("#agent-row-triage"), 200);
            await caption("Each agent has one job. triage looks at the host every morning and hands work to the others.");
            await point(custos().locator("#agent-row-patch-pilot"), 200);
            await caption("patch-pilot keeps the host patched. disk-watch frees space when /var fills up.");
            await click(custos().locator("#agent-row-patch-pilot button.link"));
            await custos().locator("#boundaries").waitFor({ timeout: 60000 });
            await point(custos().locator("#boundaries"), 300);
            await caption("patch-pilot is supervised: it may look freely, but every change waits for a person.");
            await unpoint(custos().locator("#boundaries"));
        });
        let approved;
        await scene("a change waits for a person", async () => {
            approved = await approveInCustos(await scheduledChange(), {
                chain: "This morning triage found work and handed it to patch-pilot. Its change now waits for an approval.",
                item: "The approver sees the exact call, as mcp-gateway received it, and beside it the agent's own reasoning, marked as the agent's claim.",
                decide: "A person decides. mcp-gateway enforces the decision: the agent cannot get around it.",
            });
        });
        await scene("the audit trail", () => auditInCustos(approved, "Every step is recorded: proposed, approved by carol in Cockpit, executed. A declined change is there too."));
        await card("mcp-gateway", "One control point for every tool an agent uses");
        await scene("mcp-gateway in Cockpit", async () => {
            await openGateway("/servers");
            await point(gw().locator("#servers"), 300);
            await caption("mcp-gateway runs the tools on the host: services, patches, snapshots, diagnostics. Agents reach them only through it.");
            await unpoint(gw().locator("#servers"));
            await tab(gw, "clients");
            await point(gw().locator("#clients"), 300);
            await caption("It knows every agent that connects, and decides each call by the customer's policy.");
            await unpoint(gw().locator("#clients"));
            await tab(gw, "audit");
            await point(gw().locator("#audit"), 300);
            await caption("Its own audit log records every decision and its outcome, for the customer to prove who allowed what, and when.");
            await unpoint(gw().locator("#audit"));
        });
        await scene("the gateway's approval page", () => gatewayPage({
            signIn: "Approvers do not need Cockpit. A mail or a notification links to mcp-gateway's own approval page; they sign in with the company's identity provider.",
            list: "The page lists what this approver may decide.",
            detail: "The call and its arguments, for whom, and the scopes the policy offers. The agent never sees this page.",
            done: "Approved: the change runs, and the decision is recorded as made on the approval page.",
        }));
        await scene("autonomy is earned", async () => {
            await openCustos();
            await readiness("Autonomy is earned per task. Readiness counts how often approvers said no, from mcp-gateway's own records. Only a proven task may act alone, within agreed limits.");
        });
        await scene("the emergency stop", () => emergencyStop({
            button: "And if anything looks wrong: one button stops every agent.",
            stopped: "Stopped by systemd and refused by mcp-gateway at the same time. The banner says who stopped them.",
            released: "Released again, by a person, when it is safe.",
        }));
        await card("AI proposes. People decide.<br>The platform enforces.", "mcp-gateway and Custos · SUSE Linux Enterprise Server 16 and openSUSE Leap 16", 6000);
    } else {
        await card("mcp-gateway and Custos", "Technical walkthrough");
        await scene("log in", () => login("Everything here is the installed packages in openSUSE Leap 16 with SELinux enforcing, driven as the person carol."));
        await scene("an agent and its boundaries", async () => {
            await openCustos();
            await tab(custos, "agents");
            await click(custos().locator("#agent-row-patch-pilot button.link"));
            await custos().locator("#boundaries").waitFor({ timeout: 60000 });
            await point(custos().locator("#agent-detail"), 200);
            await caption("Each agent is a systemd unit (agent@NAME) with its own user, SELinux domain agent_t and socket. Its only tools are mcp-gateway's.");
            await unpoint(custos().locator("#agent-detail"));
            await point(custos().locator("#boundaries"), 200);
            await caption("Its permissions are gateway data written by agent-admin: probe is read-only and runs; change is in task category patch-errata and needs an approval.");
            await unpoint(custos().locator("#boundaries"));
        });
        await scene("the gateway's policy decides", async () => {
            await tab(custos, "policy");
            await custos().locator("#posture-patch-pilot").waitFor({ timeout: 60000 });
            await point(custos().locator("#whatif select"), 300);
            await custos().locator("#whatif select").selectOption("patch-pilot");
            await unpoint(custos().locator("#whatif select"));
            await type(custos().locator("#whatif-tool"), "custos-test__change");
            await custos().locator("#whatif-result[data-for='patch-pilot custos-test__change ']").waitFor({ timeout: 30000 });
            await point(custos().locator("#whatif-result"), 200);
            await caption("What would the gateway decide? The page asks mcp-gateway's own OPA policy: this call needs an approval.");
            await custos().locator("#whatif-tool").fill("");
            await type(custos().locator("#whatif-tool"), "custos-test__wipe");
            await custos().locator("#whatif-result[data-for='patch-pilot custos-test__wipe ']").waitFor({ timeout: 30000 });
            await caption("A tool no permission names is denied, with the gateway's reason. The agent's own process enforces nothing.");
            await unpoint(custos().locator("#whatif-result"));
        });
        await scene("a delegated change and its approval", async () => {
            const change = await scheduledChange();
            await approveInCustos(change, {
                chain: "triage's timer started an activity; it delegated through the gateway's agents server (agents__delegate) to patch-pilot. The chain goes back to the schedule.",
                item: "The approval item: the call from the gateway's control API, and the agent's proposal (R5) beside it, marked self-asserted.",
                decide: "Approved as carol through Cockpit's bridge: the gateway's audit names carol, via cockpit, never root.",
            });
        });
        await scene("chat over AG-UI", async () => {
            await tab(custos, "agents");
            await click(custos().locator("#agent-row-patch-pilot button.link"));
            const chat = custos().locator("#chat-patch-pilot");
            await chat.waitFor({ timeout: 60000 });
            await type(chat.locator(".chat-input"), "PROBE the host, please.");
            await click(chat.locator("button:has-text('Send')"));
            await chat.locator(".msg-agent", { hasText: "probe ok" }).waitFor({ timeout: 60000 });
            await point(chat, 200);
            await caption("Chat runs over AG-UI on the agent's socket; each message is an activity of carol, with the same policy and audit.");
            await unpoint(chat);
        });
        await card("mcp-gateway", "Policy, approvals and audit, outside the agent");
        await scene("mcp-gateway's Cockpit page", async () => {
            const change = await waitingChange("CHANGE: restart the web server");
            await openGateway("/approvals/" + change.approval);
            const item = gw().locator("#approval-" + change.approval);
            await item.waitFor({ timeout: 60000 });
            await point(item, 200);
            await caption("mcp-gateway's own Cockpit page lists the pending approvals, live, with the scopes the policy offers.");
            await unpoint(item);
            await click(item.locator("button:not(.danger)").first());
            await gw().locator("#approvals").getByText(/not pending|Nothing to decide/).waitFor({ timeout: 30000 });
            await tab(gw, "servers");
            await point(gw().locator("#servers"), 200);
            await caption("Servers: each MCP server runs as transient systemd units, one SELinux domain per server, started per principal.");
            await unpoint(gw().locator("#servers"));
            await tab(gw, "clients");
            await point(gw().locator("#clients"), 200);
            await caption("Clients: the agents' sessions, identified by the kernel (SO_PEERCRED) on the socket, or by an OAuth token over HTTPS.");
            await unpoint(gw().locator("#clients"));
            await tab(gw, "policy");
            await point(gw().locator("#bindings"), 200);
            await caption("Policy: role bindings and approver rules. Custos adds its agents module; the gateway's OPA decides every call.");
            await unpoint(gw().locator("#bindings"));
            await tab(gw, "audit");
            await point(gw().locator("#audit"), 200);
            await caption("Audit: the decision, mcp-approval and mcp-result records in mcp-gateway.service's journal, keyed by decision id.");
            await unpoint(gw().locator("#audit"));
        });
        await scene("the approval page on the HTTPS listener", () => gatewayPage({
            signIn: "The gateway's own approval page, on its HTTPS listener: OpenID Connect, authorization code with PKCE, at the site's identity provider.",
            list: "Signed in as remote-user carol, an owner of patch-pilot (agent-admin owners): the approvals the policy lets her decide.",
            detail: "The call, its arguments, the principal agent-patch-pilot and the offered scopes. A CSRF-protected form; no token of the agent's.",
            done: "Decided and audited with via=page. The same broker as Cockpit, the command line, mail and webhooks.",
        }));
        await scene("the command line", async () => {
            await page.goto(url + "/system/terminal");
            const term = terminal().locator(".xterm-screen, .terminal").first();
            await term.waitFor({ timeout: 60000 });
            // Readable in the video: the terminal's own font size buttons.
            const bigger = terminal().locator("button[aria-label*='ncrease'], button:has-text('+')").first();
            for (let i = 0; i < 4; i++) await bigger.click({ timeout: 5000 }).catch(() => {});
            await click(term);
            const run = async (cmd, text, wait = 2500) => {
                await page.keyboard.type(cmd, { delay: 45 });
                await page.keyboard.press("Enter");
                await sleep(wait);
                await caption(text);
            };
            await page.keyboard.type("sudo -i", { delay: 45 });
            await page.keyboard.press("Enter");
            await sleep(1500);
            await page.keyboard.type(password, { delay: 30 });
            await page.keyboard.press("Enter");
            await sleep(1500);
            await run("clear; agent-admin show patch-pilot | jq -c '.agents[\"patch-pilot\"] | {mode, owners, permissions: [.permissions[] | {tool, category}]}'",
                      "agent-admin is root's tool: it writes the gateway's data and returns once OPA has loaded it.");
            await run("clear; systemctl show -p IPAddressDeny -p IPAddressAllow agent@patch-pilot.service",
                      "The unit denies every address; agent-admin fence allows the model endpoint and the nameservers at each start.");
            await run("clear; journalctl -o json -n 3 --no-pager SYSLOG_IDENTIFIER=agent-patch-pilot | jq -c '{AGENT_EVENT, AGENT_TOOL, AGENT_INVOCATION}'",
                      "Every step is a journal record with AGENT_* fields; arguments only as a digest. journalctl -o json feeds a SIEM.");
            await run("clear; agent-admin readiness patch-pilot patch-errata | jq -c '[.. | objects | select(has(\"ready\")) | {ready, unmet}][0]'",
                      "Readiness from the gateway's decisions: not ready yet, so promote refuses without --despite-readiness and a reason.");
            await page.keyboard.type("exit", { delay: 45 });
            await page.keyboard.press("Enter");
        });
        await scene("promotion is refused when not ready", async () => {
            await openCustos();
            await readiness("The readiness of patch-errata in Cockpit: proposals, override and incident rates over 30 days.");
            await click(custos().locator("#promote-form summary"));
            await custos().locator("#category-select").selectOption("patch-errata");
            await click(custos().locator("#promote-form button:text-is('Promote')"));
            const error = custos().locator("#error");
            await error.getByText(/not ready/).waitFor({ timeout: 60000 });
            await point(error, 200);
            await caption("Promoting it now is refused. After promotion, custos-demote.timer demotes it again on overrides, rollbacks or an incident.");
            await unpoint(error);
            await click(error.locator("button:text-is('Dismiss')"));
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
