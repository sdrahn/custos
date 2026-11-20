- `agent-admin idp-client NAME --keycloak [--apply] --token-lifetime D`
  sets the lifetime of the agent's OAuth tokens in Keycloak (default 5
  minutes, at most an hour). Each renewal of an OpenShell agent's token
  closes its sandbox's connections; with 30 minutes and
  `refresh_before_seconds: 120`, that happens every 28 minutes instead
  of every 3.
