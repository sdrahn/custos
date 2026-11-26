- An agent under systemd can delegate to an agent in an OpenShell
  sandbox: SELinux let the gateway's delegation bridge connect to an
  agent's socket only when it is the agent's own (`agent_t`); an
  OpenShell agent's socket is its relay's, which systemd labels
  `agent_relay_t`. The policy module allows that too. Delegation
  between agents under systemd was not affected.
