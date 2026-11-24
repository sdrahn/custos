- `custos-selinux` requires `selinux-policy` by its date only
  (`>= 20260826`), where it required the build's version with its git
  snapshot (`>= 20260826+git190.1d6a6b164`): a build against a newer
  snapshot of the same policy, not yet released to hosts, could not be
  installed.
