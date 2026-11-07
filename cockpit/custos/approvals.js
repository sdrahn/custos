/* Approvals tab: the calls waiting for a decision (CONTRACT.md §7), each
 * as the approval item: the call, from mcp-gateway, beside the agent's
 * account of it (self-asserted). Decisions go to the gateway as the
 * person, never as root (§2). #/approvals/<id> highlights one.
 */
"use strict";

function scopeTitle(s) {
    if (s === "once") return "Approve once";
    if (s === "session") return "Approve for this session";
    const m = /^(\d+)h$/.exec(s);
    return m ? "Approve for " + m[1] + " h" : "Approve for " + s;
}

function highlightedApproval() {
    const p = cockpit.location.path;
    return p[0] === "approvals" ? p[1] : null;
}

/* callOf finds the agent's call an approval is for, by approval id
 * (§4 calls[].approval_id), with its activity. */
function callOf(approvalId) {
    for (const a of state.activities) {
        const c = (a.calls || []).find(c => c.approval_id === approvalId);
        if (c) return { activity: a, call: c };
    }
    return null;
}

async function decide(id, decision, scope, buttons) {
    buttons.forEach(b => { b.disabled = true });
    try {
        await gateway.post("/v1/approvals/" + encodeURIComponent(id), { decision, scope, via: "cockpit" });
        showError(null);
    } catch (ex) {
        let code = null;
        try { code = JSON.parse(ex.body).code } catch { /* not the gateway's JSON */ }
        showError(ex && ex.status === 404
            ? "Approval " + id + " is no longer pending (decided elsewhere, or timed out)."
            : code === "password_required"
                ? "Approving your own request needs your password: use the MCP Gateway page or mcp-gateway-admin approvals approve " + id + "."
                : ex && ex.status === 403
                    ? "You may not decide approval " + id + ": the gateway's approver rules do not name you."
                    : describeGatewayFailure(ex));
    }
    tabs.approvals.refresh();
}

/* args are the call's arguments, as the gateway gave them, with any
 * hidden characters shown (visibleText). */
function args(a) {
    const v = visibleText(JSON.stringify(a || {}, null, 2));
    return nodes(hiddenMark(v), el("pre", { class: "args" }, v.text));
}

function principalText(p) {
    let t = p.sub;
    if (p.uid !== undefined) t += " (uid " + p.uid + ", by SO_PEERCRED)";
    else if (p.iss) t += " (" + p.iss + ")";
    return t;
}

function gatewayHalf(p) {
    return el("div", { class: "half" },
        el("h3", null, "The call, from mcp-gateway"),
        el("dl", null,
           el("dt", null, "Principal"), el("dd", { class: "mono" }, principalText(p.principal || {})),
           p.prompt ? [el("dt", null, "Policy"), el("dd", null, p.prompt)] : null,
           p.approvers ? [el("dt", null, "Who may decide"), el("dd", { class: "mono" }, p.approvers.join(", "))] : null,
           el("dt", null, "Asked via"), el("dd", null, p.channel)),
        args(p.args),
        p.waiting === false
            ? el("p", { class: "note" }, "The agent is no longer waiting (its call ended, or the gateway restarted). " +
                 "Approving lets its next identical call through.")
            : null);
}

/* suspicionView shows the signs of a prompt injection the agent found in
 * what its model read before the call (CONTRACT.md §7, O14), and those
 * outputs. Self-asserted like the rest of the agent's half: a hint for the
 * person deciding, never a reason to approve. */
