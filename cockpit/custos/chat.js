/* Chat with an agent (CONTRACT.md §11), in the agent's view of the Agents
 * tab: the person's own thread, over AG-UI (POST /agui on the agent's
 * socket) through the person's bridge, never as root. The agent knows the
 * person by SO_PEERCRED; the run is an activity like any other (origin
 * chat), and its approvals still go through the gateway: the page only
 * shows that one waits and links to the Approvals tab.
 *
 * The thread lives as long as the page (one per agent); the host keeps
 * its history, so only the new message is sent.
 */
"use strict";

const chats = new Map(); // agent name -> { thread, node, busy }

function newId(prefix) {
    const b = new Uint8Array(6);
    crypto.getRandomValues(b);
    return prefix + "-" + [...b].map(x => x.toString(16).padStart(2, "0")).join("");
}

/* sse calls onEvent for each AG-UI event in the response's chunks (one
 * event per data: line, §11). */
function sseParser(onEvent) {
    let buf = "";
    return chunk => {
        buf += chunk;
        let i;
        while ((i = buf.indexOf("\n")) >= 0) {
            const line = buf.slice(0, i).replace(/\r$/, "");
            buf = buf.slice(i + 1);
            if (!line.startsWith("data:")) continue;
            try {
                onEvent(JSON.parse(line.slice(5).trim()));
            } catch {
                // not an event
            }
        }
    };
}

function chatOf(a) {
    if (chats.has(a.name)) return chats.get(a.name);
    const c = { thread: newId("t-" + (state.user || "me")), busy: false };
    const log = el("div", { class: "chat-log", "aria-live": "polite" });
    const input = el("input", { class: "chat-input", placeholder: "Ask " + a.name + "…", "aria-label": "Message to " + a.name });
    const send = el("button", { type: "submit" }, "Send");
    const status = el("span", { class: "muted mono" });
    const form = el("form", { class: "chat-form" }, input, send);
    form.addEventListener("submit", ev => {
        ev.preventDefault();
        const text = input.value.trim();
        if (!text || c.busy) return;
        input.value = "";
        run(a.name, c, text);
    });
    c.log = log;
    c.status = status;
    c.send = send;
    c.node = el("section", { class: "card chat", id: "chat-" + a.name },
        el("div", { class: "toolbar" }, el("h2", null, "Chat"), el("span", { class: "actions" }, status)),
        el("p", { class: "muted" }, "Your own thread with this agent, live over AG-UI. It is an activity like any " +
           "other; approvals still go through the gateway."),
        log, form);
    status.textContent = "thread " + c.thread;
    chats.set(a.name, c);
    return c;
}

function bubble(c, cls, ...children) {
    const b = el("div", { class: "msg " + cls }, ...children);
    c.log.append(b);
    c.log.scrollTop = c.log.scrollHeight;
    return b;
}

function run(name, c, text) {
    c.busy = true;
    c.send.disabled = true;
    bubble(c, "msg-user", text);
    const texts = new Map(); // messageId -> its bubble
    const tools = new Map(); // toolCallId -> its chip
    const onEvent = e => {
        switch (e.type) {
        case "TEXT_MESSAGE_START":
            texts.set(e.messageId, bubble(c, "msg-agent"));
            break;
        case "TEXT_MESSAGE_CONTENT": {
            const b = texts.get(e.messageId) || bubble(c, "msg-agent");
            texts.set(e.messageId, b);
            b.append(e.delta || "");
            break;
        }
        case "TOOL_CALL_START":
            tools.set(e.toolCallId, bubble(c, "msg-tool", el("span", { class: "mono" }, e.toolCallName), " ",
                                           el("span", { class: "badge badge-running" }, "running")));
            break;
        case "TOOL_CALL_RESULT": {
            const t = tools.get(e.toolCallId);
            if (t) t.querySelector(".badge").replaceWith(el("span", { class: "badge badge-ok" }, "done"));
            break;
        }
        case "CUSTOM":
            if (e.name === "agent.approval.pending") {
                const v = e.value || {};
                bubble(c, "msg-approval", el("strong", null, "Waiting for approval"), " of ", el("span", { class: "mono" }, v.tool),
                       " (gateway approval " + shortApproval(v.approval_id) + ") ",
                       el("button", { class: "link", onclick: () => cockpit.location.go(["approvals", v.approval_id]) }, "Review in Approvals"));
            } else if (e.name === "agent.approval.resolved") {
                const v = e.value || {};
                bubble(c, "msg-note", "Approval " + shortApproval(v.approval_id) + " of " + v.tool + ": ", stateBadge(v.outcome));
            } else if (e.name === "agent.limit.exceeded") {
                const v = e.value || {};
                bubble(c, "msg-note", "Stopped at the limit " + v.limit + " (" + v.value + ").");
            }
            break;
        case "RUN_ERROR":
            bubble(c, "msg-error", e.message || "the run failed");
            break;
        }
    };
    const body = JSON.stringify({
        threadId: c.thread, runId: newId("r"), state: {}, tools: [], context: [], forwardedProps: {},
        messages: [{ id: newId("m"), role: "user", content: text }],
    });
    const http = cockpit.http({ unix: agentSocket(name) });
    const req = http.request({ method: "POST", path: "/agui", body,
                               headers: { "Content-Type": "application/json", Accept: "text/event-stream" } });
    req.stream(sseParser(onEvent));
    const done = () => {
        c.busy = false;
        c.send.disabled = false;
    };
    req.then(done, ex => {
        // One run per thread (§11): the previous message's run still
        // works, as when its stream broke off.
        bubble(c, "msg-error", ex && ex.status === 409
            ? "The agent is still working on your previous message: its answer is in Activity. Send again when it has finished."
            : "The agent did not answer: " + describeAgentFailure(ex));
        done();
    });
}

/* chatSection is the agent's chat, or why there is none. */
function chatSection(a) {
    const agui = a.status && a.status.interaction && a.status.interaction.agui;
    if (!agui) {
        return el("section", { class: "card" }, el("h2", null, "Chat"),
            el("p", { class: "muted" }, a.status ? "This agent does not offer chat (interaction.agui is off)." :
               "Chat needs the agent's socket."));
    }
    return chatOf(a).node;
}
