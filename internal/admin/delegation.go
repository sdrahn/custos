package admin

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/host"
)

// Delegation between agents (CONTRACT.md §9): the gateway's server
// agents, as Custos ships it and as the gateway reads it, and the account
// its bridge runs as since 0.9.17.
const (
	AgentsServerVendor = "/usr/share/custos/gateway/servers.d/agents.yaml"
	AgentsServer       = "/etc/mcp-gateway/servers.d/agents.yaml"
	DelegateUser       = "custos-delegate"
	AgentClients       = "agent-clients"
	groupFile          = "/etc/group"
)

// agentsShippedBefore are the SHA-256 digests of the agents.yaml earlier
// releases shipped (up to 0.9.16), which ran the bridge as the delegating
// agent's user (run_as: principal): a copy that is one of them is no
// administrator's change.
var agentsShippedBefore = []string{
	"3e80ec239f34c26bf3957e3f435a3319dfd0d1540beaeabb2d3a2692f346c561",
	"c57e61ba31afa8882675a6898f6f49ac884f32c25df9012adef9d2d0b15b9cf2",
}

// agentsServerRunAs is the run_as of the gateway's server agents: "" when
// it is not defined (delegation is off).
func (a *Admin) agentsServerRunAs() (string, []byte, error) {
	b, err := os.ReadFile(a.Path(AgentsServer))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	var def struct {
		RunAs string `yaml:"run_as"`
	}
	if err := yaml.Unmarshal(b, &def); err != nil {
		return "", b, fmt.Errorf("%s: %w", AgentsServer, err)
	}
	if def.RunAs == "" {
		def.RunAs = "principal" // the gateway's default
	}
	return def.RunAs, b, nil
}

// agentsNeedClients reports whether agents' users must be members of
// agent-clients: while the gateway's server agents runs the bridge as the
// delegating agent's user (a definition from before 0.9.17), which then
// connects to the other agent's socket as that user.
func (a *Admin) agentsNeedClients() bool {
	runAs, _, err := a.agentsServerRunAs()
	return err == nil && runAs != "" && runAs != DelegateUser
}

// DelegationSetup brings delegation on this host to the account
// custos-delegate (CONTRACT.md §9), changing nothing with check: an
// agents.yaml that is an unchanged earlier shipped copy is replaced with
// the shipped one (and the gateway reloaded); then, once the bridge runs
// as custos-delegate or delegation is off, the agents' users leave
// agent-clients, which they needed only for the bridge, and their
// sysusers files no longer put them back. A definition of the
// administrator's that still runs as the delegating agent's user is kept,
// and the users with it. Each step is printed as ok, set, todo (check) or
// kept.
func (a *Admin) DelegationSetup(check bool) error {
	say := func(kind, f string, args ...any) { fmt.Fprintf(a.Out, "%-4s %s\n", kind, fmt.Sprintf(f, args...)) }
	runAs, b, err := a.agentsServerRunAs()
	if err != nil {
		return err
	}
	switch {
	case runAs == "":
		say("ok", "delegation is not enabled (no %s)", AgentsServer)
	case runAs == DelegateUser:
		say("ok", "%s runs the bridge as %s", AgentsServer, DelegateUser)
	case slices.Contains(agentsShippedBefore, digest(b)):
		if check {
			say("todo", "%s is an earlier release's (the bridge as the delegating agent's user): replace it with %s", AgentsServer, AgentsServerVendor)
			break
		}
		nb, err := os.ReadFile(a.Path(AgentsServerVendor))
		if err != nil {
			return fmt.Errorf("the shipped definition: %w", err)
		}
		if err := host.WriteFile(a.Path(AgentsServer), nb, 0o644); err != nil {
			return err
		}
		if err := a.Run(nil, "systemctl", "try-reload-or-restart", "mcp-gateway.service"); err != nil {
			say("set", "%s replaced with %s; reloading the gateway failed (%v): reload it", AgentsServer, AgentsServerVendor, err)
		} else {
			say("set", "%s replaced with %s, the gateway reloaded: the bridge runs as %s", AgentsServer, AgentsServerVendor, DelegateUser)
		}
		runAs = DelegateUser
	default:
		say("kept", "%s runs the bridge as %q and is not a shipped copy: set run_as: %s in it (or copy %s), then run this again; "+
			"until then the agents' users stay in %s, which that bridge needs", AgentsServer, runAs, DelegateUser, AgentsServerVendor, AgentClients)
		return nil
	}
	names, err := a.List()
	if err != nil {
		return err
	}
	members, err := a.groupMembers(AgentClients)
	if err != nil {
		return err
	}
	changed := false
	for _, name := range names {
		user := "agent-" + name
		sf := a.Path(filepath.Join(SysusersDir, user+".conf"))
		line := "m " + user + " " + AgentClients
		sb, _ := os.ReadFile(sf)
		inFile := slices.Contains(strings.Split(string(sb), "\n"), line)
		inGroup := members[user]
		if !inFile && !inGroup {
			continue
		}
		changed = true
		if check {
			say("todo", "%s leaves %s", user, AgentClients)
			continue
		}
		if inFile {
			var out []string
			for _, l := range strings.Split(string(sb), "\n") {
				if l != line {
					out = append(out, l)
				}
			}
			if err := host.WriteFile(sf, []byte(strings.Join(out, "\n")), 0o644); err != nil {
				return err
			}
		}
		if inGroup {
			if err := a.Run(nil, "gpasswd", "-d", user, AgentClients); err != nil {
				return fmt.Errorf("taking %s out of %s: %w", user, AgentClients, err)
			}
		}
		say("set", "%s left %s", user, AgentClients)
	}
	if !changed {
		say("ok", "no agent's user is in %s", AgentClients)
	}
	return nil
}

// groupMembers are the members of group in /etc/group.
func (a *Admin) groupMembers(group string) (map[string]bool, error) {
	f, err := os.Open(a.Path(groupFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) == 4 && fields[0] == group {
			for _, m := range bytes.Split([]byte(fields[3]), []byte(",")) {
				if len(m) > 0 {
					out[string(m)] = true
				}
			}
		}
	}
	return out, sc.Err()
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
