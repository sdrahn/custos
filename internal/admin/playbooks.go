package admin

import "github.com/sdrahn/custos/internal/playbook"

// Playbooks are the diagnostic playbooks (§14c): the package's, replaced
// or hidden by the administrator's; a file that does not parse is listed
// with its error.
func (a *Admin) Playbooks() (map[string]*playbook.Playbook, error) { return playbook.Load(a.Root) }
