package admin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/journal"
)

// dataJournal is the gateway.data records in mem as journalctl gives
// them: root's, with the AGENT_ prefix.
func dataJournal(mem *journal.Memory) func(...string) ([]byte, error) {
	return func(args ...string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), "AGENT_EVENT="+journal.GatewayData) {
			return nil, errNoJournal
		}
		var out []string
		for _, r := range records(mem, journal.GatewayData) {
			b, _ := json.Marshal(map[string]string{"_UID": "0", "_COMM": "agent-admin",
				"AGENT_DATA": r.Fields["DATA"], "AGENT_DATA_SHA256": r.Fields["DATA_SHA256"]})
			out = append(out, string(b))
		}
		return []byte(strings.Join(out, "\n")), nil
	}
}

// A new seal covers the agents' data and their token issuers from the
// start (#158): agent-admin signs each agent's data as it writes it, and
// show reports what the gateway enforces when it does not match.
func TestSealAgentData(t *testing.T) {
	a, mem := diagnosticsHost(t)
	a.JournalJSON = dataJournal(mem)
	pub, agents, err := a.sealInfo()
	if err != nil || pub == "" || !agents {
		t.Fatalf("the new seal does not cover agent data: %v %v", agents, err)
	}
	file := a.gatewayFile("patch-pilot")
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), "signature: ") || !strings.Contains(string(b), "signed with the host's seal") {
		t.Fatalf("not signed:\n%s", b)
	}
	if len(records(mem, journal.GatewayData)) == 0 {
		t.Error("no gateway.data record")
	}
	// The payload is what the gateway reads: the entry without its
	// signature, under the agent's name.
	var doc map[string]map[string]any
	if err := yamlUnmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	pb, ok := verifiedPayload(pub, doc["patch-pilot"]["signature"].(string))
	if !ok {
		t.Fatal("the signature does not verify")
	}
	var payload map[string]any
	json.Unmarshal(pb, &payload)
	if payload["agent"] != "patch-pilot" || payload["data"].(map[string]any)["mode"] != "supervised" || payload["data"].(map[string]any)["signature"] != nil {
		t.Errorf("payload %v", payload)
	}
	p, err := a.Show()
	if err != nil || !p.AgentsSealed || p.Agents["patch-pilot"].Tampered != "" || p.IssuersTampered != "" {
		t.Fatalf("show: %+v %v", p, err)
	}

	// A hand edit: the gateway denies the agent everything.
	os.WriteFile(file, []byte(strings.Replace(string(b), "mode: supervised", "mode: bounded", 1)), 0o644)
	p, _ = a.Show()
	if tm := p.Agents["patch-pilot"].Tampered; !strings.Contains(tm, "does not match its seal") || !strings.Contains(tm, "denies the agent everything") {
		t.Errorf("a hand edit: %q", tm)
	}
	// So does a file without a signature.
	os.WriteFile(file, []byte("patch-pilot:\n    mode: supervised\n    permissions: []\n"), 0o644)
	p, _ = a.Show()
	if tm := p.Agents["patch-pilot"].Tampered; !strings.Contains(tm, "never signed") {
		t.Errorf("unsigned: %q", tm)
	}
	// agent-admin writing it again restores it.
	if err := a.SetMode("patch-pilot", "supervised"); err != nil {
		t.Fatal(err)
	}
	p, _ = a.Show()
	if tm := p.Agents["patch-pilot"].Tampered; tm != "" {
		t.Errorf("written again: %q", tm)
	}

	// Another agent's signed data under this name does not count.
	b, _ = os.ReadFile(file)
	os.WriteFile(a.gatewayFile("other"), []byte(strings.Replace(string(b), "patch-pilot:", "other:", 1)), 0o644)
	p, _ = a.Show()
	if tm := p.Agents["other"].Tampered; !strings.Contains(tm, "does not match its seal") {
		t.Errorf("another agent's signature: %q", tm)
	}
	os.Remove(a.gatewayFile("other"))

	// An earlier signed file put back verifies, and is reported.
	before, _ := os.ReadFile(file)
	if err := a.SetMode("patch-pilot", "advisory"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(file, before, 0o644)
	p, _ = a.Show()
	if tm := p.Agents["patch-pilot"].Tampered; !strings.Contains(tm, "an earlier version was put back") {
		t.Errorf("an earlier version: %q", tm)
	}
}

