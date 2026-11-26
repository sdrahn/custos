package admin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/host"
)

// The agents.yaml of 0.9.16, whose bridge ran as the delegating agent's
// user (one of agentsShippedBefore).
const agentsYAML0916 = `# mcp-gateway server definition: delegation between agents
# (Custos CONTRACT.md §9). Installed as
# /usr/share/custos/gateway/servers.d/agents.yaml; link or copy it
# to /etc/mcp-gateway/servers.d/ to let agents delegate.
#
# Its one tool, agents__delegate, is governed like any other: an agent
# may delegate only where its permissions say so, e.g.
#   - {server: agents, tool: delegate, args: {agent: '^patch-pilot$'}}
# (agent-admin permissions NAME).
version: 1
name: agents
command: ["/usr/libexec/custos/agent-delegate"]
selinux_type: mcpsrv_agents_t
isolation: principal
network: false
# The delegating agent's user: the callee checks it by SO_PEERCRED.
run_as: principal
discovery: shared
`

func shippedAgentsYAML(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "gateway", "servers.d", "agents.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func createAgent(t *testing.T, a *Admin, name string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), name+".yaml")
	os.WriteFile(f, []byte("version: 1\nname: "+name+"\nmodel: {provider: openai, name: m, endpoint: \"http://ai:8000/v1\"}\ngateway: {servers: [zypp]}\n"), 0o644)
	if err := a.Create(name, f); err != nil {
		t.Fatal(err)
	}
}

func sysusers(a *Admin, name string) string {
	b, _ := os.ReadFile(a.Path(filepath.Join(SysusersDir, "agent-"+name+".conf")))
	return string(b)
}

// An agent's user joins agent-clients only while the gateway's agents
// server runs the bridge as the delegating agent's user (before 0.9.17).
func TestCreateAgentClientsOnlyForAnOldBridge(t *testing.T) {
	a, _ := newAdmin(t)
	createAgent(t, a, "one") // no agents.yaml: delegation is off
	if s := sysusers(a, "one"); strings.Contains(s, "agent-clients") || !strings.Contains(s, "m agent-one mcp-users") {
		t.Errorf("without agents.yaml: %q", s)
	}
	host.WriteFile(a.Path(AgentsServer), shippedAgentsYAML(t), 0o644)
	createAgent(t, a, "two")
	if s := sysusers(a, "two"); strings.Contains(s, "agent-clients") {
		t.Errorf("with the bridge as custos-delegate: %q", s)
	}
	host.WriteFile(a.Path(AgentsServer), []byte(agentsYAML0916), 0o644)
	createAgent(t, a, "three")
	if s := sysusers(a, "three"); !strings.Contains(s, "m agent-three agent-clients\n") {
		t.Errorf("with the bridge as the agent's user: %q", s)
	}
}

func TestDelegationSetup(t *testing.T) {
	setup := func(t *testing.T, server string) (*Admin, *bytes.Buffer, func() []string) {
		a, r := newAdmin(t)
		host.WriteFile(a.Path(AgentsServerVendor), shippedAgentsYAML(t), 0o644)
		host.WriteFile(a.Path(AgentsServer), []byte(agentsYAML0916), 0o644)
		createAgent(t, a, "one") // made under the old bridge: in agent-clients
		createAgent(t, a, "two")
		host.WriteFile(a.Path(groupFile), []byte("mcp-users:x:480:agent-one,agent-two\nagent-clients:x:481:carol,agent-one,agent-two\n"), 0o644)
		if server != "" {
			host.WriteFile(a.Path(AgentsServer), []byte(server), 0o644)
		}
		out := &bytes.Buffer{}
		a.Out = out
		r.Cmds = nil
		return a, out, func() []string { return r.Cmds }
	}

	t.Run("an earlier shipped copy", func(t *testing.T) {
		a, out, cmds := setup(t, "")
		if err := a.DelegationSetup(true); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(a.Path(AgentsServer)); string(b) != agentsYAML0916 || len(cmds()) != 0 || !strings.Contains(out.String(), "todo") {
			t.Errorf("--check changed something: %v\n%s", cmds(), out)
		}
		out.Reset()
		if err := a.DelegationSetup(false); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(a.Path(AgentsServer)); !bytes.Equal(b, shippedAgentsYAML(t)) {
			t.Error("agents.yaml not replaced")
		}
		want := []string{"systemctl try-reload-or-restart mcp-gateway.service", "gpasswd -d agent-one agent-clients", "gpasswd -d agent-two agent-clients"}
		if got := strings.Join(cmds(), "\n"); got != strings.Join(want, "\n") {
			t.Errorf("commands:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
		}
		for _, n := range []string{"one", "two"} {
			if s := sysusers(a, n); strings.Contains(s, "agent-clients") || !strings.Contains(s, "m agent-"+n+" mcp-users") {
				t.Errorf("sysusers of %s: %q", n, s)
			}
		}
		if !strings.Contains(out.String(), "set  agent-one left agent-clients") {
			t.Errorf("output:\n%s", out)
		}
	})

	t.Run("the administrator's own copy", func(t *testing.T) {
		own := strings.Replace(agentsYAML0916, "network: false", "network: false\n# mine", 1)
		a, out, cmds := setup(t, own)
		if err := a.DelegationSetup(false); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(a.Path(AgentsServer)); string(b) != own || len(cmds()) != 0 || !strings.Contains(sysusers(a, "one"), "agent-clients") {
			t.Errorf("changed the administrator's: %v", cmds())
		}
		if !strings.Contains(out.String(), "kept") || !strings.Contains(out.String(), "run_as: "+DelegateUser) {
			t.Errorf("output:\n%s", out)
		}
	})

	t.Run("already the bridge's own account", func(t *testing.T) {
		a, out, cmds := setup(t, string(shippedAgentsYAML(t)))
		if err := a.DelegationSetup(false); err != nil {
			t.Fatal(err)
		}
		if len(cmds()) != 2 || strings.Contains(sysusers(a, "two"), "agent-clients") {
			t.Errorf("commands %v", cmds())
		}
		// Run again: nothing left to do.
		host.WriteFile(a.Path(groupFile), []byte("agent-clients:x:481:carol\n"), 0o644)
		out.Reset()
		before := len(cmds())
		a.DelegationSetup(false)
		if len(cmds()) != before || !strings.Contains(out.String(), "ok   no agent's user is in agent-clients") {
			t.Errorf("second run: %v\n%s", cmds()[before:], out)
		}
	})
}
