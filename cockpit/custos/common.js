/* Custos page for Cockpit: shared helpers.
 *
 * The page acts as the logged-in person (CONTRACT.md §2, "People and the
 * control plane"): it reads the agents' sockets (/run/agents/<name>/
 * api.sock, §3–§5) and mcp-gateway's control API, and decides approvals
 * there, always through the person's own bridge, never as root. Only
 * what needs root (cancelling another person's activity, administration)
 * asks for the root bridge (superuser: "try").
 */
"use strict";

const AGENTS_DIR = "/run/agents";
const GATEWAY_SOCKET = "/run/mcp-gateway/control.sock";
const NAME_RE = /^[a-z][a-z0-9-]{0,30}[a-z0-9]$/;

/* native makes a native promise of one of cockpit's (cockpit.spawn,
 * cockpit.file). A handler that throws in a cockpit promise's then or catch
 * does not reject the promise they return: the exception escapes as
 * uncaught and the chain never settles. So a handler that may throw
 * runs on native(…), never on cockpit's promise itself. */
function native(p) {
    return new Promise((resolve, reject) => p.then(resolve, reject));
}

/* cockpit.http rejects with the body beside the error (a second argument,
 * which await drops); keepBody keeps it as ex.body and, for the gateway's
 * JSON errors, its text as ex.message. With timeoutMs it gives up then. */
function keepBody(request, timeoutMs) {
    return new Promise((resolve, reject) => {
        // A socket that systemd holds while its service fails to start
        // accepts the connection and never answers: give up after
        // timeoutMs, so one agent cannot stall the page.
        const timer = timeoutMs && setTimeout(() => {
            if (request.close) request.close();
            reject({ problem: "timeout", message: "no answer within " + timeoutMs / 1000 + " s" });
        }, timeoutMs);
        request.then(v => {
            clearTimeout(timer);
            resolve(v);
        }, (ex, body) => {
            clearTimeout(timer);
            if (ex && typeof body === "string" && body !== "") {
                ex.body = body;
                try {
                    const e = JSON.parse(body);
                    if (e && e.error) ex.message = e.error;
                } catch {
                    ex.message = body.trim(); // the agents' errors are plain text (§5)
                }
            }
            reject(ex);
        });
    });
}

/* GET_TIMEOUT_MS bounds a read of an agent's or the gateway's API. */
const GET_TIMEOUT_MS = 8000;

function client(socket, opts) {
    const http = cockpit.http(Object.assign({ unix: socket }, opts || {}));
    return {
        get: path => keepBody(http.get(path), GET_TIMEOUT_MS).then(JSON.parse),
        post: (path, body) => keepBody(http.post(path, body === undefined ? "" : JSON.stringify(body),
                                                 { "Content-Type": "application/json" })),
    };
}

/* The gateway's control API and an agent's socket, as the person. */
const gateway = client(GATEWAY_SOCKET);
const agentSocket = name => AGENTS_DIR + "/" + name + "/api.sock";
const agentApi = name => client(agentSocket(name));
/* An agent's socket through the root bridge, where Cockpit has it. */
const agentApiAsRoot = name => client(agentSocket(name), { superuser: "try" });

/* Tabs register here: { refresh(), intervalMs, init()? } (main.js). */
const tabs = {};

/* missingServers are the servers a configuration check says are not in
 * gateway.servers, though gateway.tools names their tools (#116). */
function missingServers(message) {
    const out = [];
    for (const m of String(message || "").matchAll(/server "([a-z0-9][a-z0-9_.-]*)" is not in gateway\.servers/g)) {
        if (!out.includes(m[1])) out.push(m[1]);
    }
    return out;
}

/* withServers adds servers to a configuration's gateway.servers: a flow
 * list ([a, b], also inside gateway: {…}) or a block list (- a). null when
 * the configuration has neither: then nothing is offered. */
function withServers(yaml, servers) {
    const flow = /(servers:[ \t]*\[)([^\]\n]*)(\])/;
    const f = yaml.match(flow);
    if (f) {
        const have = f[2].split(",").map(s => s.trim()).filter(Boolean);
        return yaml.replace(flow, (_, a, inner, z) => a + have.concat(servers).join(", ") + z);
    }
    const block = /^([ \t]*)servers:[ \t]*\n((?:\1[ \t]+-[ \t].*(?:\n|$))+)/m;
    const b = yaml.match(block);
    if (b) {
        const indent = b[2].match(/^([ \t]*-[ \t]*)/)[1];
        const items = b[2].endsWith("\n") ? b[2] : b[2] + "\n";
        return yaml.replace(block, (_, ind) => ind + "servers:\n" + items + servers.map(s => indent + s + "\n").join(""));
    }
    return null;
}

