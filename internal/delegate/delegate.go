// Package delegate is the "agents" MCP server the gateway runs for
// agents that delegate (CONTRACT.md §9): one tool, delegate, which
// starts an activity on another agent and waits for it.
//
// The gateway runs it as the system account custos-delegate, whichever
// agent delegates, and tells it whom it serves (MCP_GATEWAY_PRINCIPAL,
// mcp-gateway 1.17.1). It names that agent to the callee
// (X-Custos-Delegator), which believes the name from custos-delegate
// only. Whether an agent may delegate to another is gateway policy on
// the tool agents__delegate and its argument "agent".
package delegate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sdrahn/custos/internal/config"
)

type Args struct {
	Agent string `json:"agent" jsonschema:"the agent to delegate to"`
	Task  string `json:"task" jsonschema:"what the agent should do, in plain words"`
	// Trace is added by the delegating agent's governance plugin.
	Trace map[string]string `json:"_trace,omitempty" jsonschema:"set by the agent host; leave out"`
}

// PrincipalEnv is where mcp-gateway (1.17.1) tells an instance whom it
// serves: {"kind": "local"|"remote"|"none", "sub", "iss", "uid"}.
const PrincipalEnv = "MCP_GATEWAY_PRINCIPAL"

// Delegator is the agent the gateway runs the bridge for, from
// PrincipalEnv's value: a local principal agent-<name> (an agent under
// systemd, on the gateway's socket), or a remote one with the subject
// agent-<name>, no local account and an issuer of issuers (an agent in
// OpenShell, with a token; the issuers trusted for agents, CONTRACT.md
// §6). The kind and, for a remote principal, the issuer are checked
// before the subject: a local account agent-x and a token for agent-x
// are different principals. Anything else is no agent.
func Delegator(principal string, issuers []string) (string, error) {
	if principal == "" {
		return "", errors.New("the gateway did not say whom this bridge serves (" + PrincipalEnv + "): delegation needs mcp-gateway 1.17.1 or later")
	}
	var p struct {
		Kind string  `json:"kind"`
		Sub  string  `json:"sub"`
		Iss  string  `json:"iss"`
		UID  *uint32 `json:"uid"`
	}
	if err := json.Unmarshal([]byte(principal), &p); err != nil {
		return "", fmt.Errorf("%s: %w", PrincipalEnv, err)
	}
	switch p.Kind {
	case "local":
		if p.UID == nil {
			return "", errors.New("the caller is a local principal without a uid: not an agent")
		}
	case "remote":
		if !slices.Contains(issuers, p.Iss) {
			return "", fmt.Errorf("the caller's token is from %q, not an issuer trusted for agents: not an agent", p.Iss)
		}
		if p.UID != nil {
			// An agent's token is never mapped to a local account.
			return "", errors.New("the caller's token is mapped to a local account: not an agent")
		}
	default:
		return "", fmt.Errorf("the bridge serves %q, not an agent", p.Kind)
	}
	name, ok := strings.CutPrefix(p.Sub, "agent-")
	if !ok || !config.ValidName(name) {
		return "", fmt.Errorf("the caller %q is not an agent", p.Sub)
	}
	return name, nil
}

// Serve runs the server on stdio for the delegating agent delegator;
// with none, each call is refused with why.
func Serve(ctx context.Context, socketDir, version, delegator string, why error) error {
	s := mcp.NewServer(&mcp.Implementation{Name: "custos-agents", Version: version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delegate",
		Description: "Ask another agent on this host to do a task, and wait for its answer: its full last response, and its diagnosis report if it wrote one. The other agent works under its own permissions and approvals.",
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: ptr(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, a Args) (*mcp.CallToolResult, any, error) {
		if delegator == "" {
			return errResult("%v", why), nil, nil
		}
		return Ask(ctx, socketDir, delegator, a), nil, nil
	})
	return s.Run(ctx, &mcp.StdioTransport{})
}

// Ask is the tool: it starts the task on the agent's socket under
// socketDir for delegator, waits for the activity to end, and answers
// with its result.
func Ask(ctx context.Context, socketDir, delegator string, a Args) *mcp.CallToolResult {
	if !config.ValidName(a.Agent) {
		return errResult("unknown agent %q", a.Agent)
	}
	res, err := run(ctx, filepath.Join(socketDir, a.Agent, "api.sock"), map[string]any{"prompt": a.Task, "trace": a.Trace, "wait": true}, delegator)
	if err != nil {
		return errResult("%v", err)
	}
	return answer(a.Agent, res)
}

// answer is the delegating agent's result (CONTRACT.md §9): the other
// activity's id and state, its answer, which is its full response (§4;
// up to 32 KiB) or, from an agent before 0.4.2, its 500-byte summary, and
// its diagnosis report (§4a) when it wrote one. An activity that did not
// finish is an error.
func answer(agent string, res map[string]any) *mcp.CallToolResult {
	out := map[string]any{"agent": agent, "activity": res["id"], "state": res["state"], "answer": res["summary"]}
	if r, ok := res["response"].(string); ok && r != "" {
		out["answer"] = r
	}
	if rep, ok := res["report"]; ok && rep != nil {
		out["report"] = rep
	}
	if reason, ok := res["reason"].(string); ok && reason != "" {
		out["reason"] = reason
	}
	b, _ := json.Marshal(out)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, IsError: res["state"] != "finished"}
}

func ptr[T any](v T) *T { return &v }

func errResult(f string, a ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(f, a...)}}, IsError: true}
}

func client(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
}

// Run POSTs /v1/activities on an agent's socket and returns the answer.
func Run(ctx context.Context, sock string, body map[string]any) (map[string]any, error) {
	return run(ctx, sock, body, "")
}

// HeaderDelegator names the delegating agent to the callee
// (peercred.HeaderDelegator, which this package does not import).
const HeaderDelegator = "X-Custos-Delegator"

func run(ctx context.Context, sock string, body map[string]any, delegator string) (map[string]any, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent/v1/activities", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if delegator != "" {
		req.Header.Set(HeaderDelegator, delegator)
	}
	res, err := client(sock).Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent not reachable: %w", err)
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("agent answered %d: %s", res.StatusCode, bytes.TrimSpace(rb))
	}
	var out map[string]any
	if err := json.Unmarshal(rb, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Start starts an activity without waiting (schedules).
func Start(ctx context.Context, sock string, body map[string]any) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err := Run(ctx, sock, body)
	return err
}

// Activities reads an agent's activities (GET /v1/activities), as root.
func Activities(ctx context.Context, sock string) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://agent/v1/activities", nil)
	if err != nil {
		return nil, err
	}
	res, err := client(sock).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("agent answered %d", res.StatusCode)
	}
	var out struct {
		Activities []map[string]any `json:"activities"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out.Activities, nil
}

// Post sends an empty POST to an agent's control API (pause, resume).
func Post(ctx context.Context, sock, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent"+path, nil)
	if err != nil {
		return err
	}
	res, err := client(sock).Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("agent answered %d", res.StatusCode)
	}
	return nil
}
