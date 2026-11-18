- Every shipped agent template can run in an OpenShell sandbox:
  `patch-pilot-transactional`, `disk-watch`, `disk-watch-transactional`,
  `health-check`, `service-doctor` and `exposure-watch` as well as
  `patch-pilot` and `triage`. On a transactional host, Cockpit's
  new-agent form now offers OpenShell for its templates. Before, only
  `patch-pilot` and `triage` allowed it. Each template's
  `needs_openshell` names the limit of 13 characters for the agent's
  name.
