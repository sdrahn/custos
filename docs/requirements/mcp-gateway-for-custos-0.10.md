# mcp-gateway requirements from Custos 0.10

Status: R8 is in mcp-gateway 1.17.2 (`_meta` key
`io.github.sdrahn.mcp-gateway/idempotencyKey`), and Custos uses it. The general constraints of
`mcp-gateway-for-custos-0.3.md` apply: nothing Custos-specific in the
gateway, policy decides who sees what, changes only add, and the
gateway's own records stay trustworthy.

## R8. A tool call that may be sent again without running twice

**Need.** An agent in an OpenShell sandbox reaches the gateway's HTTPS
listener through OpenShell's proxy. OpenShell closes a sandbox's open
connections at every token renewal and, as seen in CI, when another
sandbox is set up (Custos issue #375). A `tools/call` that is under way
then ends with EOF on the client's side, and the client cannot tell
whether the gateway received it, ran it, or both. It may not send it
again: the call may change the system (install a package, restart a
service), and running it twice is worse than failing once. So the agent
reports a failed call that may in fact have run.

Custos sends each `tools/call` on a connection of its own since 0.10
(#375), which avoids the case where a call goes out on a connection
OpenShell had already closed. A call that is cut off while it runs
remains: a long call (an update, an approval that waits) spans a
renewal sooner or later.

**Today** (mcp-gateway 1.17.1). The gateway runs every `tools/call` it
receives. A client that sends one again gets a second run, with a
second approval where one is needed.

**Requirement.**

1. A client may name a `tools/call` with an **idempotency key**, in its
   `_meta` under one documented key: an opaque string of bounded length,
   unique for that client's calls.
2. For a call with a key, the gateway keeps, per principal and key, the
   call's outcome for a bounded time (minutes, configurable): running,
   or its result. A second `tools/call` with the same principal and key,
   **and the same server, tool and arguments**, is not run again: it is
   answered with the first call's result, or, while that runs, waits for
   it and gets it. A pending approval is the same approval, not a new
   one.
3. A second call with the same key but another server, tool or
   arguments is refused (a client error), never run.
4. The audit records the repeat as such (the key and that it was
   answered from the first call), so that one run shows as one run.
5. Without a key, nothing changes.

**How Custos uses it.** The agent's gateway client (`internal/gateway`,
`bearer.RoundTrip`) gives each `tools/call` a key and, when the
connection ends without an answer (EOF), sends the call once more with
the same key, which today it may not do.

**Done when** a VM test of the gateway sends a `tools/call` that
changes something, cuts the connection before the answer, sends it
again with the same key and gets the result while the change happened
once; and a repeat with other arguments is refused.
