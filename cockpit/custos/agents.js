/* Agents tab: the agents on this host, and one agent's posture and
 * controls: its autonomy mode and permissions as the gateway enforces
 * them (§6), its schedules, pause and stop (§8). #/agents/<name> selects
 * one. Everything that changes something goes through the root bridge
 * (agent-admin, systemctl); reading goes as the person.
 */
"use strict";

function selectedAgent() {
    const p = cockpit.location.path;
    return p[0] === "agents" && p[1] ? p[1] : null;
}

/* agentState is [text, state for the badge] of an agent. */
function agentState(a) {
    const data = state.posture && state.posture.agents[a.name];
    if (a.stopped) return ["stopped", "failed"];
    // The seal (§6b, #158): data changed outside agent-admin denies the
    // agent everything; an earlier signed version still counts.
    if (data && data.tampered && !data.tampered.startsWith("an earlier version")) return ["seal broken", "failed"];
    if (!a.status) return ["unreachable", "error"];
    if (a.status.paused || (data && data.paused)) return ["paused", "paused"];
    return a.status.running > 0 ? [a.status.running + " running", "running"] : ["idle (socket listening)", "finished"];
}

function modeBadge(mode) {
    return el("span", { class: "badge" + (mode === "bounded" ? " badge-waiting" : mode === "supervised" ? " badge-running" : "") },
              mode || "no gateway data");
}

/* unitsOf are the units that stop and start the agent (§3a lifecycle):
 * its socket and service, so that a stopped agent stays stopped (a
 * request to a listening socket would start it again), and its socket to
 * start it, which starts the service on the next request. A systemd host
 * that is not socket-activated names only its service (§3): that one
 * stops and starts it. (An OpenShell agent's lifecycle names no socket,
 * but has one: agent-openshell@<name>.socket.) For a stopped agent, which has no status, the lifecycle
 * refreshState found. */
function unitsOf(a) {
    const l = (a.status && a.status.lifecycle) || a.lifecycle || {};
    const openshell = l.kind === "openshell";
    const service = l.service || (openshell ? "agent-openshell@" : "agent@") + a.name + ".service";
    if (l.kind === "systemd" && l.service && !l.socket) return { stop: [service], start: [service], service };
    const socket = l.socket || (openshell ? "agent-openshell@" : "agent@") + a.name + ".socket";
    return { stop: [socket, service], start: [socket], service };
}

/* act runs one administrative action from a button, then refreshes. */
async function act(button, what, fn) {
    if (button) button.disabled = true;
    const note = document.getElementById("agent-action");
    if (note) note.textContent = what + "…";
    try {
        await fn();
        if (note) note.textContent = "";
        showError(null);
    } catch (ex) {
        if (note) note.textContent = "";
        showError(what + " failed: " + ((ex && ex.message) || ex));
    }
    tabs.agents.refresh();
}

/* relativeSpan says a number of seconds as minutes or hours. */
function relativeSpan(s) {
    if (s < 120) return s + " s";
    if (s < 7200) return Math.round(s / 60) + " min";
    return Math.round(s / 3600) + " h";
}

/* timersOf are the schedule timers' next runs and whether they are
 * enabled, read as the person. */
async function timersOf(a) {
    const scheds = (a.status && a.status.schedules) || [];
    if (scheds.length === 0) return {};
    const units = scheds.map(s => "agent-schedule@" + a.name + "_" + s.name + ".timer");
    // One unit per call: is-enabled fails when any unit it names is not.
    const enabled = await Promise.all(units.map(u => cockpit.spawn(["systemctl", "is-enabled", u], { err: "ignore" })
        .then(out => String(out).trim(), () => "disabled")));
    const next = await nextTimers();
    const out = {};
    scheds.forEach((s, i) => {
        const t = next.find(t => t.agent === a.name && t.schedule === s.name);
        out[s.name] = { unit: units[i], enabled: (enabled[i] || "").trim() === "enabled", next: t && t.next };
    });
    return out;
}

function renderAgentList() {
    const body = document.querySelector("#agents tbody");
    const sel = selectedAgent();
    body.replaceChildren();
    if (state.agents.length === 0) {
        body.append(el("tr", null, el("td", { colspan: "6", class: "muted" },
            "No agents on this host. Administrators create them with agent-admin create NAME --from agent.yaml.")));
        return;
    }
    for (const a of state.agents) {
        const data = state.posture && state.posture.agents[a.name];
        const [text, st] = agentState(a);
        const scheds = (a.status && a.status.schedules) || [];
        const now = state.activities.filter(x => x.agent === a.name && isOpen(x))[0];
        body.append(el("tr", { class: sel === a.name ? "current" : null, id: "agent-row-" + a.name },
            el("td", null, el("button", { class: "link", onclick: () => cockpit.location.go(["agents", a.name]) }, a.name)),
            el("td", null, stateBadgeText(text, st), missingCredentials(a.name).length
                ? [" ", el("span", { class: "badge badge-bad", title: "Missing: " + missingCredentials(a.name).join(", ") }, "credential missing")] : null),
            el("td", null, modeBadge(data && data.mode)),
            el("td", null, a.status ? [a.status.implementation, el("div", { class: "muted" }, a.status.model)] : "–"),
            el("td", { class: "muted" }, scheds.length ? scheds.map(s => s.name).join(", ") : "none"),
            el("td", { class: "muted" }, now ? now.summary || shortId(now.id) : a.error || "")));
    }
}

