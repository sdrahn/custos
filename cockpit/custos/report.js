/* A diagnosis report (CONTRACT.md §4a) in an activity's view: the
 * summary, each hypothesis with the model's confidence next to how much
 * of its evidence Custos verified, each quote marked verified or not (the
 * host's check against the tool's output), the findings, and the
 * remediation plan with its commands to copy; and the person's feedback
 * (agent-admin feedback, through the root bridge). Running a playbook
 * from an agent's view is here too (§14c).
 */
"use strict";

const CONFIDENCE_CLASS = { high: "badge-running", medium: "badge-waiting", low: "" };

/* support is how much of a hypothesis's evidence is verified:
 * [text, class]. */
function support(h, evidence) {
    const cited = (h.evidence || []).map(id => evidence[id]).filter(Boolean);
    const ok = cited.filter(e => e.verified).length;
    if (cited.length === 0) return ["no evidence cited", "badge-bad"];
    if (ok === cited.length) return [ok + " of " + cited.length + " quotes verified", "badge-ok"];
    if (ok === 0) return ["no quote verified", "badge-bad"];
    return [ok + " of " + cited.length + " quotes verified", "badge-waiting"];
}

function evidenceView(e) {
    if (!e) return null;
    return el("div", { class: "evidence " + (e.verified ? "verified" : "unverified"), id: "evidence-" + e.id },
        el("div", { class: "head" },
           el("span", { class: "badge " + (e.verified ? "badge-ok" : "badge-bad") }, e.verified ? "✓ verified" : "unverified"),
           el("span", { class: "mono muted" }, e.tool + (e.call_id ? " · call " + e.call_id : ""))),
        hiddenMark(visibleText(e.quote)),
        el("blockquote", { class: "mono" }, visibleText(e.quote).text),
        e.verified ? null : el("div", { class: "muted" }, e.call_id
            ? "Not in what " + e.tool + " returned in this activity: the agent's claim alone."
            : e.tool + " was not called in this activity: the agent's claim alone."));
}

/* copyButton copies text, or selects it where the clipboard is refused. */
function copyButton(text, target) {
    const b = el("button", { class: "link" }, "Copy");
    b.addEventListener("click", async () => {
        try {
            await navigator.clipboard.writeText(text);
            b.textContent = "Copied";
        } catch {
            const r = document.createRange();
            r.selectNodeContents(target);
            const s = window.getSelection();
            s.removeAllRanges();
            s.addRange(r);
            b.textContent = "Selected: copy with Ctrl+C";
        }
        setTimeout(() => { b.textContent = "Copy" }, 2000);
    });
    return b;
}

function reportView(a) {
    const r = a.report;
    if (!r) return null;
    const evidence = {};
    for (const e of r.evidence || []) evidence[e.id] = e;
    const verified = (r.evidence || []).filter(e => e.verified).length;
    return el("section", { id: "report", class: "report" },
        el("h3", null, "Diagnosis report", r.playbook ? el("span", { class: "badge" }, "playbook " + r.playbook) : null),
        el("p", { class: "summary" }, r.summary),
        el("p", { class: "muted" }, "Confidence is the agent's own estimate. ✓ verified means Custos found the quote in what " +
           "that tool returned in this activity (" + verified + " of " + (r.evidence || []).length + " quotes); unverified quotes " +
           "are the agent's claim alone."),
        (r.hypotheses || []).length ? el("h4", null, "Hypotheses") : null,
        (r.hypotheses || []).map(h => {
            const [stext, scls] = support(h, evidence);
            return el("div", { class: "hypothesis", id: "hypothesis-" + h.id },
                el("div", { class: "head" },
                   el("strong", null, h.text),
                   el("span", { class: "badge " + (CONFIDENCE_CLASS[h.confidence] || "") }, "agent's confidence: " + h.confidence),
                   el("span", { class: "badge " + scls }, stext)),
                (h.evidence || []).map(id => evidenceView(evidence[id])));
        }),
        (r.findings || []).length ? el("h4", null, "Findings") : null,
        (r.findings || []).length ? el("ul", null, r.findings.map(f => el("li", null, f.text,
            (f.evidence || []).length ? el("span", { class: "muted" }, " (" + f.evidence.map(id =>
                (evidence[id] && evidence[id].verified ? "✓ " : "unverified ") + id).join(", ") + ")") : null))) : null,
        (r.plan || []).length ? el("h4", null, "Remediation plan, for you to carry out") : null,
        (r.plan || []).length ? el("ol", { class: "plan" }, r.plan.map(p => {
            // The command as it is, with hidden characters shown and every
            // line on its own (security review M1). Copy gives what is
            // shown: with hidden characters, as \uXXXX, never the hidden
            // text a root shell would run.
            const v = visibleText(p.command || "");
            const lines = v.text.split("\n").length;
            const code = p.command ? el("code", null, v.text) : null;
            return el("li", null, el("div", null, p.text),
                code ? el("div", { class: "command" }, code, copyButton(v.hidden ? v.text : p.command, code), hiddenMark(v),
                          lines > 1 ? el("span", { class: "badge badge-bad" }, lines + " lines") : null) : null,
                p.risk ? el("div", { class: "muted" }, "Risk: " + p.risk) : null);
        })) : null,
        feedbackForm(a));
}