function suspicionView(c) {
    const sus = c.suspected_injection || [];
    const pre = c.preceding || [];
    if (!sus.length && !pre.length) return null;
    return el("div", { class: "suspicion" + (sus.length ? " suspected" : "") },
        sus.length
            ? [el("p", null, el("span", { class: "badge badge-bad" }, "suspected prompt injection"), " ",
                  "What the model read before this call looks like instructions to it. Check that the call is what was asked for."),
               el("ul", null, sus.map(s => el("li", null, el("span", { class: "mono" }, s.tool), " ", s.sign,
                   s.quote ? [": ", el("q", { class: "mono" }, s.quote)] : null)))]
            : null,
        pre.length ? el("p", { class: "muted" }, "What the model read before the call (the outputs of the activity's last calls):") : null,
        pre.map(o => el("details", { class: "preceding", open: sus.some(s => s.call_id === o.call_id) },
            el("summary", null, el("span", { class: "mono" }, o.tool), " ", el("span", { class: "muted" }, "(" + o.call_id + ")")),
            el("pre", null, o.excerpt))));
}

function agentHalf(p, found) {
    const head = el("h3", null, "The agent's account", el("span", { class: "badge badge-waiting" }, "self-asserted"));
    if (!found) {
        const sub = (p.principal || {}).sub || "";
        return el("div", { class: "half" }, head, el("p", { class: "muted" }, sub.startsWith("agent-")
            ? "Agent " + sub.slice(6) + " names no call for this approval (yet, or it is not on this host)."
            : sub + " is not an agent of this host: there is no account of the call."));
    }
    const { activity: a, call: c } = found;
    const earlier = (a.calls || []).slice(0, (a.calls || []).indexOf(c));
    return el("div", { class: "half" }, head,
        el("p", null, el("span", { class: "muted" }, "Stated reason and impact: "),
           c.rationale ? [visibleText(c.rationale).text, " ", hiddenMark(visibleText(c.rationale))] : el("span", { class: "muted" }, "none given")),
        suspicionView(c),
        el("div", { class: "muted" }, "Started by"),
        chainView(chainOf(a)),
        el("p", { class: "muted" }, "Evidence: the activity's earlier calls"),
        earlier.length
            ? earlier.map(e => el("div", null, el("span", { class: "mono" }, e.tool), " ", stateBadge(e.state)))
            : el("div", { class: "muted" }, "none"));
}

function renderApprovals(pending) {
    const box = document.getElementById("approvals");
    const focus = highlightedApproval();
    box.replaceChildren();
    if (pending.length === 0) {
        box.append(el("p", { class: "card muted" },
                      focus ? "Approval " + focus + " is not pending (anymore)." : "Nothing to decide."));
        return;
    }
    for (const p of pending) {
        const found = callOf(p.id);
        const buttons = [];
        const actions = el("div", { class: "actions" });
        (p.scopes || ["once"]).forEach((scope, i) => {
            const b = el("button", { class: i === 0 ? null : "secondary", onclick: () => decide(p.id, "approve", scope, buttons) },
                         scopeTitle(scope));
            buttons.push(b);
            actions.append(b);
        });
        const decline = el("button", { class: "danger", onclick: () => decide(p.id, "deny", "", buttons) }, "Decline");
        buttons.push(decline);
        actions.append(decline);
        if (found) {
            actions.append(el("button", { class: "link", onclick: () =>
                cockpit.location.go(["activity", found.activity.agent, found.activity.id]) }, "Open activity"));
        }
        const since = (found && found.call.since) || p.created;
        const agent = found ? found.activity.agent : (p.principal || {}).sub;
        box.append(el("div", { class: "card approval" + (p.id === focus ? " highlight" : ""), id: "approval-" + p.id },
            el("div", { class: "head" },
               el("span", { class: "badge badge-waiting" }, since ? "Waiting " + relative(since).replace(/ ago$/, "") : "Waiting"),
               el("span", { class: "mono" }, p.server + "__" + p.name),
               found && (found.call.suspected_injection || []).length
                   ? el("span", { class: "badge badge-bad", title: "See the agent's account" }, "suspected injection") : null,
               el("span", { class: "muted", title: p.id }, agent + " · approval " + shortApproval(p.id) +
                  (p.expires ? " · times out at " + new Date(p.expires).toLocaleTimeString() : ""))),
            el("div", { class: "halves" }, gatewayHalf(p), agentHalf(p, found)),
            actions));
    }
    const card = focus && document.getElementById("approval-" + focus);
    if (card && !card.dataset.scrolled) {
        card.dataset.scrolled = "1";
        card.scrollIntoView({ block: "center" });
    }
}

