/* Activity tab: what the agents do (CONTRACT.md §4, §5), running first,
 * delegated activities under the one that delegated them. #/activity/
 * <agent>/<id> selects one.
 */
"use strict";

const FILTERS = {
    all: () => true,
    open: a => a.state === "running" || a.state === "waiting_approval" || a.state === "paused",
    unattended: a => !a.origin || a.origin.kind !== "chat",
    chat: a => a.origin && a.origin.kind === "chat",
};
let filter = "all";

const isOpen = FILTERS.open;
const DAY = 24 * 3600 * 1000;

function selected() {
    const p = cockpit.location.path;
    return p[0] === "activity" && p[1] && p[2] ? { agent: p[1], id: p[2] } : null;
}

/* nextTimers are the agents' schedules by when they run next
 * (agent-schedule@<name>_<schedule>.timer, §15), from systemd, as the
 * person: [{next: Date, agent, schedule}]. */
async function nextTimers() {
    let out;
    try {
        out = await cockpit.spawn(["systemctl", "list-timers", "--all", "--output=json", "agent-schedule@*.timer"],
                                  { err: "message" });
    } catch {
        return [];
    }
    let list;
    try {
        list = JSON.parse(out);
    } catch {
        return [];
    }
    return list.filter(t => t.next && t.unit).map(t => {
        const m = t.unit.match(/^agent-schedule@([^_]+)_(.+)\.timer$/);
        return m ? { next: new Date(t.next / 1000), agent: m[1], schedule: m[2] } : null;
    }).filter(Boolean).sort((a, b) => a.next - b.next);
}

function renderSummary(acts, timers) {
    const open = acts.filter(isOpen);
    const set = (id, v, note) => {
        document.getElementById("stat-" + id).textContent = v;
        document.getElementById("stat-" + id + "-note").textContent = note;
    };
    const unattended = open.filter(FILTERS.unattended).length;
    set("running", open.length, open.length ? unattended + " not started in a chat" : "nothing runs");

    const waiting = acts.flatMap(a => (a.calls || []).filter(c => c.state === "waiting_approval"));
    const oldest = waiting.map(c => c.since).filter(Boolean).sort()[0];
    set("waiting", waiting.length, oldest ? "oldest " + relative(oldest).replace(/ ago$/, "") : "");

    if (timers.length) {
        const first = timers[0];
        const same = timers.filter(t => t.next.getTime() === first.next.getTime());
        const time = first.next.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
        const day = sameDay(first.next, new Date()) ? "today" : relative(first.next);
        set("next", time, day + " · " + same.map(t => t.schedule).join(", "));
    } else {
        set("next", "–", "no schedules");
    }

    const ended = acts.filter(a => a.ended && Date.now() - new Date(a.ended) < DAY);
    const bad = ["cancelled", "failed", "limit"].map(s => [s, ended.filter(a => a.state === s).length])
        .filter(([, n]) => n > 0).map(([s, n]) => n + " " + STATE_TEXT[s]);
    set("ended", ended.length, bad.join(", "));
}

/* ordered puts each delegated activity right after the one that
 * delegated it, as a child, when that one is in the list. */
function ordered(acts) {
    const key = (agent, id) => agent + "/" + id;
    const byKey = new Map(acts.map(a => [key(a.agent, a.id), a]));
    const children = new Map();
    const roots = [];
    for (const a of acts) {
        const p = a.parent && byKey.get(key(a.parent.agent, a.parent.activity));
        if (p && p !== a) {
            const k = key(p.agent, p.id);
            if (!children.has(k)) children.set(k, []);
            children.get(k).push(a);
        } else {
            roots.push(a);
        }
    }
    const out = [];
    const emit = (a, depth) => {
        out.push({ a, depth });
        for (const c of children.get(key(a.agent, a.id)) || []) emit(c, depth + 1);
    };
    roots.forEach(a => emit(a, 0));
    return out;
}

function sortActivities(acts) {
    return acts.slice().sort((x, y) => (isOpen(y) - isOpen(x)) || (new Date(y.started) - new Date(x.started)));
}

