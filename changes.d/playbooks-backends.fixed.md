- An agent in an OpenShell container sandbox can run the shipped
  diagnostic playbooks: the agent image has them in
  `/usr/share/custos/playbooks`. Before, it answered `no playbook "…" on
  this host` for every one. Rebuild the image with the playbooks in its
  build context (chapter 11, step 2), then set each agent's gateway
  credential again, which recreates its sandbox from the new image.
