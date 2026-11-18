- `agent-admin openshell-setup --user USER --driver container|systemd
  [--ca FILE] [--check]` sets up OpenShell on the host for OpenShell
  agents, for either compute driver. It installs nothing: it checks
  that OpenShell, Podman or Docker, or the systemd compute driver are
  installed, and names what is missing. It then configures the driver,
  registers OpenShell's gateway for root as root's own files, and puts
  mcp-gateway's CA where the driver needs it. Before, these were steps
  by hand in chapter 11.