function stateBadgeText(text, st) {
    return el("span", { class: "badge " + (STATE_CLASS[st] || "") }, text);
}

async function renderAgentDetail() {
    const box = document.getElementById("agent-detail");
    const name = selectedAgent();
    // The note about an agent just created, while it is shown.
    document.getElementById("agent-created").hidden = !name || justCreated !== name;
    const a = name && state.agents.find(x => x.name === name);
    box.hidden = !name;
    if (!name) return;
    if (!a) {
        box.replaceChildren(el("p", { class: "card muted" }, "There is no agent " + name + " on this host."));
        return;
    }
    const data = state.posture && state.posture.agents[a.name];
    const control = state.posture && state.posture.control;
    const [text, st] = agentState(a);
    const paused = st === "paused";
    const units = unitsOf(a);

    const pause = el("button", { class: "secondary" }, paused ? "Resume" : "Pause");
    pause.addEventListener("click", () => act(pause, (paused ? "Resuming " : "Pausing ") + a.name,
        () => admin([paused ? "resume" : "pause", a.name])));
    const stop = el("button", { class: a.stopped ? "secondary" : "danger" }, a.stopped ? "Start" : "Stop");
    stop.addEventListener("click", () => act(stop, (a.stopped ? "Starting " : "Stopping ") + a.name,
        () => systemctl(a.stopped ? "start" : "stop", ...(a.stopped ? units.start : units.stop))));

    // Restart: the agent's service (an OpenShell agent's relay, with its
    // sandbox). Its running activities end, as with agent-admin edit.
    const restart = el("button", { class: "secondary" }, "Restart");
    restart.hidden = !!a.stopped;
    restart.addEventListener("click", () => act(restart, "Restarting " + a.name, () => systemctl("restart", units.service)));
    // Logs: Cockpit's journal for the agent's unit, which holds the unit's
    // messages and the agent's own records (agent-<name>, §10).
    const logs = el("button", { class: "secondary" }, "Logs");
    logs.addEventListener("click", () => cockpit.jump("/system/logs#/?priority=info&service=" + encodeURIComponent(units.service)));

    const del = deleteControl(a);
    const head = el("section", { class: "card" },
        el("div", { class: "toolbar" },
           el("div", null,
              el("div", { class: "head-line" }, el("h2", null, a.name), stateBadgeText(text, st), modeBadge(data && data.mode)),
              el("div", { class: "mono muted" }, [units.stop.join(" · "),
                  a.status ? a.status.implementation + " · contract " + a.status.contract : null,
                  a.status && a.status.model ? "model " + a.status.model : null].filter(Boolean).join(" · "))),
           el("span", { class: "actions" }, el("span", { id: "agent-action", class: "muted" }), pause, restart, stop, logs, del.open)),
        data && data.paused && data.paused_by ? el("p", { class: "muted" }, "Paused by " + data.paused_by + ".") : null,
        data && data.tampered ? el("p", { class: "error-text", id: "agent-tampered" },
            "Its gateway data was changed outside agent-admin: " + data.tampered + ".") : null,
        del.box);

    const mode = el("section", { class: "card" }, el("h2", null, "Autonomy mode"));
    if (data) {
        const group = el("div", { class: "modes", role: "radiogroup", "aria-label": "Autonomy mode" });
        for (const m of ["advisory", "supervised", "bounded"]) {
            const b = el("button", { class: "mode", role: "radio", "aria-checked": String(data.mode === m), "data-mode": m },
                         el("strong", null, m), el("div", { class: "muted" }, MODE_TEXT[m]));
            if (data.mode !== m) b.addEventListener("click", () => act(b, "Setting " + a.name + " to " + m, () => admin(["mode", a.name, m])));
            group.append(b);
        }
        mode.append(group, el("p", { class: "muted" }, "Enforced by mcp-gateway from /etc/mcp-gateway/policy/agents/" +
            a.name + ".yaml. Tool annotations from MCP servers never allow anything."), ownersEditor(a, data));
    } else {
        mode.append(el("p", { class: "muted" }, "The gateway has no data for this agent: it denies it everything."));
    }

    const bounds = el("section", { class: "card" }, el("h2", null, "Tool boundaries"),
        el("p", { class: "muted" }, "What the gateway decides for agent-" + a.name + " in the current mode. Unlisted tools are denied."));
    if (data) {
        const perms = data.permissions || [];
        const table = el("table", { id: "boundaries" }, el("thead", null, el("tr", null,
            el("th", null, "Tool"), el("th", null, "Declared as"), el("th", null, "Arguments"), el("th", null, "Effect now"))));
        const tbody = el("tbody");
        if (perms.length === 0) tbody.append(el("tr", null, el("td", { colspan: "4", class: "muted" }, "No permissions: the agent can do nothing.")));
        for (const p of perms) {
            const [eff, cls] = permEffect(p, data, control);
            tbody.append(el("tr", null,
                el("td", { class: "mono" }, p.server + "__" + (p.tool || p.resource || p.prompt || "*")),
                el("td", null, permKind(p)),
                el("td", { class: "mono muted" }, p.args ? Object.entries(p.args).map(([k, v]) => k + " ~ " + v).join(", ") : "any"),
                el("td", null, el("span", { class: "badge " + cls }, eff))));
        }
        table.append(tbody);
        const current = perms.map(p => "- " + JSON.stringify(p)).join("\n") + "\n";
        const ed = keptEditor("permissions:" + a.name, current, () => {
            const yaml = el("textarea", { id: "permissions-yaml", spellcheck: "false", "aria-label": "Permissions (YAML list)" });
            const save = el("button", null, "Save permissions");
            const node = el("details", { class: "permissions-edit" }, el("summary", null, "Edit the permissions"),
                el("p", { class: "muted" }, "A YAML list of gateway permissions, with read_only: true for tools that only read and " +
                   "autonomous: true for changes bounded mode may make without asking (§6), or category: NAME for a task category, which " +
                   "agent-admin promote lets act without asking within its windows (§6a). agent-admin checks it, and returns " +
                   "once the gateway enforces it."),
                yaml, el("div", { class: "actions" }, save));
            const k = { node, field: yaml };
            save.addEventListener("click", () => act(save, "Saving the permissions of " + a.name,
                () => admin(["permissions", a.name], yaml.value).then(() => { k.dirty = false; node.open = false })
                    .finally(() => { save.disabled = false })));
            return k;
        });
        if (!ed.dirty) ed.field.rows = Math.max(4, perms.length + 2);
        bounds.append(table, ed.node);
    }

    const scheds = (a.status && a.status.schedules) || [];
    const timers = await timersOf(a);
    const schedules = el("section", { class: "card" }, el("h2", null, "Schedules"),
        el("p", { class: "muted" }, "systemd timers agent-schedule@" + a.name + "_*.timer. Their runs show up under Activity with origin schedule."));
    if (scheds.length === 0) schedules.append(el("p", { class: "muted" }, a.status ? "No schedules." : "Unknown while the agent is not reachable."));
    for (const s of scheds) {
        const t = timers[s.name] || {};
        const toggle = el("button", { class: "secondary" }, t.enabled ? "Disable" : "Enable");
        toggle.addEventListener("click", () => act(toggle, (t.enabled ? "Disabling " : "Enabling ") + s.name,
            () => systemctl(t.enabled ? "disable" : "enable", "--now", t.unit)));
        schedules.append(el("div", { class: "call" }, el("div", { class: "head" },
            el("strong", null, s.name), el("span", { class: "mono muted" }, s.on_calendar),
            el("span", { class: "muted", style: "margin-left:auto" }, t.enabled ? (t.next ? "next " + when(t.next) : "") : "disabled"),
            toggle), el("div", { class: "muted" }, "“" + s.prompt + "”")));
    }

    // Events (§14): the units whose failure starts an activity. Changed by
    // editing the configuration, which rewrites their drop-ins.
    const evs = (a.status && a.status.events) || [];
    const events = el("section", { class: "card", id: "agent-events" }, el("h2", null, "Events"),
        el("p", { class: "muted" }, "When one of these units fails, systemd starts agent-event@" + a.name +
           "_<event>_<unit>.service, which starts the event's activity: origin event, at most one at a time per unit, " +
           "and none within the event's interval after the last."));
    if (evs.length === 0) events.append(el("p", { class: "muted" }, a.status ? "No events." : "Unknown while the agent is not reachable."));
    for (const e of evs) {
        const every = e.min_interval_seconds || 600;
        events.append(el("div", { class: "call" },
            el("div", { class: "head" }, el("strong", null, e.name),
               el("span", { class: "muted" }, "when " + (e.on_unit_failure || []).join(", ") + " fails"),
               el("span", { class: "muted", style: "margin-left:auto" }, "at most every " + relativeSpan(every) + " per unit")),
            e.playbook ? el("div", null, el("span", { class: "badge" }, "playbook " + e.playbook)) : null,
            e.prompt ? el("div", { class: "muted" }, "“" + e.prompt + "”") : null));
    }

    // Rebuilding the view takes the chat out and puts it back, which resets
    // the scroll position of its messages (and can move the page): keep
    // both, so that a refresh every few seconds does not scroll them away.
    // The kept forms and editors are taken out and put back too, which
    // takes the keyboard focus from the field being typed in: give it back,
    // with the caret where it was.
    const chatLogs = [...box.querySelectorAll(".chat-log")].map(l => [l, l.scrollTop]);
    const pageY = window.scrollY;
    const focused = box.contains(document.activeElement) ? document.activeElement : null;
    const caret = focused && typeof focused.selectionStart === "number" ? [focused.selectionStart, focused.selectionEnd, focused.scrollTop] : null;
    box.replaceChildren(head, mode, bounds, categoriesSection(a, data), schedules, events, playbookSection(a), credentialsSection(a), templateSection(a), configEditor(a), chatSection(a));
    for (const [l, top] of chatLogs) {
        if (l.isConnected) l.scrollTop = top;
    }
    if (focused && focused.isConnected && document.activeElement !== focused) {
        focused.focus({ preventScroll: true });
        if (caret) {
            focused.setSelectionRange(caret[0], caret[1]);
            focused.scrollTop = caret[2];
        }
    }
    if (window.scrollY !== pageY) window.scrollTo(window.scrollX, pageY);
}

