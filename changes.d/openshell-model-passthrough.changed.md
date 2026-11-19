- In an OpenShell sandbox, the connection to the agent's model endpoint
  passes OpenShell's proxy at the TCP level, uninspected, still for
  `agent-launcher` only. Before, the proxy parsed and relayed every model
  request and answer as HTTP, though the agent's rule allowed them all,
  and long model calls with large requests failed there with
  `model endpoint: Post ".../chat/completions": EOF`, where the same agent
  outside OpenShell worked. `sandbox.openshell_inspect_model: true` keeps
  the inspection, with OpenShell's record of each request. An existing
  agent gets the new policy with `agent-admin edit` (its sandbox is
  recreated). An `https://` model endpoint's certificate is now checked
  by the agent, in a container against the agent image's trust store: a
  model server with a private CA's certificate needs that CA in the
  image, or `openshell_inspect_model: true`.
