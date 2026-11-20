- An OpenShell agent with an OAuth client is no longer cut off every
  minute. Its token was renewed at every one of OpenShell's sweeps when
  `gateway.oauth.refresh_before_seconds` was unset (OpenShell's 300) and
  the token lived 5 minutes, as Keycloak's do, and each renewal closed
  the sandbox's connections: calls to the model and the gateway ended in
  `EOF`, and the agent lost its tool results. Agents drafted for OpenShell
  (from a template, or Cockpit's form) now get
  `refresh_before_seconds: 120`; `agent-admin check` notes an agent
  without it, and `check-token` and `idp-client --keycloak --apply` say
  when the token's lifetime does not suit the window. Correct an existing
  agent with `agent-admin edit`, then set its credential again.
- `agent-admin set-credential` (and `idp-client --keycloak --apply`) on an
  OpenShell agent that has a sandbox replaces its provider: it stops the
  relay and deletes the sandbox first. Before, OpenShell refused to delete
  the provider ("attached to sandbox(es)") and the credential was not set,
  while Keycloak already had the client's new secret.
- `agent-admin edit` of an OpenShell agent whose provider profile changes
  (`gateway.url`, or `gateway.oauth`'s token URL, scopes or
  `refresh_before_seconds`) replaces the profile: it stops the agent,
  deletes its sandbox and provider, imports the profile anew, and asks
  for `set-credential`. Before, it failed with "custom provider profile
  … already exists" after writing the new files.
