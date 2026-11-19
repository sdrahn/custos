- An agent's model calls to an OpenAI-compatible endpoint
  (`provider: openai`) no longer fail in the middle of a run with
  `model endpoint: Post ".../chat/completions": EOF`. Each call now goes
  on a new connection. Before, a call reused the previous call's
  connection, which the server or a proxy between could have closed
  meanwhile: LiteLLM, for one, closes a connection idle for 5 seconds,
  and a tool call between two model calls often takes longer. It
  showed most in OpenShell sandboxes, whose proxy keeps the agent's side
  of such a connection open.
