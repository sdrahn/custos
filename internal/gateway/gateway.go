// Package gateway connects an agent to mcp-gateway's aggregated MCP
// endpoint on its unix socket (CONTRACT.md §6) and recognises the
// gateway's approval notices (§7).
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Transport dials the gateway's socket afresh for every connection, so
// ADK's MCP toolset can reconnect after a gateway restart.
type Transport struct {
	Socket string
}

func (t *Transport) Connect(ctx context.Context) (mcp.Connection, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", t.Socket)
	if err != nil {
		return nil, fmt.Errorf("mcp-gateway: %w", err)
	}
	return (&mcp.IOTransport{Reader: c, Writer: c}).Connect(ctx)
}

// HTTPTransport connects to mcp-gateway's Streamable HTTP endpoint. The
// bearer token is read from the environment variable tokenEnv for every
// request: under OpenShell it holds a placeholder the supervisor
// replaces on the way out (CONTRACT.md §3a).
func HTTPTransport(endpoint, tokenEnv string, base http.RoundTripper) mcp.Transport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearer{env: tokenEnv, next: base, fresh: freshConns(base)}},
	}
}

// freshConns is base without keep-alive: each request on a connection of
// its own, closed after it. A tools/call goes on one (issue #375):
// OpenShell closes a sandbox's open connections at a token renewal, and
// when another sandbox is set up, and a call sent on an idle connection
// it closed ends in EOF, which may not be retried (the call may have
// reached the gateway). A transport of the caller's that is no
// *http.Transport (tests) stays as it is.
func freshConns(base http.RoundTripper) http.RoundTripper {
	t, ok := base.(*http.Transport)
	if !ok {
		return base
	}
	c := t.Clone()
	c.DisableKeepAlives = true
	return c
}

type bearer struct {
	env string
	// next sends every request but tools/call, fresh those (freshConns).
	next, fresh http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.env != "" {
		if tok := os.Getenv(b.env); tok != "" {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	next := b.next
	if b.fresh != nil && rpcMethod(r) == "tools/call" {
		next = b.fresh
	}
	res, err := next.RoundTrip(r)
	// OpenShell's proxy answers 500, without asking the gateway, for a
	// little while after it renewed the token (until the sandbox's
	// supervisor has the new one). mcp-gateway answers 500 only to an
	// initialize it cannot serve. So a 500 is retried with backoff, while
	// the request can be sent again and the caller still waits. Around a
	// renewal the sandbox may also refuse the connection for a moment
	// (connect: permission denied): nothing was sent, so any request is
	// retried. And the proxy may close the connection without an answer
	// (EOF): that is retried only for a request that may run twice
	// (never a tools/call, which may have reached the gateway).
	for wait := retryFirst; retryable(r, res, err) && wait <= retryLast; wait *= 2 {
		if err == nil {
			drain(res)
		}
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-time.After(wait):
		}
		again := r.Clone(r.Context())
		if r.GetBody != nil {
			if again.Body, err = r.GetBody(); err != nil {
				return nil, err
			}
		}
		res, err = next.RoundTrip(again)
	}
	if err == nil && res.StatusCode == http.StatusInternalServerError && r.Header.Get(sessionHeader) != "" {
		// Still 500 on a session: as "session not found" (404) the MCP
		// client gives up the session, and the toolset opens a new one and
		// retries the request once.
		drain(res)
		res = &http.Response{
			Status: "404 Not Found", StatusCode: http.StatusNotFound, Proto: res.Proto, ProtoMajor: res.ProtoMajor, ProtoMinor: res.ProtoMinor,
			Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader("session lost on the way (500)")), Request: r,
		}
	}
	return res, err
}

// retryable: a 500, a connection that could not be made, or one dropped
// without an answer to a request that is safe to send again; and the
// request can be sent again.
func retryable(r *http.Request, res *http.Response, err error) bool {
	if r.Body != nil && r.GetBody == nil {
		return false
	}
	if err == nil {
		return res.StatusCode == http.StatusInternalServerError
	}
	return notConnected(err) || (dropped(err) && idempotent(r))
}

// notConnected: the connection could not be made, so nothing was sent.
func notConnected(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// dropped: the connection ended without an answer.
func dropped(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)
}

// idempotent: a request whose repetition changes nothing: the session's
// stream (GET), and the JSON-RPC methods that only read or set up a
// session. A tools/call, a batch or anything unknown is not.
func idempotent(r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	switch rpcMethod(r) {
	case "initialize", "notifications/initialized", "ping", "tools/list", "prompts/list", "resources/list", "resources/templates/list":
		return true
	}
	return false
}

// rpcMethod is the JSON-RPC method a POST carries, "" for anything else
// (another method, a batch, a body that cannot be read again).
func rpcMethod(r *http.Request) string {
	if r.Method != http.MethodPost || r.GetBody == nil {
		return ""
	}
	body, err := r.GetBody()
	if err != nil {
		return ""
	}
	defer body.Close()
	var msg struct {
		Method string `json:"method"`
	}
	if json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&msg) != nil {
		return ""
	}
	return msg.Method
}