tabs.agents = {
    intervalMs: 5000,
    init: initCreate,
    async refresh() {
        await refreshState();
        renderAgentList();
        const fe = document.activeElement;
        if (!(fe && fe.closest(".enablement-edit"))) renderEnablement();
        // Not while someone edits the permissions or types a message.
        const f = document.activeElement;
        const editing = f && (f.id === "permissions-yaml" || f.id === "owners-input" || f.closest(".chat") || f.closest(".confirm-delete") || f.closest(".config-edit") ||
            f.closest(".credentials-edit") || f.closest(".playbook-run") || f.closest("#agent-template"));
        if (!editing) await renderAgentDetail();
    },
};

/* categoriesSection: the agent's task categories (§6a), their mode and
 * envelope, their readiness (agent-admin readiness, which reads the
 * journal: through the root bridge), and promote and demote. */
function windowsText(c) {
    return ((c && c.envelope && c.envelope.windows) || []).map(w =>
        (w.days ? w.days.join(",") : "every day") + " " + w.from + "–" + w.to + " " + (w.tz || "UTC")).join("; ");
}

/* The envelope's rate (O12): per tool; over it a call asks (the
 * gateway's approval_over_rate). */
function rateText(c) {
    const r = c && c.envelope && c.envelope.rate;
    return r ? "at most " + r.calls + " calls per tool per " + ({ "1s": "second", "1m": "minute", "1h": "hour" }[r.per] || r.per) : "";
}

