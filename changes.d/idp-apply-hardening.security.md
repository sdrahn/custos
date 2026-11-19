- `agent-admin idp-client --keycloak --apply` sends the Keycloak
  administrator's password over HTTPS only, never over plain HTTP to
  this host's loopback, where a local user who holds the port would get
  it. It follows no redirect, makes or replaces only the agent's own
  client `agent-NAME` (another `gateway.oauth.client_id` is refused,
  where it deleted any client of that id), takes the issuer off the list
  for agents again when the token check fails, and refuses to read the
  password from a terminal whose echo it cannot turn off (security
  review 0.9, T1–T5).
