/* The envelope of a task category's promotion (CONTRACT.md §6a), as a
 * form (#163): windows (days, from, to, time zone), a rate and rollback
 * conditions, built into the same envelope agent-admin promote reads,
 * which it checks again. "Edit as YAML" keeps the text for what the form
 * does not show; an envelope with such parts opens there. */

const DAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
const DAY_NAMES = { mon: "Monday", tue: "Tuesday", wed: "Wednesday", thu: "Thursday", fri: "Friday", sat: "Saturday", sun: "Sunday" };
const RATE_PERS = [["1s", "second"], ["1m", "minute"], ["1h", "hour"]];
const ROLLBACKS = [
    ["tool_error", "a tool call of the category fails"],
    ["limit_exceeded", "the rate is exceeded"],
    ["health_check_failed", "a health check fails"],
];

/* The host's time zone (timedatectl), once: windows are in a zone, and
 * the host's is the likely one. UTC when it cannot be read. */
let hostZone;
function hostTimeZone() {
    if (!hostZone) {
        hostZone = native(cockpit.spawn(["timedatectl", "show", "-p", "Timezone", "--value"], { err: "ignore" }))
            .then(out => out.trim() || "UTC").catch(() => "UTC");
    }
    return hostZone;
}

/* The zones offered: the host's, UTC, and the browser's list. */
function zoneNames(host, have) {
    let all = [];
    try { all = Intl.supportedValuesOf("timeZone"); } catch (e) { all = []; }
    return [...new Set([host, "UTC", ...(have ? [have] : []), ...all].filter(Boolean))];
}

/* What the form shows: an envelope with nothing else, which it can
 * build again without losing a part. */
function formShows(env) {
    if (!env || typeof env !== "object" || Array.isArray(env)) return false;
    if (Object.keys(env).some(k => !["windows", "rate", "rollback"].includes(k))) return false;
    const ws = env.windows || [];
    if (!Array.isArray(ws) || ws.some(w => !w || typeof w !== "object" ||
        Object.keys(w).some(k => !["days", "from", "to", "tz"].includes(k)) ||
        (w.days && (!Array.isArray(w.days) || w.days.some(d => !DAYS.includes(d)))))) return false;
    const r = env.rate;
    if (r && (typeof r !== "object" || Object.keys(r).some(k => !["calls", "per"].includes(k)) ||
        !RATE_PERS.some(([p]) => p === r.per))) return false;
    const rb = env.rollback;
    if (rb && (typeof rb !== "object" || Object.keys(rb).some(k => !["on", "health_check"].includes(k)) ||
        !Array.isArray(rb.on || []) || (rb.on || []).some(c => !ROLLBACKS.some(([x]) => x === c)))) return false;
    return true;
}

/* envelopeSummary says an envelope in words. */
function envelopeSummary(env) {
    const parts = [];
    const ws = (env && env.windows) || [];
    const dayText = days => {
        if (!days || days.length === 7) return "every day";
        const s = DAYS.filter(d => days.includes(d));
        if (s.join() === "mon,tue,wed,thu,fri") return "weekdays";
        if (s.join() === "sat,sun") return "weekends";
        return s.map(d => DAY_NAMES[d].slice(0, 3)).join(", ");
    };
    parts.push(ws.length
        ? ws.map(w => dayText(w.days) + " " + w.from + "–" + w.to + " " + (w.tz || "UTC")).join("; ")
        : "at any time (no window)");
    const r = env && env.rate;
    if (r) parts.push("at most " + r.calls + " calls per tool per " + ((RATE_PERS.find(([p]) => p === r.per) || [])[1] || r.per));
    const rb = env && env.rollback;
    if (rb && (rb.on || []).length) parts.push("demoted at once when " + rb.on.map(c => (ROLLBACKS.find(([x]) => x === c) || [c, c])[1]).join(", or "));
    return parts.join("; ");
}

/* envelopeEditor is the form. set(env) fills it (an envelope the form
 * cannot show opens as YAML), value() is {text, errors}: the envelope to
 * send and what is wrong with it. changed() is called on every input. */
