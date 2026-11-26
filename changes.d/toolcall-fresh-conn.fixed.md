- An agent in OpenShell no longer has a tool call fail with `EOF` right
  after OpenShell closed the sandbox's connections (at a token renewal,
  or when another sandbox was set up): each tool call goes to the
  gateway on a connection of its own, never on an idle one OpenShell
  may have closed. A call cut off while it runs is sent again with its
  idempotency key (below).
