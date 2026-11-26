package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StandIns are the servers the shipped diagnostic playbooks
// (packaging/playbooks) call, with the tools they name: testsrv --as
// SERVER stands in for one, so that the playbooks run on every backend
// in the functional tests (#227) without the real servers, which other
// tests check. Each tool takes any arguments and answers that it is a
// stand-in, naming them.
var StandIns = map[string][]string{
	"exec": {"boot_time", "boot_blame", "disk_usage", "memory_usage", "pressure_stall", "load_average",
		"package_dependencies", "selinux_status", "time_status", "reboot_status"},
	"systemd":    {"list_loaded_units", "list_log", "get_file"},
	"custos-net": {"network_addresses", "network_routes", "listening_sockets"},
	"snapper":    {"list_snapshots"},
	"tukit":      {"list_snapshots", "list_updates", "get_status"},
	"zypp":       {"check_updates"},
	"restarts":   {"pending_restarts"},
	"storage":    {"storage_devices", "device_health", "btrfs_device_stats", "btrfs_scrub_status"},
}

// StandIn is the stand-in for server, or nil when there is none.
func StandIn(server string) *mcp.Server {
	tools, ok := StandIns[server]
	if !ok {
		return nil
	}
	s := mcp.NewServer(&mcp.Implementation{Name: server + "-stand-in", Version: "1"}, nil)
	for _, t := range tools {
		name := server + "__" + t
		mcp.AddTool(s, &mcp.Tool{Name: t, Description: "Stands in for " + name + " (Custos's functional tests)."},
			func(_ context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
				b, _ := json.Marshal(args)
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "stand-in " + name + ": nothing wrong found (arguments " + string(b) + ")"}}}, nil, nil
			})
	}
	return s
}

// standInNames are the stand-ins' server names, sorted.
func standInNames() []string {
	var out []string
	for s := range StandIns {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func standInUsage() string { return fmt.Sprintf("testsrv --as SERVER: one of %v", standInNames()) }