// The backoff of retried 500s: 0.5 s doubling to 8 s, about 15 s in all
// (OpenShell's supervisor syncs provider credentials within about 10 s).
var retryFirst, retryLast = 500 * time.Millisecond, 8 * time.Second

func drain(res *http.Response) {
	io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
}

// sessionHeader is MCP's Streamable HTTP session header.
const sessionHeader = "Mcp-Session-Id"

// Notice is an approval notice of the gateway's broker.
type Notice struct {
	Server, Tool, ApprovalID string
	// Where is the place named by the gateway: a URL or a description.
	Where string
	// ProgressToken is the progress token of the call the notice is for,
	// as the gateway names it in the notice's _meta (MetaApproval); empty
	// from a gateway that names only the tool.
	ProgressToken string
}

// MetaApproval is the _meta key of mcp-gateway's approval notice: the
// approval id ("id") and the call it is for ("requestId",
// "progressToken"; CONTRACT.md §7).
const MetaApproval = "io.github.sdrahn.mcp-gateway/approval"

// CallToken is the progress token the agent's tools/call carries for
// ADK's call callID of invocation (NewClient), by which the gateway's
// approval notice names the call.
func CallToken(invocation, callID string) string { return "custos:" + invocation + "/" + callID }

// ToolName is the tool's name on the aggregated endpoint.
func (n Notice) ToolName() string { return n.Server + "__" + n.Tool }

// The text of mcp-gateway's broker (internal/broker, notifyOOB):
// "Waiting for approval of <server>/<tool> <where> (id <id>)."
var noticeRE = regexp.MustCompile(`^Waiting for approval of ([^/\s]+)/(\S+) (.*) \(id ([A-Za-z0-9._:-]+)\)\.$`)

// ParseNotice recognises an approval notice in a logging message.
func ParseNotice(p *mcp.LoggingMessageParams) (Notice, bool) {
	if p == nil || p.Logger != "mcp-gateway" {
		return Notice{}, false
	}
	s, ok := p.Data.(string)
	if !ok {
		return Notice{}, false
	}
	m := noticeRE.FindStringSubmatch(s)
	if m == nil {
		return Notice{}, false
	}
	where := m[3]
	if u, ok := cutPrefix(where, "at "); ok {
		where = u
	}
	n := Notice{Server: m[1], Tool: m[2], Where: where, ApprovalID: m[4]}
	// The call, if the gateway names it, for the same approval as the text.
	if a, ok := p.Meta[MetaApproval].(map[string]any); ok && a["id"] == n.ApprovalID {
		n.ProgressToken, _ = a["progressToken"].(string)
	}
	return n, true
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return s, false
}

// NewClient returns the agent's MCP client. It declares no elicitation
// capability (no ElicitationHandler), so the gateway never asks the
// agent to approve its own call (§6). Logging messages go to onLog.
//
// Every tools/call made from an ADK tool carries the call's progress
// token (CallToken), so that the gateway's approval notice names the
// call it is for, not only the tool, and the agent's account of the call
// (propose, if not nil; ProposalKey), which the gateway shows with the
// approval.
func NewClient(agentName, version string, onLog func(context.Context, *mcp.LoggingMessageParams), propose func(invocation, callID string) *Proposal) *mcp.Client {
	c := mcp.NewClient(&mcp.Implementation{Name: "custos/" + agentName, Version: version}, &mcp.ClientOptions{
		LoggingMessageHandler: func(ctx context.Context, r *mcp.LoggingMessageRequest) {
			if onLog != nil {
				onLog(ctx, r.Params)
			}
		},
	})
	c.AddSendingMiddleware(tagCalls(propose))
	return c
}

// adkCall is what ADK's tool context (tool.Context, the context ADK's MCP
// toolset calls with) tells about the call.
type adkCall interface {
	InvocationID() string
	FunctionCallID() string
}

// tagCalls sets a tools/call's progress token to its ADK call's
// CallToken, unless it has one, and its proposal (ProposalKey) to what
// propose gives for the call.
func tagCalls(propose func(invocation, callID string) *Proposal) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if ids, ok := ctx.(adkCall); ok && ids.FunctionCallID() != "" {
					if p, ok := req.GetParams().(*mcp.CallToolParams); ok {
						// go-sdk's SetProgressToken drops a _meta it creates.
						if p.Meta == nil {
							p.Meta = mcp.Meta{}
						}
						if p.GetProgressToken() == nil {
							p.SetProgressToken(CallToken(ids.InvocationID(), ids.FunctionCallID()))
						}
						if propose != nil {
							if pr := propose(ids.InvocationID(), ids.FunctionCallID()); pr != nil {
								p.Meta[ProposalKey] = pr.Clean()
							}
						}
					}
				}
			}
			return next(ctx, method, req)
		}
	}
}
