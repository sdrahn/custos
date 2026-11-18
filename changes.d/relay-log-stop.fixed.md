- Stopping an agent in an OpenShell sandbox on rootless Podman no longer
  logs an SELinux denial (`kill` in `cap_userns` for `agent_relay_t`):
  the relay leaves its log command (`podman logs`, run in the OpenShell
  user's user namespace) to the unit's stop, which ends it with the
  unit's other processes. Before, the relay tried to signal it itself
  and was refused.