/* serverFix is a button that adds the missing servers a check named to the
 * configuration in textarea and checks it again (recheck), or null. */
function serverFix(message, textarea, recheck) {
    const servers = missingServers(message);
    if (servers.length === 0 || withServers(textarea.value, servers) === null) return null;
    const b = el("button", { type: "button", class: "secondary server-fix" },
        "Add " + servers.join(", ") + " to gateway.servers");
    b.addEventListener("click", () => {
        textarea.value = withServers(textarea.value, servers);
        recheck();
    });
    return b;
}

/* nodes is children as DOM nodes: arrays flattened, null, undefined and
 * false left out, the rest as text. */
/* Characters that change or hide what text shows: format and control
 * characters (bidi overrides and isolates, zero-width ones, the byte
 * order mark), line and paragraph separators. A model can put them in a
 * call's arguments or a plan's command, so that what an approver reads
 * differs from what runs (security review M1). They are shown as \uXXXX;
 * newlines and tabs stay. */
const HIDDEN_RE = /[\p{Cf}\p{Cc}\u2028\u2029]/gu;

/* visibleText is the text with those characters shown, and how many. */
function visibleText(s) {
    let hidden = 0;
    const text = String(s == null ? "" : s).replace(HIDDEN_RE, c => {
        if (c === "\n" || c === "\t") return c;
        hidden++;
        return "\\u" + c.codePointAt(0).toString(16).toUpperCase().padStart(4, "0");
    });
    return { text, hidden };
}

/* hiddenMark says that text held such characters (null if none). */
function hiddenMark(v) {
    return v.hidden
        ? el("span", { class: "badge badge-bad hidden-chars", title: "Characters that change how text shows (direction, zero-width), shown as \\uXXXX" },
             v.hidden + " hidden character" + (v.hidden === 1 ? "" : "s"))
        : null;
}

function nodes(...children) {
    return children.flat(Infinity).filter(c => c !== null && c !== undefined && c !== false)
        .map(c => c instanceof Node ? c : document.createTextNode(String(c)));
}

function el(tag, attrs, ...children) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
        if (v === null || v === undefined || v === false) continue;
        if (k === "class") e.className = v;
        else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
        else e.setAttribute(k, v === true ? "" : v);
    }
    e.append(...nodes(children));
    return e;
}

/* relative describes a time relative to now ("in 5 min", "3 h ago"). */
function relative(date) {
    let s = Math.round((new Date(date) - Date.now()) / 1000);
    const past = s < 0;
    s = Math.abs(s);
    let t;
    if (s < 2) return "now";
    if (s < 120) t = s + " s";
    else if (s < 7200) t = Math.round(s / 60) + " min";
    else if (s < 172800) t = Math.round(s / 3600) + " h";
    else t = Math.round(s / 86400) + " days";
    return past ? t + " ago" : "in " + t;
}

