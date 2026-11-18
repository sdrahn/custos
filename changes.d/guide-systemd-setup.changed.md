- The guide's chapter 11 gives the systemd compute driver's setup as
  commands, in order: OpenShell for its user, its gateway running and
  registered (with OpenShell's package: `systemctl --user … enable
  --now openshell-gateway` and `openshell gateway add`), the driver's
  packages, a reboot on a transactional system, then
  `agent-admin openshell-setup --driver systemd`. It says which
  certificate `--ca` takes and when it is not needed, and that with the
  systemd driver that CA is trusted host-wide.
