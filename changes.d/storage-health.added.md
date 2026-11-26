- The diagnostic playbook `storage-health` checks the disks' health with
  mcp-gateway's server `storage` (package `mcp-gateway-storage-server`,
  mcp-gateway 1.17): the disks, each one's SMART or NVMe health log with
  a verdict, the btrfs error counters and the last scrub, and the
  journal's I/O and btrfs errors. It reads only. Without the server it
  reads the journal alone and says so. The `health-check` template runs
  it every Monday, and it and the `disk-watch` templates may call the
  server's four tools, each named, without an approval.
  `custos-setup --storage` links the server's profile when the package
  is installed; the server holds capabilities and reads the disks, so
  offering it is your choice, as with `--restarts`. An agent made
  before this release gets the tools with `agent-admin apply-template`;
  it keeps its schedules, so add the weekly run to it yourself.
