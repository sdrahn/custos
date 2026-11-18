- `agent-admin check`, `create`, `draft` and Cockpit's new-agent form
  refuse an OpenShell agent's name longer than 13 characters: its
  sandbox, `agent-<name>`, would exceed OpenShell's 19 characters for a
  sandbox's name. Before, the name was taken, and setting the agent's
  gateway credential failed after creating its provider
  (`name exceeds maximum length (22 > 19)`).
