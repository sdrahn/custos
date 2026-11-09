package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Gateway is a fake mcp-gateway aggregated endpoint on a unix socket: raw
// newline-delimited JSON-RPC like the real one, which sends its approval
// notice whatever log level the client set.
type Gateway struct {
	Socket string
	// Ask names tools whose calls wait for Decide, after the notice.
	Ask map[string]string // tool -> approval id
	// Tools lists the tools offered.
	Tools []string
	// Block names tools whose calls never return (for cancel tests).
	Block map[string]bool
	// Fail names tools whose calls return their output as an error
	// (isError), as the exec server does for a non-zero exit.
	Fail map[string]bool
	// Legacy: approval notices name only the tool (mcp-gateway before
	// sdrahn/mcp-gateway#333), no call in _meta.
	Legacy bool

	mu        sync.Mutex
	decisions map[string]chan bool
	Executed  []string // tools actually run, in order
	Initializ []map[string]any
	tokens    []string
	// callTokens are the calls' progress tokens, in order; proposals the
	// calls' proposals (mcp-gateway's R5), by progress token.
	callTokens []string
	proposals  map[string]json.RawMessage
}

// StartGateway listens on dir/mcp.sock.
func StartGateway(t *testing.T, dir string, tools []string) *Gateway {
	g := &Gateway{Socket: dir + "/mcp.sock", Tools: tools, Ask: map[string]string{}, Block: map[string]bool{}, Fail: map[string]bool{}, decisions: map[string]chan bool{}}
	l, err := net.Listen("unix", g.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go g.serve(c)
		}
	}()
	return g
}

// Decide approves or declines approval id.
func (g *Gateway) Decide(id string, approve bool) {
	g.mu.Lock()
	ch := g.decisions[id]
	if ch == nil {
		ch = make(chan bool, 1)
		g.decisions[id] = ch
	}
	g.mu.Unlock()
	ch <- approve
}

func (g *Gateway) decision(id string) chan bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.decisions[id] == nil {
		g.decisions[id] = make(chan bool, 1)
	}
	return g.decisions[id]
}

// Ran returns the tools executed so far.
func (g *Gateway) Ran() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.Executed...)
}

type rpc struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
}

func (g *Gateway) serve(c net.Conn) {
	defer c.Close()
	var wmu sync.Mutex
	write := func(v any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		c.Write(append(b, '\n'))
		wmu.Unlock()
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var m rpc
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil {
			continue // notifications
		}
		switch m.Method {
		case "initialize":
			var p map[string]any
			json.Unmarshal(m.Params, &p)
			g.mu.Lock()
			g.Initializ = append(g.Initializ, p)
			g.mu.Unlock()
			write(rpc{JSONRPC: "2.0", ID: m.ID, Result: map[string]any{
				"protocolVersion": p["protocolVersion"], "capabilities": map[string]any{"tools": map[string]any{"listChanged": true}, "logging": map[string]any{}},
				"serverInfo": map[string]any{"name": "mcp-gateway", "version": "1.12.4"},
			}})
		case "tools/list":
			var ts []map[string]any
			for _, n := range g.Tools {
				ts = append(ts, map[string]any{"name": n, "description": "fake " + n, "inputSchema": map[string]any{"type": "object"}})
			}
			write(rpc{JSONRPC: "2.0", ID: m.ID, Result: map[string]any{"tools": ts}})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
				Meta      struct {
					ProgressToken any             `json:"progressToken"`
					Proposal      json.RawMessage `json:"io.github.sdrahn.mcp-gateway/proposal"`
				} `json:"_meta"`
			}
			json.Unmarshal(m.Params, &p)
			if tok, ok := p.Meta.ProgressToken.(string); ok && p.Meta.Proposal != nil {
				g.mu.Lock()
				if g.proposals == nil {
					g.proposals = map[string]json.RawMessage{}
				}
				g.proposals[tok] = p.Meta.Proposal
				g.mu.Unlock()
			}
			go g.call(m.ID, p.Name, p.Arguments, p.Meta.ProgressToken, write)
		default:
			write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	}
}

