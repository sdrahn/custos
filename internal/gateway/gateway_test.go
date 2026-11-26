package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseNotice(t *testing.T) {
	for _, tc := range []struct {
		data   any
		logger string
		ok     bool
		want   Notice
	}{
		{"Waiting for approval of zypp/install_patches on the mcp-gateway approvals page (id a-1f2e).", "mcp-gateway", true,
			Notice{Server: "zypp", Tool: "install_patches", Where: "on the mcp-gateway approvals page", ApprovalID: "a-1f2e"}},
		{"Waiting for approval of fs/write_file at https://host:9090/cockpit/@localhost/mcp-gateway/index.html#/approvals/a-9 (id a-9).", "mcp-gateway", true,
			Notice{Server: "fs", Tool: "write_file", Where: "https://host:9090/cockpit/@localhost/mcp-gateway/index.html#/approvals/a-9", ApprovalID: "a-9"}},
		{"Waiting for approval of zypp/install on the page (id a-1).", "zypp", false, Notice{}},
		{"something else", "mcp-gateway", false, Notice{}},
		{map[string]any{"x": 1}, "mcp-gateway", false, Notice{}},
	} {
		got, ok := ParseNotice(&mcp.LoggingMessageParams{Logger: tc.logger, Data: tc.data, Level: "notice"})
		if ok != tc.ok || got != tc.want {
			t.Errorf("%v: got %+v %v, want %+v %v", tc.data, got, ok, tc.want, tc.ok)
		}
	}
	if n := (Notice{Server: "zypp", Tool: "install"}); n.ToolName() != "zypp__install" {
		t.Error(n.ToolName())
	}
}

// The notice's _meta names the call (mcp-gateway's MetaApproval), for
// the approval the text names.
func TestParseNoticeNamesCall(t *testing.T) {
	text := "Waiting for approval of zypp/install on the mcp-gateway approvals page (id a-2)."
	n, ok := ParseNotice(&mcp.LoggingMessageParams{Logger: "mcp-gateway", Data: text,
		Meta: mcp.Meta{MetaApproval: map[string]any{"id": "a-2", "requestId": 7.0, "progressToken": "custos:e-1/c-2"}}})
	if !ok || n.ApprovalID != "a-2" || n.ProgressToken != "custos:e-1/c-2" {
		t.Errorf("got %+v %v", n, ok)
	}
	// Another approval's _meta is not this one's.
	n, _ = ParseNotice(&mcp.LoggingMessageParams{Logger: "mcp-gateway", Data: text,
		Meta: mcp.Meta{MetaApproval: map[string]any{"id": "a-3", "progressToken": "custos:e-1/c-3"}}})
	if n.ProgressToken != "" {
		t.Errorf("took another approval's call: %+v", n)
	}
}

// adkCtx stands in for ADK's tool.Context.
type adkCtx struct {
	context.Context
	inv, call string
}

func (c adkCtx) InvocationID() string   { return c.inv }
func (c adkCtx) FunctionCallID() string { return c.call }

