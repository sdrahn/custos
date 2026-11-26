- Feedback on a diagnosis report longer than 4 KB is recorded: it was
  refused with "agent … has no diagnosis report for activity …", in
  Cockpit and by `agent-admin feedback`, because `journalctl` gave the
  report's record with the report left out (a field over 4096 bytes is
  null in its JSON unless asked for `--all`). For the same reason
  `agent-admin readiness` left such reports out of an agent's diagnosis
  history (reports, verified quotes, feedback). Every reading of agent
  records by `agent-admin` now asks for `--all`. Reports written before
  the update are in the journal: they take feedback and count once it is
  installed.