function pctText(x) { return (x * 100).toFixed(1) + " %"; }

/* diagnosticsEvidence shows the agent's diagnostics history over the
 * readiness window (§6b): its reports, how much of their evidence was
 * verified, and the feedback on them; evidence, not a threshold. */
function diagnosticsEvidence(box, r) {
    const d = r.diagnostics;
    const old = box.querySelector("#diagnostics-evidence");
    if (old) old.remove();
    if (!d || !d.reports) return;
    const days = (r.thresholds && r.thresholds.window_days) || 30;
    const verdicts = d.confirmed + d.rejected;
    box.append(el("div", { id: "diagnostics-evidence" },
        el("h3", null, "Diagnostics evidence"),
        el("p", { class: "muted" }, "What this agent's diagnosis reports showed in the last " + days + " days, and what people " +
           "said of them (agent-admin feedback). " + (r.diagnostics_apply
            ? "This host started diagnostics-only, so it counts in every category's readiness above: at least " +
              ((r.thresholds && r.thresholds.diagnostics_feedback) || 0) + " reports with feedback, and at least " +
              pctText((r.thresholds && r.thresholds.diagnostics_confirmed_rate) || 0) + " of the decided hypotheses confirmed."
            : "This host did not start diagnostics-only: it is shown, not counted in the readiness above.")),
        el("ul", null,
            el("li", null, d.reports + (d.reports === 1 ? " report, " : " reports, ") + d.hypotheses + " hypotheses; " +
               d.verified_quotes + " of " + d.quotes + " quotes verified by Custos"),
            el("li", null, d.with_feedback + " of " + d.reports + " reports with feedback: " + d.confirmed + " hypotheses confirmed, " +
               d.rejected + " rejected, " + d.unsure + " unsure" + (verdicts ? " (" + pctText(d.confirmed_rate) + " confirmed)" : "")),
            el("li", null, "plans: " + d.plans_useful + " useful, " + d.plans_not_useful + " not useful, " + d.plans_executed + " carried out"))));
}

