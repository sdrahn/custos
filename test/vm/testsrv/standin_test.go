package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sdrahn/custos/internal/playbook"
)

// The stand-ins cover every tool the shipped playbooks name (#227): a new
// step on a server or tool they lack fails here, not in the VM.
func TestStandInsCoverThePlaybooks(t *testing.T) {
	root := t.TempDir()
	vendor := filepath.Join(root, playbook.Vendor)
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../../../packaging/playbooks/*.yaml")
	if len(files) == 0 {
		t.Fatal("no shipped playbooks")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(vendor, filepath.Base(f)), b, 0o644)
	}
	all, err := playbook.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range all {
		if p.Error != "" {
			t.Errorf("%s: %s", name, p.Error)
		}
		for _, s := range p.Steps {
			server, tool, ok := strings.Cut(s.Tool, "__")
			if !ok || !slices.Contains(StandIns[server], tool) {
				t.Errorf("%s: step tool %s has no stand-in", name, s.Tool)
			}
		}
	}
}

// A stand-in offers its tools under the server's name and answers any
// arguments, naming them; there is none for an unknown server.
func TestStandIn(t *testing.T) {
	if StandIn("no-such") != nil {
		t.Error("a stand-in for an unknown server")
	}
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := StandIn("systemd").Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != len(StandIns["systemd"]) {
		t.Fatalf("tools %v %v", list, err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_log", Arguments: map[string]any{"unit": "web.service"}})
	if err != nil || res.IsError || res.Content[0].(*mcp.TextContent).Text != `stand-in systemd__list_log: nothing wrong found (arguments {"unit":"web.service"})` {
		t.Errorf("list_log: %+v %v", res, err)
	}
}
