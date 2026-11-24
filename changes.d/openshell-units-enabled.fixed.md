- `agent-admin openshell-setup` checks that the systemd compute driver's
  helper and the driver's user service are enabled to start at boot, not
  only that they run, and enables them where they are not. One that ran
  without being enabled was gone after the next boot, and OpenShell
  agents did not start; `--check` said it was fine.