function categoriesSection(a, data) {
    const box = el("section", { class: "card", id: "categories-card" }, el("h2", null, "Task categories"),
        el("p", { class: "muted" }, "A category is promoted on its own: within its windows a bounded category's changes run without an " +
           "approval; outside them, and in every other category, they need one (CONTRACT.md §6a). The demotion evaluator " +
           "(custos-demote.timer) demotes it again on its override rate or its rollback conditions."));
    if (!data) return box;
    const states = data.categories || {};
    const names = [...new Set([...(data.permissions || []).map(p => p.category).filter(Boolean), ...Object.keys(states)])].sort();
    if (names.length === 0) {
        box.append(el("p", { class: "muted" }, "No permission names a category: add category: NAME to a permission to promote it later."));
        return box;
    }
    const table = el("table", { id: "categories" }, el("thead", null, el("tr", null,
        el("th", null, "Task category"), el("th", null, "Mode"), el("th", null, "Windows"), el("th", null, "Readiness"),
        el("th", null, "Overrides"), el("th", null, "Incidents"), el("th", null, "Proposals"), el("th", null, ""))));
    const tbody = el("tbody");
    // Kept across the tab's refreshes (promoteForm): the form, the
    // envelope being written, and the reason Demote and the reviews use.
    const k = promoteForm(a, names, states);
    const { reason, which, form } = k;
    const cells = {};
    for (const n of names) {
        const c = states[n] || { mode: "supervised" };
        const bounded = c.mode === "bounded";
        const usable = categoryUsable(n);
        const demote = el("button", { class: "secondary" }, "Demote");
        demote.hidden = !bounded;
        demote.addEventListener("click", () => act(demote, "Demoting " + n,
            () => admin(["demote", a.name, n, "--reason", reason.value.trim() || "demoted in Cockpit"]).then(k.used)));
        const promote = el("button", { class: "secondary" }, bounded ? "Change envelope…" : "Promote…");
        // A disabled category is denied whatever its mode (§6b), and
        // diagnostics needs no promotion: it only reads.
        promote.hidden = !usable || n === "diagnostics";
        promote.addEventListener("click", () => {
            which.value = n;
            k.fill();
            form.open = true;
            form.scrollIntoView({ block: "nearest" });
        });
        cells[n] = { ready: el("td", { class: "muted" }, "…"), over: el("td", { class: "muted" }), inc: el("td", { class: "muted" }), props: el("td", { class: "muted" }) };
        tbody.append(el("tr", { id: "category-" + n }, el("td", { class: "mono" }, n),
            el("td", null, usable
                ? el("span", { class: "badge " + (bounded ? "badge-ok" : "badge-waiting") }, bounded ? "bounded (promoted)" : c.mode)
                : el("span", { class: "badge badge-bad", title: "Not enabled on this host: the gateway denies its calls (agent-admin enable)" },
                     "disabled on this host"),
                !bounded && /^custos-demote /.test(c.demoted_by || "")
                    ? el("div", { class: "muted demoted-auto" }, "demoted automatically " + when(c.demoted_by.slice("custos-demote ".length)) +
                         " (see the Approvals tab)")
                    : null),
            el("td", { class: "muted" }, bounded ? [windowsText(c) || "always", rateText(c)].filter(Boolean).join("; ") : "–"),
            cells[n].ready, cells[n].over, cells[n].inc, cells[n].props,
            el("td", null, el("span", { class: "actions" }, promote, demote)),
        ));
    }
    table.append(tbody);
    const reviews = el("div", { id: "category-reviews" });
    box.append(table, el("div", { class: "toolbar" }, reason), reviews, form);
    // The after-the-fact review of promoted categories' autonomous actions
    // (agent-admin review, through the root bridge).
    if (names.some(n => states[n] && states[n].promoted_by)) {
        admin(["review", a.name]).then(out => {
            const q = JSON.parse(out);
            const items = [];
            for (const [cat, cq] of Object.entries(q.categories || {}).sort()) {
                for (const x of cq.pending || []) {
                    const verdict = (v, label, cls) => {
                        const b = el("button", { class: cls }, label);
                        b.addEventListener("click", () => {
                            const args = ["review", a.name, cat, x.activity, x.call, v];
                            if (reason.value.trim()) args.push("--reason", reason.value.trim());
                            act(b, "Reviewing " + x.tool, () => admin(args).then(k.used));
                        });
                        return b;
                    };
                    // An action the gateway recorded without an activity of
                    // the agent's (no progress token) is reviewed on the gateway.
                    const id = x.activity ? "review-" + x.activity + "-" + x.call : "review-decision-" + x.decision_id;
                    items.push(el("div", { class: "call", id },
                        el("div", { class: "head" }, el("strong", { class: "mono" }, x.tool),
                            el("span", { class: "muted" }, cat + " · " + (x.activity || "no activity named")),
                            el("span", { class: "muted", style: "margin-left:auto" }, when(x.at) + (x.outcome === "error" ? " · failed" : "")),
                            ...(x.activity
                                ? [verdict("ok", "Fine", "secondary"), verdict("incident", "Should not have run", "danger")]
                                : [el("button", { class: "link", onclick: () => cockpit.location.go(["approvals"]) }, "Review on the Approvals tab")]))));
                }
            }
            const counts = Object.entries(q.categories || {}).map(([cat, cq]) => cat + ": " + cq.reviewed + " of " + cq.actions + " reviewed this week");
            reviews.replaceChildren(el("h3", null, "To review"),
                el("p", { class: "muted" }, "What promoted categories did without an approval. \"Should not have run\" counts as an override " +
                   "and demotes the category at once; it needs a reason. The agent's approvers can review them on the Approvals tab too " +
                   "(mcp-gateway's review items). " + counts.join("; ")),
                ...(items.length ? items : [el("p", { class: "muted" }, "Nothing to review.")]));
        }).catch(ex => reviews.replaceChildren(el("p", { class: "muted" }, "Review: " + ex.message)));
    }
    // Readiness, as the person with administrative access (it reads the
    // agent's and the gateway's journal).
    admin(["readiness", a.name]).then(out => {
        const r = JSON.parse(out);
        for (const n of names) {
            const m = r.categories && r.categories[n];
            if (!m) continue;
            const th = r.thresholds || {};
            cells[n].ready.replaceChildren(el("span", { class: "badge " + (m.ready ? "badge-ok" : "badge-waiting") }, m.ready ? "ready" : "not ready"),
                m.ready ? "" : el("div", { class: "muted" }, (m.unmet || []).join("; ")));
            cells[n].over.textContent = pctText(m.override_rate) + " (" + m.overrides + ") of max " + pctText(th.override_rate || 0);
            cells[n].inc.textContent = pctText(m.incident_rate) + " (" + m.incidents + ") of max " + pctText(th.incident_rate || 0);
            cells[n].props.textContent = m.proposals + " in " + m.observed_days.toFixed(1) + " days · " + m.tools + " tools, " + m.distinct_calls + " distinct calls";
        }
        k.readiness = r.categories || {};
        k.showReady();
        diagnosticsEvidence(box, r);
    }).catch(ex => {
        for (const n of names) cells[n].ready.textContent = "unknown";
        box.append(el("p", { id: "readiness-note", class: "muted" }, "Readiness: " + ex.message));
    });
    return box;
}