function renderList(acts) {
    const box = document.getElementById("activities");
    const sel = selected();
    const shown = sortActivities(acts.filter(FILTERS[filter]));
    box.replaceChildren();
    if (shown.length === 0) {
        box.append(el("p", { class: "muted" }, acts.length ? "No activity matches." : "No activities yet."));
        return;
    }
    for (const { a, depth } of ordered(shown)) {
        const calls = (a.calls || []).length;
        const current = sel && sel.agent === a.agent && sel.id === a.id;
        box.append(el("button", {
            class: "activity" + (depth > 0 ? " child" : ""), "aria-current": current ? "true" : null,
            id: "activity-" + a.agent + "-" + a.id,
            onclick: () => cockpit.location.go(["activity", a.agent, a.id]),
        },
        el("div", { class: "head" }, depth > 0 ? el("span", { class: "muted" }, "└") : null,
           el("span", { class: "agent" }, a.agent), stateBadge(a.state), el("span", { class: "badge" }, originText(a))),
        a.summary ? el("div", { class: "summary" }, a.summary) : null,
        el("div", { class: "muted" }, shortId(a.id) + " · started " + when(a.started) + " · " +
           calls + (calls === 1 ? " tool call" : " tool calls"))));
    }
}

async function cancelActivity(a, button) {
    button.disabled = true;
    const path = "/v1/activities/" + encodeURIComponent(a.id) + "/cancel";
    try {
        // As the person (their own activity), else through the root
        // bridge (§5: root, or the activity's user or originator).
        await agentApi(a.agent).post(path).catch(ex => {
            if (ex && ex.status === 403) return agentApiAsRoot(a.agent).post(path);
            throw ex;
        });
        showError(null);
    } catch (ex) {
        showError("Cancelling " + a.agent + " " + shortId(a.id) + " failed: " +
                  (ex && ex.status === 403 ? "only its user, who started it, or an administrator may" : describeAgentFailure(ex)));
    }
    tabs.activity.refresh();
}

/* showResponse opens the agent's full response (§4 response, from
 * GET /v1/activities/{id}), rendered from its Markdown, in a dialog. */
