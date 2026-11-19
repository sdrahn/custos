- Cockpit's new-agent form names an OpenShell agent's OAuth client after
  the agent's name as typed (`client_id: agent-NAME`). Before, a
  configuration drafted from a template kept the client id of the
  template's name after the agent was renamed, and `idp-client --keycloak
  --apply` (and its form) then refused the agent: "gateway.oauth.client_id
  is agent-health-check: --apply makes and replaces only agent-NAME". A
  client id written by hand stays as it is.