function sameDay(a, b) {
    return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/* when is a time of day, with "yesterday" or the date when not today. */
function when(date) {
    const d = new Date(date);
    const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
    const now = new Date();
    if (sameDay(d, now)) return time;
    const y = new Date(now);
    y.setDate(now.getDate() - 1);
    if (sameDay(d, y)) return "yesterday " + time.replace(/:\d\d$/, "");
    return d.toLocaleDateString() + " " + time.replace(/:\d\d$/, "");
}

/* shortApproval abbreviates a gateway approval id for display
 * (a-6c88df4c…); the full one is in the element's title. */
function shortApproval(id) {
    return id && id.length > 12 ? id.slice(0, 10) + "…" : id;
}

/* shortId abbreviates an activity id (e-7c21…) for display. */
function shortId(id) {
    return id && id.length > 6 ? id.slice(0, 6) : id;
}

const STATE_TEXT = {
    running: "running", waiting_approval: "waiting for approval", paused: "paused",
    finished: "finished", failed: "failed", cancelled: "cancelled", limit: "stopped at a limit",
    ok: "ok", error: "error", pending: "pending", approved: "approved", declined: "declined",
};
const STATE_CLASS = {
    running: "badge-running", waiting_approval: "badge-waiting", paused: "badge-waiting", pending: "badge-waiting",
    finished: "badge-ok", ok: "badge-ok", approved: "badge-ok",
    failed: "badge-bad", error: "badge-bad", limit: "badge-bad", declined: "badge-bad",
};

function stateBadge(state) {
    return el("span", { class: "badge " + (STATE_CLASS[state] || "") }, STATE_TEXT[state] || state);
}

/* originText says who or what started an activity (§4 origin). */
function originText(a) {
    const o = a.origin || {};
    switch (o.kind) {
    case "schedule": return "schedule · " + (o.schedule || o.by);
    case "event": return "event · " + o.by;
    case "chat": return "chat · " + (o.by || a.user);
    case "api": return "api · " + (o.by || a.user);
    case "delegation": return "delegated by " + ((a.parent && a.parent.agent) || o.by);
    default: return o.kind || a.user || "";
    }
}

/* listAgents finds the agents on this host (§3): the directories of
 * /run/agents whose socket answers /v1/status with contract 1. */
async function listAgents() {
    let names;
    try {
        names = (await cockpit.spawn(["ls", "-1", AGENTS_DIR], { err: "message" })).split("\n");
    } catch (ex) {
        if (ex && ex.exit_status) return []; // no /run/agents: no agents
        throw ex;
    }
    names = names.map(n => n.trim()).filter(n => NAME_RE.test(n)).sort();
    return Promise.all(names.map(async name => {
        try {
            const status = await agentApi(name).get("/v1/status");
            return status.contract === 1 ? { name, status } : { name, error: "speaks contract " + status.contract };
        } catch (ex) {
            // No socket file: its socket unit is stopped. systemd removes
            // the file (RemoveOnStop=) and leaves the directory, so the
            // directory alone does not mean the agent runs.
            return { name, error: describeAgentFailure(ex), stopped: !!(ex && ex.problem === "not-found") };
        }
    }));
}

/* stoppedLifecycle is a stopped agent's backend, which it cannot tell
 * itself (§3a): an OpenShell agent's socket unit is enabled once it has
 * its sandbox (agent-admin set-credential). Needs no root. */
async function stoppedLifecycle(name) {
    const socket = "agent-openshell@" + name + ".socket";
    try {
        const state = (await cockpit.spawn(["systemctl", "show", "-p", "UnitFileState", "--value", socket], { err: "ignore" })).trim();
        if (state === "enabled") return { kind: "openshell", service: "agent-openshell@" + name + ".service", socket };
    } catch (ex) { /* systemd's answer is all there is */ }
    return { kind: "systemd", service: "agent@" + name + ".service", socket: "agent@" + name + ".socket" };
}

/* loadActivities reads every reachable agent's activities, each with its
 * agent's name. */
async function loadActivities(agents) {
    const lists = await Promise.all(agents.filter(a => a.status).map(async a => {
        try {
            const r = await agentApi(a.name).get("/v1/activities");
            return (r.activities || []).map(x => Object.assign({ agent: a.name }, x));
        } catch (ex) {
            a.error = describeAgentFailure(ex);
            return [];
        }
    }));
    return lists.flat();
}

function describeAgentFailure(ex) {
    if (ex && ex.problem === "access-denied") return "no access to its socket (members of agent-clients have it)";
    if (ex && ex.problem === "not-found") return "not running";
    if (ex && ex.problem === "timeout") return "not answering (" + ex.message + "): its service may be failing to start; see its Logs";
    return (ex && (ex.message || ex.problem)) || String(ex);
}

function describeGatewayFailure(ex) {
    if (ex && ex.problem === "not-found") return "mcp-gateway is not running (" + GATEWAY_SOCKET + " not found).";
    if (ex && ex.problem === "access-denied") return "No access to mcp-gateway's control socket (" + GATEWAY_SOCKET + ").";
    return "Talking to mcp-gateway failed: " + ((ex && (ex.message || ex.problem)) || ex);
}

/* The error box shows two kinds: an action's failure (showError), which
 * stays until the next action or Dismiss, and the periodic refresh's
 * (showRefreshError), which the next good refresh clears. A refresh never
 * clears an action's failure: it would vanish before anyone read it. */
const errors = { action: null, refresh: null };

function renderErrors() {
    const box = document.getElementById("error");
    box.hidden = !errors.action && !errors.refresh;
    box.replaceChildren(
        ...(errors.action ? [el("span", null, errors.action), " ",
            el("button", { class: "link", onclick: () => showError(null) }, "Dismiss")] : []),
        ...(errors.action && errors.refresh ? [el("br")] : []),
        ...(errors.refresh ? [el("span", null, errors.refresh)] : []));
}

function showError(msg) {
    errors.action = msg || null;
    renderErrors();
}

function showRefreshError(msg) {
    errors.refresh = msg || null;
    renderErrors();
}

/* setTabCount shows n after a tab's name; none for 0. */
function setTabCount(tab, n) {
    const badge = document.querySelector("#tabs button[data-tab=" + tab + "] .count");
    if (!badge) return;
    badge.textContent = n > 0 ? String(n) : "";
    badge.hidden = !(n > 0);
}

/* The page's view of the host, shared by the tabs: refreshed by whichever
 * tab is shown. */
const state = { agents: [], activities: [], posture: null, credentials: null, user: null };

async function refreshState() {
    const [agents, posture, credentials] = await Promise.all([listAgents(), loadPosture(), loadCredentials()]);
    // Agents with gateway data but no socket (stopped) are agents too. A
    // directory without a socket and without gateway data is what a
    // deleted agent left behind (agent-admin delete before 0.2.1 kept
    // it until reboot): not an agent.
    if (posture) {
        for (let i = agents.length - 1; i >= 0; i--) {
            if (agents[i].stopped && !posture.agents[agents[i].name]) agents.splice(i, 1);
        }
        for (const name of Object.keys(posture.agents)) {
            if (!agents.some(a => a.name === name)) agents.push({ name, error: "not running", stopped: true });
        }
        agents.sort((x, y) => x.name.localeCompare(y.name));
    }
    await Promise.all(agents.filter(a => a.stopped).map(async a => { a.lifecycle = await stoppedLifecycle(a.name); }));
    state.agents = agents;
    state.posture = posture;
    state.credentials = credentials;
    state.activities = await loadActivities(agents);
    setTabCount("activity", state.activities.filter(a => a.state === "running" || a.state === "waiting_approval").length);
    renderStopBanner();
    return state;
}

/* loadPosture reads what the gateway enforces for the agents: their data
 * (mode, pause, permissions, §6) and the emergency stop (§8), from
 * agent-admin show (the files are readable by everyone). null if it
 * cannot be read. */
async function loadPosture() {
    // As the person; through the root bridge where Cockpit has one and the
    // files are not readable for the person.
    for (const superuser of [undefined, "try"]) {
        try {
            return JSON.parse(await cockpit.spawn(["agent-admin", "show"], { superuser, err: "message" }));
        } catch {
            // next way, or none
        }
    }
    return null;
}

/* admin runs agent-admin through the root bridge (§2: administration
 * needs root), naming the person, whom agent-admin records (by). It
 * rejects with agent-admin's own message. */
function admin(args, input) {
    const proc = cockpit.spawn(["agent-admin", ...args], {
        superuser: "require", err: "message",
        environ: state.user ? ["SUDO_USER=" + state.user] : [],
    });
    if (input !== undefined) proc.input(input);
    return native(proc).catch(ex => {
        throw new Error(ex && ex.problem === "access-denied"
            ? "This needs administrative access (turn it on in Cockpit's top bar)."
            : ((ex && ex.message) || String(ex)).replace(/^agent-admin: /, "").trim());
    });
}

/* systemctl runs systemctl through the root bridge. */
function systemctl(...args) {
    return native(cockpit.spawn(["systemctl", ...args], { superuser: "require", err: "message" })).catch(ex => {
        throw new Error(ex && ex.problem === "access-denied"
            ? "This needs administrative access (turn it on in Cockpit's top bar)."
            : ((ex && ex.message) || String(ex)).trim());
    });
}

/* The gateway's decision for an agent's permission or tool, by the rules
 * of the agents module (gateway/policy/agents.rego). The gateway's own
 * decision is the one that counts: this only explains it. */
const MODE_TEXT = {
    advisory: "Only read-only tools run. Everything that changes the system is denied.",
    supervised: "Read-only tools run. Everything that changes the system waits for an approval.",
    bounded: "Like supervised, but the changes marked autonomous run without asking.",
};

function permKind(p) {
    return p.read_only ? "read-only" : p.category ? "changes · " + p.category : p.autonomous ? "changes · autonomous" : "changes the system";
}

/* A promoted category's envelope (§6a): its windows, as the agents
 * module reads them (days mon…sun or all, from ≤ now < to, in tz or UTC). */
const WEEKDAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];

