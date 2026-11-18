- The relay of an OpenShell agent reads root's OpenShell registration
  (`/root/.config/openshell`) by a type of its own,
  `agent_openshell_home_t`, where its SELinux domain could read every
  user's `~/.config` (`config_home_t`; security review 0.8, S5). The
  package and `agent-admin openshell-setup` restore the label; after
  copying the registration by hand, run
  `restorecon -R /root/.config/openshell`.
