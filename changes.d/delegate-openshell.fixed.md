- Delegation to an agent in an OpenShell sandbox works: it failed with
  `dial unix /run/agents/NAME/api.sock: connect: permission denied`.
  SELinux let the gateway's delegation bridge connect to an agent's
  socket only when it is the agent's own (`agent_t`); an OpenShell
  agent's socket is its relay's, which systemd labels `agent_relay_t`.
  The policy module allows that too. Delegation between agents under
  systemd was not affected.