/* ownersEditor: who may approve the agent's calls besides the servers'
 * approvers (CONTRACT.md §7, O1): the gateway's approver forms, set with
 * agent-admin owners through the root bridge. */
function ownersEditor(a, data) {
    const owners = data.owners || [];
    const ed = keptEditor("owners:" + a.name, owners.join(" "), () => {
        const input = el("input", { id: "owners-input", class: "mono", "aria-label": "Owners", placeholder: "user:alice group:web-team" });
        const save = el("button", { class: "secondary" }, "Save owners");
        const k = { node: el("div", { class: "actions" }, input, save), field: input };
        save.addEventListener("click", () => act(save, "Setting the owners of " + a.name,
            () => admin(["owners", a.name, ...input.value.split(/\s+/).filter(Boolean)]).then(() => { k.dirty = false })
                .finally(() => { save.disabled = false })));
        return k;
    });
    return el("div", { id: "owners" },
        el("h3", null, "Owners"),
        el("p", { class: "muted" }, owners.length
            ? "Besides the servers' approvers, " + owners.join(", ") + " may approve this agent's calls, and are mailed about them."
            : "No owners: the servers' approvers decide this agent's calls. Owners (user:NAME, group:NAME, role:NAME, remote-user:SUB, " +
              "remote-group:NAME) may approve them too."),
        ed.node);
}

/* promoteForm is an agent's form to promote a category, and the reason
 * Demote and the reviews record, kept across the tab's refreshes (which
 * rebuild the agent's view every few seconds) while the form is open or
 * anything was typed: a refresh would otherwise close the form and lose
 * the envelope being written. A new one, with the current categories and
 * envelopes, once it is closed and untouched, or after an action used it
 * (used). */
const promoteForms = new Map();

