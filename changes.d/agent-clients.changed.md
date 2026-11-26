- Agents' users are no longer members of `agent-clients`: only the
  delegation bridge needed that, and it has an account of its own since
  0.9.17 (`custos-delegate`). A new agent's user joins `agent-clients`
  only while `/etc/mcp-gateway/servers.d/agents.yaml` still runs the
  bridge as the delegating agent's user. The new command
  `agent-admin delegation-setup` brings an existing host over: it
  replaces an unchanged earlier copy of `agents.yaml` with the shipped
  one (a changed one it keeps, saying what to change), then takes the
  agents' users out of `agent-clients` and out of their sysusers files,
  so that a reboot does not put them back. `--check` shows what it
  would do. Run it once after the update; it replaces copying
  `agents.yaml` by hand.
