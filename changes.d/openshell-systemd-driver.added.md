- OpenShell agents can run on OpenShell's systemd compute driver:
  `sandbox.openshell_driver: systemd` in `agent.yaml` creates the
  sandbox without an image (`--from host`), and the relay reads the
  agent's records from the journal of the sandbox's workload unit.
  The default, `container`, is Docker or Podman, as before.
- Root's copy of the OpenShell gateway's registration
  (`/root/.config/openshell`) must be root's own files: the guide's
  `cp -a` is followed by `chown -R root:root`. The relay reads it under
  SELinux to follow the systemd driver's journal.