func (g *Gateway) call(id json.RawMessage, name string, args map[string]any, token any, write func(any)) {
	g.mu.Lock()
	if tok, ok := token.(string); ok {
		g.callTokens = append(g.callTokens, tok)
	}
	g.mu.Unlock()
	text := func(s string, isErr bool) {
		write(rpc{JSONRPC: "2.0", ID: id, Result: map[string]any{"content": []any{map[string]any{"type": "text", "text": s}}, "isError": isErr}})
	}
	if g.Block[name] {
		select {}
	}
	if aid, ok := g.Ask[name]; ok {
		srv, tool := splitTool(name)
		// As mcp-gateway does since sdrahn/mcp-gateway#333: the call it is
		// for in _meta, unless Legacy.
		params := map[string]any{
			"level": "notice", "logger": "mcp-gateway",
			"data": "Waiting for approval of " + srv + "/" + tool + " on the mcp-gateway approvals page (id " + aid + ").",
		}
		if !g.Legacy {
			a := map[string]any{"id": aid, "requestId": id}
			if token != nil {
				a["progressToken"] = token
			}
			params["_meta"] = map[string]any{"io.github.sdrahn.mcp-gateway/approval": a}
		}
		write(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": params})
		if !<-g.decision(aid) {
			text("mcp-gateway: declined by an approver", true) // as mcp-gateway words a denial
			return
		}
	}
	g.mu.Lock()
	g.Executed = append(g.Executed, name)
	g.mu.Unlock()
	b, _ := json.Marshal(args)
	text(name+" done with "+string(b), g.Fail[name])
}

func splitTool(n string) (string, string) {
	for i := 0; i+1 < len(n); i++ {
		if n[i] == '_' && n[i+1] == '_' {
			return n[:i], n[i+2:]
		}
	}
	return "", n
}

// StartHTTPGateway serves the same fake gateway over MCP Streamable HTTP
// (as mcp-gateway's listener for remote and sandboxed agents): JSON
// answers, and an SSE stream for tools/call that carries the approval
// notice before the result. It records the Authorization headers.
func StartHTTPGateway(t *testing.T, tools []string) (*Gateway, string) {
	g := NewGateway(tools)
	srv := httptest.NewServer(http.HandlerFunc(g.serveHTTP))
	t.Cleanup(srv.Close)
	return g, srv.URL + "/mcp"
}

// NewGateway returns a fake gateway.
func NewGateway(tools []string) *Gateway {
	return &Gateway{Tools: tools, Ask: map[string]string{}, Block: map[string]bool{}, Fail: map[string]bool{}, decisions: map[string]chan bool{}}
}

// Pending reports whether approval id waits for Decide.
func (g *Gateway) Pending(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.decisions[id]
	return ok
}

// CallTokens returns the progress tokens of the tools/calls, in order.
func (g *Gateway) CallTokens() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.callTokens...)
}

// Proposal is the proposal the call with the progress token sent, as JSON
// (nil: none).
func (g *Gateway) Proposal(token string) json.RawMessage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.proposals[token]
}

// Tokens returns the bearer tokens the HTTP gateway saw.
func (g *Gateway) Tokens() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.tokens...)
}

func (g *Gateway) serveHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.tokens = append(g.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	g.mu.Unlock()
	switch r.Method {
	case http.MethodPost:
	case http.MethodGet:
		http.Error(w, "no standalone stream", http.StatusMethodNotAllowed)
		return
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var m rpc
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if m.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "fake-session")
		json.NewEncoder(w).Encode(v)
	}
	switch m.Method {
	case "initialize":
		var p map[string]any
		json.Unmarshal(m.Params, &p)
		g.mu.Lock()
		g.Initializ = append(g.Initializ, p)
		g.mu.Unlock()
		reply(rpc{JSONRPC: "2.0", ID: m.ID, Result: map[string]any{
			"protocolVersion": p["protocolVersion"], "capabilities": map[string]any{"tools": map[string]any{}, "logging": map[string]any{}},
			"serverInfo": map[string]any{"name": "mcp-gateway", "version": "1.12.4"},
		}})
	case "tools/list":
		var ts []map[string]any
		for _, n := range g.Tools {
			ts = append(ts, map[string]any{"name": n, "description": "fake " + n, "inputSchema": map[string]any{"type": "object"}})
		}
		reply(rpc{JSONRPC: "2.0", ID: m.ID, Result: map[string]any{"tools": ts}})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
			Meta      struct {
				ProgressToken any `json:"progressToken"`
			} `json:"_meta"`
		}
		json.Unmarshal(m.Params, &p)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Mcp-Session-Id", "fake-session")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		n := 0
		write := func(v any) {
			b, _ := json.Marshal(v)
			n++
			fmt.Fprintf(w, "id: s-%d\nevent: message\ndata: %s\n\n", n, b)
			if fl != nil {
				fl.Flush()
			}
		}
		g.call(m.ID, p.Name, p.Arguments, p.Meta.ProgressToken, write)
	default:
		reply(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
	}
}