// A tools/call from an ADK tool carries its call's progress token; one
// from elsewhere carries none.
func TestCallsCarryToken(t *testing.T) {
	ctx := context.Background()
	got := make(chan any, 2)
	srv := mcp.NewServer(&mcp.Implementation{Name: "gw"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "probe"}, func(_ context.Context, r *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		got <- r.Params.GetProgressToken()
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	s, err := NewClient("pp", "test", nil, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CallTool(adkCtx{ctx, "e-1", "c-9"}, &mcp.CallToolParams{Name: "probe"}); err != nil {
		t.Fatal(err)
	}
	if tok := <-got; tok != CallToken("e-1", "c-9") {
		t.Errorf("token %v, want %s", tok, CallToken("e-1", "c-9"))
	}
	if _, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "probe"}); err != nil {
		t.Fatal(err)
	}
	if tok := <-got; tok != nil {
		t.Errorf("a call from outside ADK got token %v", tok)
	}
}

// With propose, a call from an ADK tool carries its proposal next to its
// progress token, cleaned; none when propose has none.
func TestCallsCarryProposal(t *testing.T) {
	ctx := context.Background()
	got := make(chan mcp.Meta, 2)
	srv := mcp.NewServer(&mcp.Implementation{Name: "gw"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "probe"}, func(_ context.Context, r *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		got <- r.Params.GetMeta()
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	propose := func(inv, call string) *Proposal {
		if call != "c-1" {
			return nil
		}
		return &Proposal{Summary: "Installing\n2 patches\x1b[31m", Context: map[string]string{"activity": inv}}
	}
	s, err := NewClient("pp", "test", nil, propose).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.CallTool(adkCtx{ctx, "e-1", "c-1"}, &mcp.CallToolParams{Name: "probe"})
	m := <-got
	b, _ := json.Marshal(m[ProposalKey])
	if m["progressToken"] != CallToken("e-1", "c-1") || string(b) != `{"context":{"activity":"e-1"},"summary":"Installing 2 patches[31m"}` {
		t.Errorf("meta %v (%s)", m, b)
	}
	s.CallTool(adkCtx{ctx, "e-1", "c-2"}, &mcp.CallToolParams{Name: "probe"})
	if m := <-got; m[ProposalKey] != nil {
		t.Errorf("a call without a proposal sent %v", m[ProposalKey])
	}
}

// Clean keeps a proposal within what mcp-gateway takes (proposal.Parse):
// plain text, one line where one is asked, its limits.
func TestProposalClean(t *testing.T) {
	long := strings.Repeat("ä", 400)
	p := Proposal{
		Summary:  "  \u202eevil\u202c reason\r\nnext  ",
		Impact:   "line one\nline two\x07",
		Steps:    []string{"", "a\tb"},
		Warnings: append([]string{long}, make([]string, 12)...),
		Context:  map[string]string{"": "x", "k\n": "v\u2028w"},
	}
	for i := range p.Warnings[1:] {
		p.Warnings[i+1] = "w"
	}
	c := p.Clean()
	if c.Summary != "evil reason next" || c.Impact != "line one\nline two" || len(c.Steps) != 1 || c.Steps[0] != "a b" ||
		len(c.Warnings) != 10 || len(c.Warnings[0]) > 300 || !strings.HasSuffix(c.Warnings[0], "…") || !utf8.ValidString(c.Warnings[0]) ||
		len(c.Context) != 1 || c.Context["k"] != "vw" {
		t.Errorf("%+v", c)
	}
	if e := (Proposal{Summary: "\x00 \u2066"}).Clean(); e.Summary == "" {
		t.Error("a summary is required")
	}
	// Big: under 4 KiB as JSON.
	big := Proposal{Summary: long, Impact: strings.Repeat("x", 2000)}
	for i := 0; i < 10; i++ {
		big.Steps = append(big.Steps, long)
		big.Warnings = append(big.Warnings, long)
	}
	if b, _ := json.Marshal(big.Clean()); len(b) > 4096 {
		t.Errorf("%d bytes", len(b))
	}
}

// A call from an ADK tool carries its idempotency key (mcp-gateway 1.17.2,
// R8), the same for the same call; a call from outside ADK none.
func TestCallsCarryKey(t *testing.T) {
	ctx := context.Background()
	got := make(chan any, 2)
	srv := mcp.NewServer(&mcp.Implementation{Name: "gw"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "probe"}, func(_ context.Context, r *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		got <- r.Params.Meta[MetaIdempotencyKey]
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	s, err := NewClient("pp", "test", nil, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CallTool(adkCtx{ctx, "e-1", "c-9"}, &mcp.CallToolParams{Name: "probe"}); err != nil {
		t.Fatal(err)
	}
	if k := <-got; k != CallToken("e-1", "c-9") {
		t.Errorf("key %v, want %s", k, CallToken("e-1", "c-9"))
	}
	if _, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "probe"}); err != nil {
		t.Fatal(err)
	}
	if k := <-got; k != nil {
		t.Errorf("a call from outside ADK got key %v", k)
	}
}

// A key the gateway would refuse (over 128 characters, a space) becomes a
// digest, the same for the same call.
func TestCallKey(t *testing.T) {
	if k := callKey("e-1", "c-9"); k != "custos:e-1/c-9" {
		t.Errorf("plain: %q", k)
	}
	long := strings.Repeat("x", 200)
	for _, id := range []string{long, "call 9", "c-é"} {
		k := callKey("e-1", id)
		if len(k) > 128 || strings.ContainsAny(k, " é") || !strings.HasPrefix(k, "custos:") || k != callKey("e-1", id) {
			t.Errorf("%q: key %q", id, k)
		}
	}
	if callKey("e-1", long) == callKey("e-2", long) {
		t.Error("two calls, one key")
	}
}
