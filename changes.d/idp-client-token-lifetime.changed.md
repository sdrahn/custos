- `idp-client --keycloak --apply` (and Cockpit's "Make the client and
  set the credential") keeps the token lifetime of the client it
  replaces, also one set in Keycloak's admin console. Before, it reset
  the lifetime to 5 minutes.
