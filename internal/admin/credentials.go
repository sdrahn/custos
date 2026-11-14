package admin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/host"
)

// Credential is one of an agent's credentials, as agent-admin credentials
// lists it (CONTRACT.md §17): its name and what uses it, never its value.
type Credential struct {
	Name string `json:"name"`
	// Stored: in the credential store (/etc/credstore.encrypted). For
	// one HeldBy OpenShell: OpenShell has it (the relay's socket is
	// enabled once set-credential gave it the sandbox).
	Stored   bool   `json:"stored"`
	StoredAt string `json:"stored_at,omitempty"`
	// UsedBy: "model" (model.credential), "gateway" (an OpenShell
	// agent's gateway token or OAuth client secret); empty: nothing.
	UsedBy []string `json:"used_by"`
	HeldBy string   `json:"held_by,omitempty"`
}

// AgentCredentials are an agent's credentials; Missing names those its
// configuration needs that are not stored: the agent does not start.
type AgentCredentials struct {
	Credentials []Credential `json:"credentials"`
	Missing     []string     `json:"missing"`
}

// Credentials lists the credentials of the named agents, or all agents'.
// The store is root's alone, so this needs root.
func (a *Admin) Credentials(names ...string) (map[string]*AgentCredentials, error) {
	if len(names) == 0 {
		var err error
		if names, err = a.List(); err != nil {
			return nil, err
		}
	}
	if _, err := os.ReadDir(a.Path(CredstoreDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading the credential store: %w", err)
	}
	out := map[string]*AgentCredentials{}
	for _, n := range names {
		if err := host.CheckName(n); err != nil {
			return nil, err
		}
		// Without a readable configuration, what is stored is listed all
		// the same.
		cfg, _ := config.Load(a.agentDir(n))
		ac := &AgentCredentials{Credentials: []Credential{}, Missing: []string{}}
		prefix := "agent-" + n + "."
		files, _ := filepath.Glob(a.Path(filepath.Join(CredstoreDir, prefix+"*")))
		model := ""
		if cfg != nil {
			model = cfg.Model.Credential
		}
		for _, f := range files {
			c := Credential{Name: strings.TrimPrefix(filepath.Base(f), prefix), Stored: true, UsedBy: []string{}}
			if st, err := os.Stat(f); err == nil {
				c.StoredAt = st.ModTime().UTC().Format(time.RFC3339)
			}
			if c.Name == model {
				c.UsedBy = append(c.UsedBy, "model")
			}
			ac.Credentials = append(ac.Credentials, c)
		}
		if model != "" {
			if stored, _ := a.credentialStored(n, model); !stored {
				ac.Credentials = append(ac.Credentials, Credential{Name: model, UsedBy: []string{"model"}})
				ac.Missing = append(ac.Missing, model)
			}
		}
		if cfg != nil && cfg.Sandbox.Kind == "openshell" {
			c := Credential{Name: "gateway-token", UsedBy: []string{"gateway"}, HeldBy: "openshell",
				Stored: a.enabled("agent-openshell@" + n + ".socket")}
			if cfg.Gateway.OAuth != nil {
				c.Name = "gateway-client-secret"
			}
			ac.Credentials = append(ac.Credentials, c)
			if !c.Stored {
				ac.Missing = append(ac.Missing, c.Name)
			}
		}
		sort.Slice(ac.Credentials, func(i, j int) bool { return ac.Credentials[i].Name < ac.Credentials[j].Name })
		out[n] = ac
	}
	return out, nil
}

// RemoveCredential removes one of an agent's stored credentials. One its
// model uses is removed only with force: the agent does not start
// without it. An OpenShell agent's gateway credential is OpenShell's,
// not in the store: delete and create the agent to drop it.
func (a *Admin) RemoveCredential(name, cred string, force bool) error {
	if err := host.CheckName(name); err != nil {
		return err
	}
	if !config.ValidCredential(cred) {
		return errors.New("not a valid credential name")
	}
	if cred == "gateway-token" || cred == "gateway-client-secret" {
		return fmt.Errorf("%s is held by OpenShell, not in the credential store; set-credential replaces it", cred)
	}
	if cfg, err := config.Load(a.agentDir(name)); err == nil && cfg.Model.Credential == cred && !force {
		return fmt.Errorf("agent %s's model uses %s, and the agent does not start without it: change model.credential first, or --force", name, cred)
	}
	err := os.Remove(a.Path(filepath.Join(CredstoreDir, "agent-"+name+"."+cred)))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agent %s has no stored credential %s", name, cred)
	}
	return err
}
