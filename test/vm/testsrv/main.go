// testsrv is the MCP server of the functional tests (package
// custos-tests), registered with mcp-gateway as custos-test.
// It speaks MCP on stdio and has four tools: probe, which reads nothing
// and says so; change, which stands for a call that changes the system
// (it only answers; the gateway's audit records show that it ran; with
// the value "fail" it fails, a tool error);
// read_note, which returns a note an attacker wrote: a prompt-injection
// case (cases.go); and wipe, which stands for a destructive call that no
// test permits. The tests' permissions declare probe and read_note
// read-only and change not.
//
// Without arguments it speaks MCP on stdio; with --cases it prints the
// injection cases as JSON (id, title, target); with --as SERVER it stands
// in for a server the shipped playbooks call (standin.go); with --as-tools
// it prints the stand-ins' servers and tools as JSON.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type changeArgs struct {
	Value string `json:"value" jsonschema:"any text"`
}

type noteArgs struct {
	Name string `json:"name" jsonschema:"the note's name"`
}

// New returns the server.
func New() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "custos-test", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "probe", Description: "Reads nothing; answers probe ok."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "probe ok"}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "change", Description: "Stands for a change to the system."},
		func(_ context.Context, _ *mcp.CallToolRequest, a changeArgs) (*mcp.CallToolResult, any, error) {
			if a.Value == "fail" { // a change that fails: a tool error
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "change failed"}}}, nil, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "changed " + a.Value}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "read_note", Description: "Reads a note left on the system by its name."},
		func(_ context.Context, _ *mcp.CallToolRequest, a noteArgs) (*mcp.CallToolResult, any, error) {
			c, ok := caseByID(a.Name)
			if !ok {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no note " + a.Name}}}, nil, nil
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: c.Note}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "wipe", Description: "Stands for a destructive call; no test permits it."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "wiped"}}}, nil, nil
		})
	return s
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--as-tools" {
		b, _ := json.Marshal(StandIns)
		fmt.Println(string(b))
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "--as" {
		s := StandIn(os.Args[2])
		if s == nil {
			fmt.Fprintln(os.Stderr, standInUsage())
			os.Exit(2)
		}
		if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, "testsrv:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--cases" {
		b, _ := json.Marshal(Cases)
		fmt.Println(string(b))
		return
	}
	if err := New().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "testsrv:", err)
		os.Exit(1)
	}
}
