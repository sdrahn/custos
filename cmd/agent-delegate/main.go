// agent-delegate is the "agents" MCP server mcp-gateway runs for agents
// that delegate to other agents (CONTRACT.md §9). It speaks MCP on stdio
// and runs as custos-delegate, in its own SELinux domain
// (mcpsrv_agents_t), the only one besides Cockpit that may reach the
// agents' sockets. The gateway tells it which agent it serves
// (MCP_GATEWAY_PRINCIPAL); the issuers trusted for agents say whether a
// token's agent is one.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sdrahn/custos/internal/delegate"
	"github.com/sdrahn/custos/internal/gatewayconf"
	"github.com/sdrahn/custos/internal/launcher"
)

func main() {
	issuers, err := gatewayconf.AgentIssuers("/")
	var who string
	if err == nil {
		who, err = delegate.Delegator(os.Getenv(delegate.PrincipalEnv), issuers)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-delegate:", err)
	}
	if err := delegate.Serve(context.Background(), "/run/agents", launcher.Version, who, err); err != nil {
		fmt.Fprintln(os.Stderr, "agent-delegate:", err)
		os.Exit(1)
	}
}