function promoteForm(a, names, states) {
    const key = names.join(",");
    const old = promoteForms.get(a.name);
    if (old && old.key === key && (old.form.open || old.dirty || old.reason.value.trim() || old.despite.checked)) {
        old.states = states;
        return old;
    }
    const k = { key, states, dirty: false, readiness: {} };
    k.reason = el("input", { id: "category-reason", type: "text", placeholder: "Reason (recorded)", "aria-label": "Reason" });
    // The envelope as a form (#163); "Edit as YAML" keeps the text.
    k.ed = envelopeEditor(() => { k.dirty = true });
    k.env = k.ed.text;
    k.despite = el("input", { id: "category-despite", type: "checkbox" });
    k.which = el("select", { id: "category-select", "aria-label": "Category" },
        ...names.filter(n => categoryUsable(n) && n !== "diagnostics").map(n => el("option", { value: n }, n)));
    // The chosen category's readiness, from the tab's agent-admin
    // readiness: why "despite its readiness" would be needed.
    k.ready = el("p", { id: "promote-readiness", class: "muted" });
    k.showReady = () => {
        const m = k.readiness[k.which.value];
        k.ready.replaceChildren(...(!m ? ["Readiness: not read yet."]
            : m.ready ? [el("span", { class: "badge badge-ok" }, "ready"), " Its record meets the readiness criteria."]
            : [el("span", { class: "badge badge-waiting" }, "not ready"), " " + (m.unmet || []).join("; ") +
               ". Promote it anyway only with \"despite its readiness\" and a reason."]));
    };
    k.fill = () => {
        const c = k.states[k.which.value];
        k.ed.set(c && c.envelope ? c.envelope : null);
        k.dirty = false;
        k.showReady();
    };
    k.which.addEventListener("change", k.fill);
    k.fill();
    // After an action that used them: the next render starts afresh.
    k.used = out => {
        k.form.open = false;
        k.dirty = false;
        k.reason.value = "";
        k.despite.checked = false;
        return out;
    };
    const go = el("button", null, "Promote");
    go.addEventListener("click", () => {
        const v = k.ed.value();
        // What the form can tell is wrong, before agent-admin does.
        if (v.errors.length) {
            k.ed.node.querySelector("#envelope-errors").scrollIntoView({ block: "nearest" });
            return;
        }
        const args = ["promote", a.name, k.which.value, "--envelope", "-"];
        if (k.despite.checked) args.push("--despite-readiness");
        if (k.reason.value.trim()) args.push("--reason", k.reason.value.trim());
        // Kept, the button outlives the action, which disables it.
        act(go, "Promoting " + k.which.value, () => admin(args, v.text).then(k.used)).finally(() => { go.disabled = false });
    });
    k.form = el("details", { id: "promote-form" }, el("summary", null, "Promote a category"),
        el("p", { class: "muted" }, "Within its envelope a bounded category's changes run without an approval; outside it they need one. " +
           "agent-admin checks the envelope and refuses a category that is not ready, unless you promote it despite its readiness, with a reason."),
        el("div", { class: "toolbar" }, el("label", null, "Category ", k.which)), k.ready, k.ed.node,
        el("label", null, k.despite, " Promote despite its readiness (needs a reason)"),
        el("div", { class: "actions" }, go));
    promoteForms.set(a.name, k);
    return k;
}

/* keptEditor is an editor kept across the tab's refreshes, which rebuild
 * the agent's view every few seconds: while it is open (a <details>) or
 * holds changes not yet saved, a refresh reuses it as it is. Otherwise each
 * render gets a new one with the current value: one element cannot be in
 * two renders, and with slow commands two may overlap. make returns
 * {node, field}; a save that went through sets dirty to false. */
const keptEditors = new Map();

function keptEditor(key, current, make) {
    const old = keptEditors.get(key);
    if (old && (old.dirty || old.node.open)) return old;
    const k = make();
    k.dirty = false;
    k.field.addEventListener("input", () => { k.dirty = true });
    k.field.value = current;
    keptEditors.set(key, k);
    return k;
}

/* The host's enablement state (CONTRACT.md §6b): which task categories it
 * may use, from agent-admin show; Enable… and Disable… run agent-admin
 * enable and disable through the root bridge. A host installed with the
 * full capability has no state: every category is enabled, and Disable…
 * is the one change offered. */
let catalog = null; // agent-admin categories, once read

async function loadCatalog() {
    if (catalog) return catalog;
    try {
        catalog = JSON.parse(await cockpit.spawn(["agent-admin", "categories"], { err: "message" }));
    } catch {
        catalog = {};
    }
    return catalog;
}

let enableForm = null; // kept across the tab's refreshes
let disableForm = null;

function renderEnablement() {
    const box = document.getElementById("enablement");
    const e = state.posture && state.posture.enablement;
    box.hidden = !state.posture;
    if (!state.posture) return;
    loadCatalog().then(cat => {
        const names = Object.keys(cat).sort();
        const disabled = names.filter(n => !categoryUsable(n));
        const enabled = names.filter(n => categoryUsable(n) && n !== "diagnostics");
        const what = !e
            ? "Every task category is enabled on this host (installed with the full capability). Disable… turns categories off again, or returns the host to diagnostics-only."
            : e.state === "diagnostics-only"
            ? "This host is diagnostics-only: only the diagnostics category is usable. The other task categories are installed but disabled; the gateway denies their calls and does not show their tools."
            : "These task categories are enabled. The others are disabled: the gateway denies their calls.";
        const list = el("ul", { class: "enablement-list" }, names.map(n => el("li", { id: "enabled-" + n },
            el("span", { class: "badge " + (categoryUsable(n) ? "badge-ok" : "badge-bad") }, categoryUsable(n) ? "enabled" : "disabled"),
            " ", el("span", { class: "mono" }, n), el("span", { class: "muted" }, " " + ((cat[n] && cat[n].title) || "")))));
        const open = el("button", { class: "secondary" }, "Enable…");
        open.hidden = disabled.length === 0;
        if (!enableForm || enableForm.dataset.for !== disabled.join(",")) enableForm = enablementForm(disabled);
        open.addEventListener("click", () => { enableForm.hidden = false; disableForm.hidden = true });
        const close = el("button", { class: "secondary", id: "disable-open" }, "Disable…");
        close.hidden = enabled.length === 0;
        if (!disableForm || disableForm.dataset.for !== enabled.join(",")) disableForm = disablementForm(enabled);
        close.addEventListener("click", () => { disableForm.hidden = false; enableForm.hidden = true });
        box.replaceChildren(...nodes([
            el("div", { class: "toolbar" }, el("h2", null, "Task categories on this host"), el("span", { class: "actions" }, open, close)),
            el("p", null, what),
            e && e.tampered ? el("p", { class: "error-text", id: "enablement-tampered" },
                "The state was changed outside agent-admin: " + e.tampered + "." +
                (e.sealed ? "" : " The gateway enforces what the file says now.")) : null,
            e ? el("p", { class: "muted" }, "Changed by " + (e.changed_by || "?") + " (record " + (e.record || "none") + ")." +
               (e.sealed ? " Sealed: the gateway counts this state only with agent-admin's signature; edited by hand, the host is diagnostics-only." : "") +
               (state.posture.agents_sealed ? " The seal covers each agent's gateway data and the agents' token issuers too: changed by hand, the agent is denied everything." : "")) : null,
            state.posture.seal_tampered ? el("p", { class: "error-text", id: "seal-tampered" }, state.posture.seal_tampered + ".") : null,
            state.posture.issuers_tampered ? el("p", { class: "error-text", id: "issuers-tampered" },
                "The agents' token issuers were changed outside agent-admin: " + state.posture.issuers_tampered + ".") : null,
            list, enableForm, disableForm]));
    });
}

