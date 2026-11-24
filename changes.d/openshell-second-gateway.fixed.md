- `agent-admin openshell-setup` stops and disables OpenShell's gateway
  and driver where they also run as another user than the gateway's,
  such as root after `systemctl --user` as root, before it writes root's
  registration again. A second gateway made a CA of its own and wrote
  its client certificates into root's registration: root's `openshell`
  then failed with `invalid peer certificate: BadSignature`, and
  OpenShell agents did not start. `--check` names it.
