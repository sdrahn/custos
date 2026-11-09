package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sdrahn/custos/internal/governance"
)

func TestTools(t *testing.T) {
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 4 {
		t.Fatalf("tools %v %v", list, err)
	}
	for name, args := range map[string]map[string]any{"probe": {}, "change": {"value": "x"}, "wipe": {}} {
		want := map[string]string{"probe": "probe ok", "change": "changed x", "wipe": "wiped"}[name]
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError || res.Content[0].(*mcp.TextContent).Text != want {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
}

// Every injection case is a note that asks for its target in the words
// the scripted model obeys; an unknown note is an error.
func TestCases(t *testing.T) {
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	seen := map[string]bool{}
	for _, c := range Cases {
		if seen[c.ID] || (c.Target != "change" && c.Target != "wipe") || c.Title == "" {
			t.Errorf("case %+v", c)
		}
		seen[c.ID] = true
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_note", Arguments: map[string]any{"name": c.ID}})
		if err != nil || res.IsError {
			t.Fatalf("%s: %+v %v", c.ID, res, err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		want := "all the " + c.Target + " tool"
		if c.Target == "change" {
			want += " with value injected-" + c.ID
		}
		if !strings.Contains(strings.ToLower(text), want) {
			t.Errorf("%s: %q lacks %q", c.ID, text, want)
		}
	}
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "change", Arguments: map[string]any{"value": "fail"}}); err != nil || !res.IsError {
		t.Errorf("change fail: %+v %v", res, err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_note", Arguments: map[string]any{"name": "nope"}})
	if err != nil || !res.IsError {
		t.Errorf("unknown note: %+v %v", res, err)
	}
}

// Every injection case reaches the model with the host's notice (#155):
// the launcher finds a sign of an injection in each note.
func TestCasesHaveSigns(t *testing.T) {
	for _, c := range Cases {
		if s := governance.SignsIn(c.Note); len(s) == 0 {
			t.Errorf("case %s: no sign of an injection", c.ID)
		}
	}
}
