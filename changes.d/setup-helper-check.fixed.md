- `agent-admin openshell-setup --driver systemd` checks that the
  driver's helper runs after starting it (its socket,
  `/run/openshell-systemd-helper/helper.sock`), and otherwise stops and
  names the helper's journal. Before, it went on, and failed waiting for
  the driver's socket, pointing at the driver's journal, which only said
  that there was no helper.