function inWindow(w, now) {
    const parts = new Intl.DateTimeFormat("en-US", { timeZone: w.tz || "UTC", weekday: "short",
                                                     hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).formatToParts(now);
    const get = t => parts.find(x => x.type === t).value;
    const minutes = s => Number(s.slice(0, 2)) * 60 + Number(s.slice(3, 5));
    const at = Number(get("hour")) * 60 + Number(get("minute"));
    return (w.days || WEEKDAYS).includes(get("weekday").toLowerCase().slice(0, 3)) && minutes(w.from) <= at && at < minutes(w.to);
}

function inEnvelope(c, now) {
    const windows = (c.envelope && c.envelope.windows) || [];
    return windows.length === 0 || windows.some(w => inWindow(w, now));
}

/* categoryMode is the mode of a permission's task category: bounded
 * once promoted, else supervised. */
function categoryMode(p, data) {
    const c = (data.categories || {})[p.category];
    return c && c.mode ? c.mode : "supervised";
}

/* permEffect is [text, badge class] of a permission in force now. */
function permEffect(p, data, control) {
    if (control && control.stopped) return ["denied (emergency stop)", "badge-bad"];
    if (data.paused) return ["denied (paused)", "badge-bad"];
    if (p.read_only) return ["allowed", "badge-ok"];
    if (data.mode === "advisory") return ["denied (advisory mode)", "badge-bad"];
    if (p.category) {
        if (categoryMode(p, data) !== "bounded") return ["needs approval", "badge-waiting"];
        return inEnvelope(data.categories[p.category], new Date())
            ? ["allowed, no approval (in its window)", "badge-ok"] : ["needs approval (outside its window)", "badge-waiting"];
    }
    if (data.mode === "bounded" && p.autonomous) return ["allowed, no approval", "badge-ok"];
    return ["needs approval", "badge-waiting"];
}

function globRe(g) {
    return new RegExp("^" + g.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".") + "$");
}

