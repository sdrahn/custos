- OpenShell agents can run on OpenShell's systemd compute driver:
  `sandbox.openshell_driver: systemd` in `agent.yaml` creates the
  sandbox without an image (`--from host`), and the relay reads the
  agent's records from the journal of the sandbox's workload unit.
  The default, `container`, is Docker or Podman, as before.
