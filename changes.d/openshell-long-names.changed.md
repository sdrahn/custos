- An agent in an OpenShell sandbox may have a name of up to 19
  characters, where it had 13: its sandbox is `agent-<name>` as before
  where that fits OpenShell's 19 characters, and `<name>` itself beyond.
  A name of 14 to 19 characters does not begin with `agent-` (its
  sandbox would be another agent's). Existing agents and their sandboxes
  stay as they are; `openshell.env` names the sandbox where it is not
  `agent-<name>`.