/* decideTool is what the gateway decides for a call of server__tool by
 * the agent: the most permissive of the permissions that match it, or
 * denied when none does. Argument constraints are not checked. */
function decideTool(data, control, server, tool) {
    const matching = (data.permissions || []).filter(p =>
        p.resource === undefined && p.prompt === undefined &&
        globRe(p.server).test(server) && globRe(p.tool || "*").test(tool));
    if (matching.length === 0) {
        return { text: "denied", cls: "badge-bad", why: "no permission lists it" };
    }
    const rank = { "badge-ok": 2, "badge-waiting": 1, "badge-bad": 0 };
    const [text, cls] = matching.map(p => permEffect(p, data, control)).sort((x, y) => rank[y[1]] - rank[x[1]])[0];
    const p = matching.find(p => permEffect(p, data, control)[1] === cls);
    const args = matching.some(p => p.args) ? "; its argument constraints are not checked here" : "";
    const where = p.category && data.mode !== "advisory" ? "category " + p.category + ", " + categoryMode(p, data) : data.mode + " mode";
    return { text, cls, why: "by the permission " + p.server + "__" + (p.tool || "*") + " (" + permKind(p) + ") in " + where + args };
}

/* findActivity is an activity by agent and id, from the last refresh. */
function findActivity(agent, id) {
    return state.activities.find(a => a.agent === agent && a.id === id);
}

/* chainOf is the delegation chain of a (§9): its root first, a last. A
 * link not on this host (or not kept anymore) is named without details. */
function chainOf(a) {
    const out = [a];
    let cur = a;
    while (cur.parent && cur.parent.activity && out.length < 16) {
        const p = findActivity(cur.parent.agent, cur.parent.activity) ||
                  { agent: cur.parent.agent, id: cur.parent.activity, missing: true };
        out.unshift(p);
        if (p.missing) break;
        cur = p;
    }
    return out;
}

/* chainView shows a chain: the root's origin, then each activity. */
function chainView(chain) {
    const box = el("div", { class: "chain" });
    const root = chain[0];
    if (!root.missing) box.append(el("span", { class: "badge" }, originText(root)));
    chain.forEach((a, i) => {
        if (i > 0 || !root.missing) box.append(el("span", { class: "sep" }, "›"));
        box.append(el("span", { class: "badge" + (i === chain.length - 1 ? " badge-running" : "") },
                      a.agent + " " + shortId(a.id)));
    });
    return box;
}

/* categoryUsable is the gateway's rule (agents.rego, CONTRACT.md §6b):
 * whether permissions of a task category ("" for none) may be used on
 * this host, by the enablement state in agent-admin show. */
function categoryUsable(cat) {
    const e = state.posture && state.posture.enablement;
    if (!e) return true;
    if (e.state === "diagnostics-only") return cat === "diagnostics";
    if (e.state === "categories") return !cat || cat === "diagnostics" || (e.enabled || []).includes(cat);
    return false;
}