/* renderDecided lists today's decisions from the agents' journal records
 * (approval.resolved, §10), as far as the person may read the journal. */
async function renderDecided() {
    const box = document.getElementById("decided");
    let out;
    try {
        out = await cockpit.spawn(["journalctl", "--no-pager", "--since", "today", "-o", "json",
                                   "AGENT_EVENT=approval.resolved"], { err: "message" });
    } catch {
        box.replaceChildren(el("p", { class: "muted" }, "Reading the journal needs access to it (systemd-journal or wheel)."));
        return;
    }
    const recs = out.split("\n").filter(Boolean).map(l => { try { return JSON.parse(l) } catch { return null } })
        .filter(Boolean).reverse();
    box.replaceChildren(...(recs.length
        ? recs.map(r => el("div", { class: "decided" },
            stateBadge(r.AGENT_OUTCOME), el("span", { class: "mono" }, r.AGENT_TOOL),
            el("span", { class: "muted", title: r.AGENT_APPROVAL_ID }, r.AGENT_NAME + " · approval " + shortApproval(r.AGENT_APPROVAL_ID) + " · " +
               when(new Date(Number(r.__REALTIME_TIMESTAMP) / 1000)) +
               (r.AGENT_OUTCOME === "declined" ? " · not executed" : ""))))
        : [el("p", { class: "muted" }, "Nothing decided today.")]));
}

/* renderDemotions lists the automatic demotions of the last week (the
 * demotion evaluator's category.demoted records, CONTRACT.md §6a, §10):
 * what approvers should know, since the agent asks for approvals again.
 * Only root's records count (§10, trusted records). Hidden when there are
 * none, or when the person may not read the journal. */
async function renderDemotions() {
    const box = document.getElementById("demotions");
    let out;
    try {
        out = await cockpit.spawn(["journalctl", "--no-pager", "--since", "-7d", "-o", "json", "_UID=0",
                                   "SYSLOG_IDENTIFIER=custos-promotion", "AGENT_EVENT=category.demoted", "AGENT_BY=custos-demote"],
                                  { err: "message" });
    } catch {
        box.hidden = true;
        return;
    }
    const recs = out.split("\n").filter(Boolean).map(l => { try { return JSON.parse(l) } catch { return null } })
        .filter(r => r && r._UID === "0" && r.AGENT_BY === "custos-demote").reverse();
    box.hidden = recs.length === 0;
    box.replaceChildren(el("h2", null, "Demoted automatically"),
        el("p", { class: "muted" }, "In the last 7 days the demotion evaluator set these task categories back to supervised: " +
           "their changes wait for your approval again."),
        ...recs.map(r => el("div", { class: "demotion", id: "demotion-" + r.AGENT_NAME + "-" + r.AGENT_CATEGORY },
            el("button", { class: "link", onclick: () => cockpit.location.go(["agents", r.AGENT_NAME]) }, r.AGENT_NAME),
            " · ", el("span", { class: "mono" }, r.AGENT_CATEGORY), " · ",
            el("span", { class: "muted" }, when(new Date(Number(r.__REALTIME_TIMESTAMP) / 1000))), el("div", null, r.AGENT_REASON || ""))));
}

/* renderReviews lists the calls that ran without an approval and that
 * the gateway asks their approvers to review (mcp-gateway's review items,
 * its R4; for Custos, a promoted category's autonomous actions, §6a):
 * the items the person may review, read and decided as the person.
 * "Should not have run" counts as an override and demotes the category.
 * Hidden when the gateway has no reviews. Items are kept across the
 * tab's refreshes, so that a reason being typed stays. */
