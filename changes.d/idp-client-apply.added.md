- `agent-admin idp-client NAME --keycloak --apply --admin USER` sets up
  an OpenShell agent's OAuth client in one command: it logs in to
  Keycloak's admin API (the password asked for, or on standard input),
  makes the client, lists the issuer for agents, checks a token and sets
  the agent's `gateway-client-secret`. No `kcadm.sh`, no container
  command and no secret in a file. Before, this took a `kcadm.sh` script
  and four more commands.
