- `agent-admin check`, `create` and `edit` note an OpenShell agent's
  `gateway.oauth.refresh_before_seconds` below 90: OpenShell's gateway
  renews tokens once a minute, so such a token can expire before it is
  renewed, and a sandbox started then has no token. The guide's example
  uses 120, where it used 60.