const reviewItems = new Map();

async function renderReviews() {
    const box = document.getElementById("reviews");
    let items;
    try {
        items = await gateway.get("/v1/reviews");
    } catch {
        box.hidden = true; // an mcp-gateway without reviews
        return;
    }
    const open = (items || []).filter(r => !r.verdict);
    for (const id of [...reviewItems.keys()]) {
        if (!open.some(r => r.id === id)) reviewItems.delete(id);
    }
    for (const r of open) {
        if (!reviewItems.has(r.id)) reviewItems.set(r.id, reviewItem(r));
    }
    box.hidden = open.length === 0;
    box.replaceChildren(el("h2", null, "To review"),
        el("p", { class: "muted" }, "Calls that ran without an approval, which mcp-gateway asks you to review (a promoted task " +
           "category's autonomous actions). \"Should not have run\" counts as an override and demotes the category; it needs a reason."),
        ...open.map(r => reviewItems.get(r.id)));
}

function reviewItem(r) {
    const reason = el("input", { class: "review-reason", "aria-label": "Reason", placeholder: "Reason (needed for \"should not have run\")" });
    const note = el("span", { class: "muted" });
    const buttons = [];
    const verdict = (v, label, cls) => {
        const b = el("button", { class: cls }, label);
        b.addEventListener("click", async () => {
            if (v === "incident" && !reason.value.trim()) {
                note.className = "error-text";
                note.textContent = "Say why it should not have run.";
                return;
            }
            buttons.forEach(x => { x.disabled = true });
            try {
                await gateway.post("/v1/reviews/" + encodeURIComponent(r.id), { verdict: v, reason: reason.value.trim(), via: "cockpit" });
                showError(null);
                reviewItems.delete(r.id);
            } catch (ex) {
                let code = null;
                try { code = JSON.parse(ex.body).code } catch { /* not the gateway's JSON */ }
                note.className = "error-text";
                note.textContent = code === "password_required"
                    ? "Reviewing your own call needs your password: use the MCP Gateway page or mcp-gateway-admin reviews."
                    : ex && ex.status === 403 ? "You may not review this call: the gateway's approver rules do not name you."
                        : describeGatewayFailure(ex);
                buttons.forEach(x => { x.disabled = false });
                return;
            }
            tabs.approvals.refresh();
        });
        buttons.push(b);
        return b;
    };
    const sub = (r.principal || {}).sub || "";
    return el("div", { class: "call review", id: "gw-review-" + r.id },
        el("div", { class: "head" },
           el("strong", { class: "mono" }, r.server + "__" + r.name),
           el("span", { class: "muted" }, sub + (r.ran ? " · ran " + when(r.ran) : "")),
           r.outcome && r.outcome !== "ok" ? el("span", { class: "badge badge-bad" }, r.outcome) : null,
           el("span", { class: "muted", title: r.id, style: "margin-left:auto" }, "review " + shortApproval(r.id))),
        r.args ? el("pre", null, JSON.stringify(r.args, null, 2)) : null,
        el("div", { class: "actions" }, reason, verdict("ok", "Fine", "secondary"), verdict("incident", "Should not have run", "danger"), note));
}

tabs.approvals = {
    intervalMs: 2000,
    async refresh() {
        const [pending] = await Promise.all([
            gateway.get("/v1/approvals").catch(ex => { throw { userMessage: describeGatewayFailure(ex) } }),
            refreshState()]);
        renderApprovals(pending);
        setTabCount("approvals", pending.length);
        await Promise.all([renderReviews(), renderDemotions(), renderDecided()]);
    },
};

/* refreshApprovalCount keeps the Approvals tab's count while another tab
 * is shown. */
async function refreshApprovalCount() {
    try {
        setTabCount("approvals", (await gateway.get("/v1/approvals")).length);
    } catch {
        // the tab says why when shown
    }
}