// The agents' token issuers: agent-admin issuers writes them, signed on
// a host whose seal covers agent data; a hand edit is reported.
func TestIssuers(t *testing.T) {
	a, mem := diagnosticsHost(t)
	a.JournalJSON = dataJournal(mem)
	const iss = "https://idp.example.com/realms/mcp"
	if err := a.Issuers([]string{iss}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := a.agentIssuers()
	if len(got) != 1 || got[0] != iss {
		t.Fatalf("issuers %v", got)
	}
	b, _ := os.ReadFile(a.p(GatewayHTTPIssuers))
	if !strings.Contains(string(b), "signature: ") {
		t.Fatalf("not signed:\n%s", b)
	}
	p, _ := a.Show()
	if p.IssuersTampered != "" {
		t.Errorf("show: %q", p.IssuersTampered)
	}
	os.WriteFile(a.p(GatewayHTTPIssuers), []byte(strings.Replace(string(b), iss, "https://evil.example.com", 1)), 0o644)
	p, _ = a.Show()
	if !strings.Contains(p.IssuersTampered, "trusts no issuer") {
		t.Errorf("a hand edit: %q", p.IssuersTampered)
	}
	if err := a.Issuers(nil, []string{"https://evil.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.agentIssuers(); len(got) != 0 {
		t.Errorf("removed: %v", got)
	}
	for _, bad := range []string{"http://idp", "https://a b"} {
		if err := a.Issuers([]string{bad}, nil); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := a.Issuers(nil, []string{"https://nobody.example.com"}); err == nil {
		t.Error("removing an issuer that is not listed")
	}
}

// A host sealed before #158 (the seal covers only the enablement state):
// agent data stays unsigned until agent-admin seal --agents; a host
// without a seal is refused.
func TestSealAgentsLater(t *testing.T) {
	a, _, _ := withCategoriesRec(t)
	if err := a.SealAgents(); err == nil || !strings.Contains(err.Error(), "no seal") {
		t.Errorf("without a seal: %v", err)
	}
	a, mem := diagnosticsHost(t)
	a.JournalJSON = dataJournal(mem)
	pub, _, _ := a.sealInfo()
	if err := a.writeSealFile(sealFile{PublicKey: pub}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetMode("patch-pilot", "supervised"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.gatewayFile("patch-pilot"))
	if strings.Contains(string(b), "signature: ") {
		t.Fatalf("signed though the seal does not cover agent data:\n%s", b)
	}
	p, _ := a.Show()
	if p.AgentsSealed || p.Agents["patch-pilot"].Tampered != "" {
		t.Errorf("show before: %+v", p)
	}
	// agent-admin sealed agent data here before (diagnosticsHost's new
	// seal did): its removal is reported.
	if !strings.Contains(p.SealTampered, "no longer covers the agents' data") {
		t.Errorf("the seal's agent part removed: %q", p.SealTampered)
	}
	var out bytes.Buffer
	a.Out = &out
	if err := a.SealAgents(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "the seal covers the agents' data now: 1 agent and the token issuers signed") {
		t.Errorf("output %q", out.String())
	}
	if _, agents, _ := a.sealInfo(); !agents {
		t.Error("the seal does not cover agent data")
	}
	p, _ = a.Show()
	if !p.AgentsSealed || p.Agents["patch-pilot"].Tampered != "" || p.SealTampered != "" {
		t.Errorf("show after: %+v %q", p.Agents["patch-pilot"], p.SealTampered)
	}
	// A missing key is an error, never a new key.
	os.Remove(a.p(filepath.Join(CredstoreDir, SealCredential)))
	if err := a.SetMode("patch-pilot", "advisory"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("without the key: %v", err)
	}
}

func yamlUnmarshal(b []byte, v any) error { return yaml.Unmarshal(b, v) }

func TestCountAgents(t *testing.T) {
	for n, want := range map[int]string{0: "no agents", 1: "1 agent", 2: "2 agents", 12: "12 agents"} {
		if got := countAgents(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}