function envelopeEditor(changed) {
    const ed = { yaml: false, zone: "UTC" };
    hostTimeZone().then(z => { ed.zone = z; });
    const note = el("p", { class: "muted", id: "envelope-always" });
    const rows = el("div", { id: "envelope-windows", class: "envelope-windows" });
    const addWindow = el("button", { type: "button", class: "secondary", id: "envelope-add-window" }, "Add window");

    const windowRow = w => {
        const days = new Set(w.days && w.days.length ? w.days : DAYS);
        const boxes = DAYS.map(d => {
            const c = el("input", { type: "checkbox", value: d, "aria-label": DAY_NAMES[d] });
            c.checked = days.has(d);
            return { d, c, label: el("label", { class: "day", title: DAY_NAMES[d] }, c, DAY_NAMES[d].slice(0, 3)) };
        });
        const pick = which => () => { for (const b of boxes) b.c.checked = which.includes(b.d); update(); };
        const shortcut = (label, which) => el("button", { type: "button", class: "link", onclick: pick(which) }, label);
        const from = el("input", { type: "time", class: "from", value: w.from || "02:00", "aria-label": "From" });
        const to = el("input", { type: "time", class: "to", value: w.to || "04:00", "aria-label": "To" });
        const tz = el("select", { class: "tz", "aria-label": "Time zone" });
        const fillZones = () => {
            const want = tz.value || w.tz || ed.zone;
            tz.replaceChildren(...zoneNames(ed.zone, want).map(z => el("option", { value: z }, z)));
            tz.value = want;
        };
        fillZones();
        if (!w.tz) hostTimeZone().then(() => { if (!tz.dataset.touched) { tz.value = ed.zone; fillZones(); } });
        tz.addEventListener("change", () => { tz.dataset.touched = "1" });
        const remove = el("button", { type: "button", class: "link", "aria-label": "Remove window" }, "Remove");
        const row = el("div", { class: "envelope-window" },
            el("span", { class: "days" }, ...boxes.map(b => b.label)),
            el("span", { class: "shortcuts" }, shortcut("every day", DAYS), shortcut("weekdays", DAYS.slice(0, 5)), shortcut("weekend", ["sat", "sun"])),
            el("label", null, "from ", from), el("label", null, " to ", to), el("label", null, " ", tz), remove);
        remove.addEventListener("click", () => { row.remove(); update(); });
        row.read = () => {
            const ds = boxes.filter(b => b.c.checked).map(b => b.d);
            const out = {};
            if (ds.length !== 7) out.days = ds;
            out.from = from.value;
            out.to = to.value;
            out.tz = tz.value;
            return out;
        };
        return row;
    };
    addWindow.addEventListener("click", () => {
        rows.append(windowRow({ days: ["sat"], from: "02:00", to: "04:00" }));
        update();
    });

    const rateOn = el("input", { type: "checkbox", id: "envelope-rate" });
    const calls = el("input", { type: "number", min: "1", step: "1", value: "20", id: "envelope-rate-calls", "aria-label": "Calls" });
    const per = el("select", { id: "envelope-rate-per", "aria-label": "Per" }, ...RATE_PERS.map(([p, w]) => el("option", { value: p }, w)));
    per.value = "1h";
    const rollbacks = ROLLBACKS.map(([c, text]) => ({ c, box: el("input", { type: "checkbox", value: c, id: "envelope-rollback-" + c }), text }));
    const health = el("input", { type: "text", class: "mono", id: "envelope-health-check", placeholder: "systemctl is-active nginx",
        "aria-label": "Health check command" });
    const healthRow = el("div", { class: "envelope-health" }, el("label", null, "Health check, run as root by sh -c: ", health));

    const errors = el("ul", { class: "error-text", id: "envelope-errors" });
    const summary = el("p", { id: "envelope-summary" });
    const preview = el("pre", { class: "mono", id: "envelope-preview" });
    const text = el("textarea", { id: "category-envelope", rows: "6", spellcheck: "false", "aria-label": "Envelope (YAML)" });
    const toggle = el("button", { type: "button", class: "link", id: "envelope-yaml-toggle" }, "Edit as YAML");
    const formPart = el("div", { id: "envelope-form" },
        el("h4", null, "When it may act without asking"), note, rows, el("div", { class: "actions" }, addWindow),
        el("h4", null, "Rate"),
        el("div", { class: "envelope-rate" }, el("label", null, rateOn, " At most"), " ", calls, " calls per tool per ", per,
           el("span", { class: "muted" }, " (beyond it, each call waits for an approval; reaching it three times in a day demotes the category)")),
        el("h4", null, "Demote at once when"),
        ...rollbacks.map(r => el("label", { class: "rollback" }, r.box, " " + r.text)), healthRow,
        errors, summary, el("details", null, el("summary", null, "The envelope sent"), preview));
    const yamlPart = el("div", { id: "envelope-yaml", hidden: true },
        el("p", { class: "muted" }, "The envelope as YAML or JSON: windows (days mon…sun, from and to as HH:MM, tz), rate (calls, per: 1s, 1m " +
           "or 1h), rollback (on: tool_error, limit_exceeded, health_check_failed; health_check). agent-admin checks it."), text);

    // The envelope the form says, and what is wrong with it.
    const build = () => {
        const env = {};
        const errs = [];
        const ws = [...rows.children].map(r => r.read());
        ws.forEach((w, i) => {
            const n = ws.length > 1 ? "Window " + (i + 1) + ": " : "The window: ";
            if (w.days && w.days.length === 0) errs.push(n + "choose at least one day");
            if (!w.from || !w.to) errs.push(n + "give from and to");
            else if (w.from >= w.to) errs.push(n + "from must be before to (a window does not cross midnight: use two)");
        });
        if (ws.length) env.windows = ws;
        if (rateOn.checked) {
            const n = Number(calls.value);
            if (!Number.isInteger(n) || n < 1) errs.push("The rate: a whole number of calls, at least 1");
            env.rate = { calls: n, per: per.value };
        }
        const on = rollbacks.filter(r => r.box.checked).map(r => r.c);
        if (on.length) {
            env.rollback = { on };
            if (on.includes("health_check_failed")) {
                if (!health.value.trim()) errs.push("The health check: give the command");
                env.rollback.health_check = health.value.trim();
            }
        }
        return { env, errs };
    };
    const update = () => {
        if (ed.yaml) return changed && changed();
        const { env, errs } = build();
        note.textContent = rows.children.length ? "" : "No window: the category acts without asking at any time. Add a window to limit when.";
        healthRow.hidden = !rollbacks.find(r => r.c === "health_check_failed").box.checked;
        calls.disabled = per.disabled = !rateOn.checked;
        errors.replaceChildren(...errs.map(e => el("li", null, e)));
        summary.textContent = "In words: " + envelopeSummary(env) + ".";
        preview.textContent = JSON.stringify(env, null, 2);
        if (changed) changed();
    };
    formPart.addEventListener("input", update);
    formPart.addEventListener("change", update);
    text.addEventListener("input", () => changed && changed());

    const showYAML = on => {
        ed.yaml = on;
        formPart.hidden = on;
        yamlPart.hidden = !on;
        toggle.textContent = on ? "Edit with the form" : "Edit as YAML";
    };
    toggle.addEventListener("click", () => {
        if (!ed.yaml) {
            text.value = JSON.stringify(build().env, null, 2) + "\n";
            showYAML(true);
            return;
        }
        // Back to the form: only JSON can be read here, and only what the
        // form shows; otherwise the text stays.
        let env;
        try { env = JSON.parse(text.value); } catch (e) { env = null; }
        if (!formShows(env)) {
            errorsYAML.textContent = "The form shows windows, a rate and rollback, from JSON: this text stays as it is.";
            return;
        }
        errorsYAML.textContent = "";
        ed.set(env);
    });
    const errorsYAML = el("p", { class: "error-text", id: "envelope-yaml-note" });
    yamlPart.append(errorsYAML);

    ed.set = env => {
        if (env && !formShows(env)) {
            text.value = JSON.stringify(env, null, 2) + "\n";
            showYAML(true);
            return;
        }
        env = env || { windows: [{ days: ["sat"], from: "02:00", to: "04:00" }] };
        rows.replaceChildren(...(env.windows || []).map(windowRow));
        rateOn.checked = !!env.rate;
        if (env.rate) { calls.value = String(env.rate.calls); per.value = env.rate.per; }
        const on = (env.rollback && env.rollback.on) || [];
        for (const r of rollbacks) r.box.checked = on.includes(r.c);
        health.value = (env.rollback && env.rollback.health_check) || "";
        showYAML(false);
        update();
    };
    ed.value = () => ed.yaml ? { text: text.value, errors: [] } : (b => ({ text: JSON.stringify(b.env, null, 2) + "\n", errors: b.errs }))(build());
    ed.node = el("div", { id: "envelope-editor" }, formPart, yamlPart, el("div", { class: "actions" }, toggle));
    ed.text = text;
    return ed;
}