async function showResponse(a, button) {
    button.disabled = true;
    let full;
    try {
        full = await agentApi(a.agent).get("/v1/activities/" + encodeURIComponent(a.id));
    } catch (ex) {
        showError("Reading the response of " + a.agent + " " + shortId(a.id) + " failed: " + describeAgentFailure(ex));
        button.disabled = false;
        return;
    }
    button.disabled = false;
    // An agent from before the response was kept has only the summary.
    const text = full.response || full.summary || "";
    const copy = el("button", { type: "button" }, "Copy as text");
    const close = el("button", { type: "button" }, "Close");
    const dlg = el("dialog", { class: "response-dialog", id: "response-dialog", "aria-labelledby": "response-title" },
        el("div", { class: "toolbar" },
           el("div", null, el("div", { class: "muted" }, "Activity " + shortId(a.id) + (a.playbook ? " · playbook " + a.playbook : "")),
              el("h2", { id: "response-title" }, "Response of " + a.agent)),
           el("span", { class: "actions" }, copy, close)),
        el("p", { class: "muted" }, "The agent's own words, as its model wrote them: not checked by the host."),
        el("div", { class: "md" }, markdown(text)));
    copy.addEventListener("click", () => {
        navigator.clipboard.writeText(text).then(() => { copy.textContent = "Copied"; }, () => { copy.textContent = "Copy failed"; });
    });
    close.addEventListener("click", () => dlg.close());
    dlg.addEventListener("close", () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
}

/* modelView says how the model went about it: its calls, how much it
 * thought (model.reasoning_effort; the thinking itself is never shown or
 * kept), and whether the host had to ask for the playbook's report (the
 * report nudge, §14c). */
function modelView(a) {
    const lines = [];
    if (a.thought_bytes) {
        lines.push(el("li", { id: "model-thought" }, "It thought " + (a.thought_bytes >= 10240
            ? Math.round(a.thought_bytes / 1024) + " KB" : a.thought_bytes + " bytes") + " before answering (the thinking is not kept; " +
            "model.reasoning_effort: none turns it off)."));
    }
    if (a.report_nudged) {
        lines.push(el("li", { id: "model-nudged" }, "It answered the playbook in text, without a report; the host asked it once to file one" +
            (a.report ? ", and it did." : ". It did not: its answer is the response above.")));
    }
    if (!lines.length) return null;
    return el("div", { id: "model-view" }, el("h3", null, "The model"),
        el("p", { class: "muted" }, (a.llm_calls || 0) + (a.llm_calls === 1 ? " model call." : " model calls.")),
        el("ul", null, lines));
}

/* responseView is the start of the agent's response, with a button for
 * all of it. */
function responseView(a) {
    if (!a.summary) return null;
    const more = el("button", { type: "button", id: "show-response" }, "Show the full response");
    more.addEventListener("click", () => showResponse(a, more));
    return el("div", { class: "response" },
        el("h3", null, "Response"),
        el("p", { class: "response-start" }, a.summary),
        el("div", null, more));
}

function renderDetail(acts) {
    const box = document.getElementById("activity-detail");
    const sel = selected();
    const a = sel && acts.find(x => x.agent === sel.agent && x.id === sel.id);
    if (!a) {
        box.replaceChildren(el("p", { class: "muted" }, sel
            ? "Activity " + shortId(sel.id) + " of " + sel.agent + " is not kept anymore (the journal has it)."
            : "Select an activity to see who started it and its tool calls."));
        return;
    }
    const cancel = isOpen(a) ? el("button", { class: "danger" }, "Cancel activity") : null;
    if (cancel) cancel.addEventListener("click", () => cancelActivity(a, cancel));
    const chain = chainOf(a);
    box.replaceChildren(...nodes(
        el("div", { class: "toolbar" },
           el("div", null, el("div", { class: "muted" }, "Activity " + shortId(a.id)), el("h2", null, a.agent)),
           el("span", { class: "actions" }, cancel)),
        el("h3", null, "Started by"),
        chainView(chain),
        chain.length > 1
            ? el("p", { class: "muted" }, "Each link is checked twice: the callee saw the caller by SO_PEERCRED, " +
                 "and the gateway logged its agents__delegate call.")
            : null,
        a.reason ? el("p", null, el("span", { class: "muted" }, "Reason: "), a.reason) : null,
        responseView(a),
        modelView(a),
        reportView(a),
        el("h3", null, "Tool calls"),
        (a.calls || []).length ? null : el("p", { class: "muted" }, "None yet."),
        (a.calls || []).map(c => el("div", { class: "call" },
            el("div", { class: "head" },
               el("span", { class: "mono muted" }, c.since ? when(c.since) : ""), el("span", { class: "mono" }, c.tool),
               stateBadge(c.state)),
            c.rationale ? el("div", null, el("span", { class: "muted" }, "Rationale (the agent's): "), "“" + c.rationale + "”") : null,
            (c.suspected_injection || []).length
                ? el("div", { class: "suspected-injection" }, el("span", { class: "badge badge-bad" }, "suspected prompt injection"), " ",
                     el("span", { class: "muted" }, c.suspected_injection.map(s => s.tool + " " + s.sign).join("; ")))
                : null,
            c.approval_id
                ? el("div", null, c.approval === "pending"
                    ? el("button", { class: "link", onclick: () => cockpit.location.go(["approvals", c.approval_id]) },
                         "Review approval " + shortApproval(c.approval_id))
                    : el("span", { class: "muted", title: c.approval_id }, "Approval " + shortApproval(c.approval_id) + ": " + (STATE_TEXT[c.approval] || c.approval)))
                : null)),
        el("p", { class: "mono muted" }, "GET " + agentSocket(a.agent) + " /v1/activities/" + a.id)));
}

function renderAgentsNote(agents) {
    const note = document.getElementById("agents-note");
    const bad = agents.filter(a => a.error);
    note.hidden = agents.length > 0 && bad.length === 0;
    note.textContent = agents.length === 0
        ? "No agents on this host (" + AGENTS_DIR + " is empty). Administrators create them with agent-admin create."
        : bad.map(a => a.name + ": " + a.error).join("; ");
}

tabs.activity = {
    intervalMs: 3000,
    init() {
        for (const b of document.querySelectorAll("#filters button")) {
            b.addEventListener("click", () => { filter = b.dataset.filter; tabs.activity.refresh() });
        }
    },
    async refresh() {
        const [{ agents, activities }, timers] = await Promise.all([refreshState(), nextTimers()]);
        for (const b of document.querySelectorAll("#filters button")) {
            b.setAttribute("aria-pressed", String(b.dataset.filter === filter));
        }
        renderAgentsNote(agents);
        renderSummary(activities, timers);
        renderList(activities);
        // Not while someone fills in a report's feedback.
        const f = document.activeElement;
        if (!(f && f.closest(".report-feedback"))) renderDetail(activities);
    },
};
