- An agent in OpenShell whose tool call is cut off while it runs (the
  connection closed without an answer, as OpenShell does at every token
  renewal) asks the gateway for the call's outcome instead of reporting
  a failure that may in fact have run: each tool call carries an
  idempotency key, and the gateway answers a call sent again with it
  from the first one, which runs once (mcp-gateway 1.17.2, requirement
  R8). Custos needs mcp-gateway 1.17.2 or later now.