/* The feedback forms, per activity: kept across the tab's refreshes. */
const feedbackForms = new Map();

function feedbackForm(a) {
    const key = a.agent + "/" + a.id;
    if (feedbackForms.has(key)) return feedbackForms.get(key);
    const r = a.report;
    const selects = (r.hypotheses || []).map(h => {
        const s = el("select", { name: "h-" + h.id, "aria-label": "Hypothesis " + h.id },
            el("option", { value: "" }, "no verdict"), el("option", { value: "confirmed" }, "confirmed"),
            el("option", { value: "rejected" }, "rejected"), el("option", { value: "unsure" }, "unsure"));
        return { h, s };
    });
    const plan = el("select", { name: "plan", "aria-label": "The plan" },
        el("option", { value: "" }, "no verdict"), el("option", { value: "useful" }, "useful"), el("option", { value: "not_useful" }, "not useful"));
    const executed = el("input", { type: "checkbox", name: "executed" });
    const comment = el("input", { name: "comment", "aria-label": "Comment", placeholder: "What was it really?" });
    const note = el("span", { class: "muted" });
    const send = el("button", { type: "submit" }, "Record feedback");
    const form = el("form", { class: "report-feedback" },
        el("h4", null, "Was it right?"),
        el("p", { class: "muted" }, "Your feedback is recorded with your name (agent-admin feedback) and is the evidence for " +
           "letting this agent do more later. Needs administrative access."),
        selects.map(({ h, s }) => el("label", null, s, " " + h.text)),
        (r.plan || []).length ? el("label", null, plan, " the plan") : null,
        el("label", null, executed, " I carried out the plan"),
        el("label", null, "Comment ", comment),
        el("div", { class: "actions" }, send, note));
    form.addEventListener("submit", async ev => {
        ev.preventDefault();
        const args = ["feedback", a.agent, a.id];
        for (const { h, s } of selects) if (s.value) args.push("--hypothesis", h.id + "=" + s.value);
        if (plan.value) args.push("--plan", plan.value);
        if (executed.checked) args.push("--executed");
        if (comment.value.trim()) args.push("--comment", comment.value.trim());
        if (args.length === 3) {
            note.className = "error-text";
            note.textContent = "Give a verdict, tick the plan, or comment.";
            return;
        }
        send.disabled = true;
        note.className = "muted";
        note.textContent = "Recording…";
        try {
            await admin(args);
            note.className = "ok-text";
            note.textContent = "Recorded. Thank you.";
        } catch (ex) {
            note.className = "error-text";
            note.textContent = ex.message;
        }
        send.disabled = false;
    });
    feedbackForms.set(key, form);
    return form;
}

/* The playbooks (agent-admin playbooks, needing no root), once read. */
let playbooksCache = null;

async function loadPlaybooks() {
    if (playbooksCache) return playbooksCache;
    try {
        playbooksCache = JSON.parse(await cockpit.spawn(["agent-admin", "playbooks"], { err: "message" }));
    } catch {
        playbooksCache = {};
    }
    return playbooksCache;
}

