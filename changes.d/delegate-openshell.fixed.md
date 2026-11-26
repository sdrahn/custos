<<<<<<< HEAD
- Delegation to an agent in an OpenShell sandbox works: it failed with
  `dial unix /run/agents/NAME/api.sock: connect: permission denied`.
  SELinux let the gateway's delegation bridge connect to an agent's
  socket only when it is the agent's own (`agent_t`); an OpenShell
  agent's socket is its relay's, which systemd labels `agent_relay_t`.
  The policy module allows that too. Delegation between agents under
  systemd was not affected.
=======
- Agents in OpenShell can delegate, and be delegated to. Delegation
  from one failed with `dial unix /run/agents/NAME/api.sock: connect:
  permission denied`: the gateway ran the bridge as the delegating
  agent's user, and an agent in OpenShell, which calls the gateway with
  a token, has none, so it got a throwaway account that may not reach
  the sockets. The bridge now runs as the system account
  `custos-delegate` for every agent. The gateway tells it which agent it
  serves (`MCP_GATEWAY_PRINCIPAL`, mcp-gateway 1.17.1); a token's agent
  counts only from an issuer trusted for agents. It names that agent to
  the other one, which believes the name from `custos-delegate` only.
  Custos needs mcp-gateway 1.17.1 or later now. After the update, copy
  `/usr/share/custos/gateway/servers.d/agents.yaml` to
  `/etc/mcp-gateway/servers.d/` again: an earlier copy still runs the
  bridge as the delegating agent's user.
>>>>>>> dfcebb2 (Delegation for agents in OpenShell: the bridge as custos-delegate)
