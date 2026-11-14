package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Owners (§7, O1): the gateway's approver forms in the agent data, never
// the agent itself.
func TestSetOwners(t *testing.T) {
	a, _ := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(yml), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	if err := a.SetOwners("patch-pilot", []string{"user:alice", "group:web-team", "user:alice", "remote-group:ops"}); err != nil {
		t.Fatal(err)
	}
	d, _ := a.ReadGateway("patch-pilot")
	if strings.Join(d.Owners, ",") != "user:alice,group:web-team,remote-group:ops" || d.Mode != "supervised" {
		t.Errorf("%+v", d)
	}
	b, _ := os.ReadFile(a.gatewayFile("patch-pilot"))
	if !strings.Contains(string(b), "owners:") {
		t.Errorf("gateway data:\n%s", b)
	}
	for _, bad := range []string{"self", "user:agent-patch-pilot", "group:agent-triage", "alice", "user:", "user:a b", "root:x",
		// Groups every agent is in, and agents as remote approvers (security review M2).
		"group:agent-clients", "group:mcp-users", "remote-user:agent-triage", "remote-group:agent-x"} {
		if err := a.SetOwners("patch-pilot", []string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if d, _ := a.ReadGateway("patch-pilot"); len(d.Owners) != 3 {
		t.Error("a refused change changed the owners")
	}
	if err := a.SetOwners("patch-pilot", nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := a.ReadGateway("patch-pilot"); len(d.Owners) != 0 {
		t.Errorf("not cleared: %v", d.Owners)
	}
	p, _ := a.Show("patch-pilot")
	if p.Agents["patch-pilot"].Owners != nil {
		t.Error("show")
	}
}