/* The playbooks an agent can run (GET /v1/playbooks, §5): each with
 * runnable, or the tools the gateway does not offer it. An agent from
 * before 0.4.1 has no such request: then every playbook on the host. */
async function agentPlaybooks(name) {
    try {
        return (await agentApi(name).get("/v1/playbooks")).playbooks || {};
    } catch (ex) {
        if (!ex || ex.status !== 404) throw ex;
        const all = await loadPlaybooks();
        return Object.fromEntries(Object.entries(all).map(([n, p]) => [n, { title: p.title, runnable: !p.error, error: p.error }]));
    }
}

/* The run-a-playbook form, per agent: kept across the tab's refreshes. */
const playbookForms = new Map();

function playbookSection(a) {
    if (!a.status) return el("section", { class: "card" }, el("h2", null, "Diagnostic playbooks"),
        el("p", { class: "muted" }, "Unknown while the agent is not reachable."));
    if (playbookForms.has(a.name)) return playbookForms.get(a.name);
    const pick = el("select", { name: "playbook", "aria-label": "Playbook" }, el("option", { value: "" }, "Reading…"));
    const extra = el("input", { name: "request", placeholder: "Optional: what to look at (a unit, a path)", "aria-label": "Request" });
    const note = el("span", { class: "muted" });
    const run = el("button", { type: "submit" }, "Run");
    const others = el("details", { class: "playbooks-unavailable", hidden: true });
    const form = el("form", { class: "card playbook-run", id: "playbook-run" },
        el("h2", null, "Diagnostic playbooks"),
        el("p", { class: "muted" }, "A published diagnostic procedure (CONTRACT.md §14c): the agent's host calls its read-only " +
           "tools through the gateway, which decides each as usual, and the agent ends with a report. Only the playbooks whose " +
           "tools the gateway offers this agent are listed."),
        el("div", { class: "fields" }, el("label", null, "Playbook ", pick), el("label", null, "Request ", extra)),
        el("div", { class: "actions" }, run, note),
        others);
    const fill = () => agentPlaybooks(a.name).then(pbs => {
        const names = Object.keys(pbs).filter(n => pbs[n].runnable).sort();
        const rest = Object.keys(pbs).filter(n => !pbs[n].runnable && !pbs[n].error).sort();
        const keep = pick.value;
        pick.replaceChildren(...(names.length ? names.map(n => el("option", { value: n }, n + ": " + pbs[n].title))
            : [el("option", { value: "" }, Object.keys(pbs).length ? "None this agent can run" : "No playbooks on this host")]));
        if (names.includes(keep)) pick.value = keep;
        pick.disabled = run.disabled = !names.length;
        others.hidden = !rest.length;
        others.replaceChildren(
            el("summary", null, rest.length + " more on this host that this agent cannot run"),
            el("p", { class: "muted" }, "The gateway does not offer this agent the tools they need: give it the permissions " +
               "(an agent made from the health-check template has them), or run them from an agent that has."),
            el("ul", null, rest.map(n => el("li", null, n + ": " + pbs[n].title + " (needs " + (pbs[n].missing || []).join(", ") + ")"))));
    }, ex => {
        pick.replaceChildren(el("option", { value: "" }, "Unknown"));
        pick.disabled = run.disabled = true;
        note.className = "error-text";
        note.textContent = describeAgentFailure(ex);
    });
    fill();
    form.addEventListener("submit", async ev => {
        ev.preventDefault();
        if (!pick.value) return;
        run.disabled = true;
        note.className = "muted";
        note.textContent = "Starting…";
        try {
            // As the person (§5: supervisors start api activities).
            const r = JSON.parse(await agentApi(a.name).post("/v1/activities", { playbook: pick.value, prompt: extra.value.trim() }));
            note.textContent = "";
            cockpit.location.go(["activity", a.name, r.id]);
        } catch (ex) {
            note.className = "error-text";
            note.textContent = (ex && ex.status === 422) || (ex && ex.status === 404)
                ? String((ex && ex.body) || ex.message || "").trim() : describeAgentFailure(ex);
            // Its permissions may have changed since the list was read.
            if (ex && ex.status === 422) fill();
        }
        run.disabled = !pick.value;
    });
    playbookForms.set(a.name, form);
    return form;
}