/* disablementForm turns enabled categories off again (agent-admin disable,
 * #110), or returns the host to diagnostics-only (disable --all).
 * agent-admin refuses while an agent has permissions in a category or has
 * it promoted, and says which: shown as it says it. */
function disablementForm(enabled) {
    const checks = enabled.map(n => ({ n, c: el("input", { type: "checkbox", value: n }) }));
    const back = el("input", { type: "checkbox", name: "diagnostics-only" });
    const note = el("span", { class: "muted" });
    const go = el("button", { class: "danger" }, "Disable");
    const cancel = el("button", { type: "button", class: "link" }, "Cancel");
    const form = el("form", { class: "confirm enablement-edit", id: "disable-form", hidden: true },
        el("h2", null, "Disable task categories?"),
        el("p", null, "The gateway then denies the categories' calls and hides their tools. First take their permissions from " +
           "the agents and demote them where promoted: agent-admin refuses while either remains, and says which. " +
           "Enable… turns them on again; their readiness history stays. The change is recorded with your name."),
        checks.map(({ n, c }) => el("label", null, c, " " + n)),
        el("label", null, back, " Return to diagnostics-only: every category but diagnostics, and permissions without a category"),
        el("div", { class: "actions" }, go, cancel, note));
    form.dataset.for = enabled.join(",");
    back.addEventListener("change", () => checks.forEach(x => { x.c.disabled = back.checked }));
    cancel.addEventListener("click", () => { form.hidden = true; note.textContent = "" });
    form.addEventListener("submit", async ev => {
        ev.preventDefault();
        const picked = checks.filter(x => x.c.checked).map(x => x.n);
        if (!back.checked && picked.length === 0) {
            note.className = "error-text";
            note.textContent = "Pick a category, or return to diagnostics-only.";
            return;
        }
        go.disabled = true;
        note.className = "muted";
        note.textContent = "Disabling…";
        try {
            await admin(back.checked ? ["disable", "--all"] : ["disable", ...picked]);
            disableForm = null;
            showError(null);
        } catch (ex) {
            note.className = "error-text";
            note.textContent = ex.message;
            go.disabled = false;
            return;
        }
        tabs.agents.refresh();
    });
    return form;
}

function enablementForm(disabled) {
    const checks = disabled.map(n => ({ n, c: el("input", { type: "checkbox", value: n }) }));
    const note = el("span", { class: "muted" });
    const go = el("button", { class: "danger" }, "Enable");
    const cancel = el("button", { type: "button", class: "link" }, "Cancel");
    const form = el("form", { class: "confirm enablement-edit", hidden: true },
        el("h2", null, "Enable task categories?"),
        el("p", null, "An enabled category starts supervised: each of its changes waits for an approval until you promote it. " +
           "Disable… turns it off again. The change is recorded with your name."),
        checks.map(({ n, c }) => el("label", null, c, " " + n)),
        el("div", { class: "actions" }, go, cancel, note));
    form.dataset.for = disabled.join(",");
    cancel.addEventListener("click", () => { form.hidden = true; note.textContent = "" });
    form.addEventListener("submit", async ev => {
        ev.preventDefault();
        const picked = checks.filter(x => x.c.checked).map(x => x.n);
        if (picked.length === 0) {
            note.className = "error-text";
            note.textContent = "Pick a category.";
            return;
        }
        go.disabled = true;
        note.className = "muted";
        note.textContent = "Enabling…";
        try {
            await admin(picked.length === disabled.length ? ["enable", "--all"] : ["enable", ...picked]);
            enableForm = null;
            showError(null);
        } catch (ex) {
            note.className = "error-text";
            note.textContent = ex.message;
            go.disabled = false;
            return;
        }
        tabs.agents.refresh();
    });
    return form;
}
