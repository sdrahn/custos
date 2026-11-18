- An agent created from a template can run in an OpenShell sandbox:
  `agent-admin create NAME --template T --sandbox openshell
  [--openshell-driver container|systemd]` (and `draft`) add
  `gateway.url`, the agent's OAuth client and the `sandbox` line from
  mcp-gateway's HTTPS settings. A template says where it may run
  (`backends` in `template.yaml`); `patch-pilot` and `triage` allow
  OpenShell. Cockpit's new-agent form offers the choice. Before, every
  agent from a template ran as a systemd service.
