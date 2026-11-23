- Cockpit's form for a new agent no longer creates it from a
  configuration drafted for something else. When the draft for the
  chosen sandbox fails, Create waits, and the form says why and what to
  do, as for a name too long for an OpenShell sandbox (at most 13
  characters; a template's own name can be longer). A new name drafts
  it again. Before, the form kept the earlier configuration, for a
  systemd service, and a shorter name only renamed it, so the agent
  could be created without its sandbox.
