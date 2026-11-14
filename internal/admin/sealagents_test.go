package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/host"
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
	// agent-admin does not build on it (security review 0.6, N1): a
	// change is refused until it is accepted, which signs it as it is.
	if err := a.SetMode("patch-pilot", "supervised"); err == nil || !strings.Contains(err.Error(), "seal --agents --accept patch-pilot") {
		t.Errorf("a change of data that does not verify: %v", err)
	}
	if err := a.SealAgents(nil); err == nil || !strings.Contains(err.Error(), "patch-pilot") {
		t.Errorf("seal --agents without --accept: %v", err)
	}
	if err := a.SealAgents([]string{"patch-pilot"}); err != nil {
		t.Fatal(err)
	}
	p, _ = a.Show()
	if tm := p.Agents["patch-pilot"].Tampered; tm != "" {
		t.Errorf("accepted: %q", tm)
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
	got, _ := a.AgentIssuers()
	if len(got) != 1 || got[0] != iss {
		t.Fatalf("issuers %v", got)
	}
	b, _ := os.ReadFile(a.Path(GatewayHTTPIssuers))
	if !strings.Contains(string(b), "signature: ") {
		t.Fatalf("not signed:\n%s", b)
	}
	p, _ := a.Show()
	if p.IssuersTampered != "" {
		t.Errorf("show: %q", p.IssuersTampered)
	}
	os.WriteFile(a.Path(GatewayHTTPIssuers), []byte(strings.Replace(string(b), iss, "https://evil.example.com", 1)), 0o644)
	p, _ = a.Show()
	if !strings.Contains(p.IssuersTampered, "trusts no issuer") {
		t.Errorf("a hand edit: %q", p.IssuersTampered)
	}
	if err := a.Issuers(nil, []string{"https://evil.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.AgentIssuers(); len(got) != 0 {
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
	if err := a.SealAgents(nil); err == nil || !strings.Contains(err.Error(), "no seal") {
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
	// The file was written while the seal's agent part was gone, and
	// agent-admin sealed agent data here before: it is signed only when
	// accepted (security review 0.6, N1).
	if err := a.SealAgents(nil); err == nil || !strings.Contains(err.Error(), "patch-pilot") {
		t.Errorf("seal --agents without --accept after the agent part was removed: %v", err)
	}
	if err := a.SealAgents([]string{"patch-pilot"}); err != nil {
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
	// A missing key is an error, never a new key: for a change that gives
	// the agent more, and for the categories' state (security review
	// 0.6, N2); a change that only takes from it is written unsigned
	// (N3): the gateway denies the agent everything.
	os.Remove(a.Path(filepath.Join(CredstoreDir, SealCredential)))
	if err := a.SetOwners("patch-pilot", []string{"user:alice"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("owners without the key: %v", err)
	}
	if err := a.Enable("alice", []string{"patch-errata"}, false); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("enable without the key: %v", err)
	}
	if _, err := os.Stat(a.Path(filepath.Join(CredstoreDir, SealCredential))); err == nil {
		t.Error("a new key was made")
	}
	if err := a.SetMode("patch-pilot", "advisory"); err != nil {
		t.Errorf("a lower mode without the key: %v", err)
	}
	b, _ = os.ReadFile(a.gatewayFile("patch-pilot"))
	if strings.Contains(string(b), "signature: ") || !strings.Contains(string(b), "mode: advisory") {
		t.Errorf("a lower mode without the key is written unsigned:\n%s", b)
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

// A pause or a demotion of an agent whose data does not verify is still
// written, unsigned, so that the gateway denies it everything (security
// review 0.6, N1, N3); adding an issuer to an _http.yaml that does not
// verify is refused, removing one is written unsigned.
func TestReducingWrites(t *testing.T) {
	a, mem := diagnosticsHost(t)
	a.JournalJSON = dataJournal(mem)
	file := a.gatewayFile("patch-pilot")
	b, _ := os.ReadFile(file)
	os.WriteFile(file, []byte(strings.Replace(string(b), "mode: supervised", "mode: bounded", 1)), 0o644)
	var out bytes.Buffer
	a.Out = &out
	if err := a.Pause("patch-pilot", "alice", true); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(file)
	if strings.Contains(string(b), "signature: ") || !strings.Contains(string(b), "paused: true") || !strings.Contains(out.String(), "written unsigned") {
		t.Errorf("a pause of data that does not verify:\n%s\n%s", b, out.String())
	}
	if err := a.Pause("patch-pilot", "alice", false); err == nil {
		t.Error("a resume of data that does not verify was written")
	}

	const iss = "https://idp.example.com/realms/mcp"
	if err := a.Issuers([]string{iss, "https://two.example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	h, _ := os.ReadFile(a.Path(GatewayHTTPIssuers))
	os.WriteFile(a.Path(GatewayHTTPIssuers), []byte(strings.Replace(string(h), iss, "https://evil.example.com", 1)), 0o644)
	if err := a.Issuers([]string{"https://three.example.com"}, nil); err == nil || !strings.Contains(err.Error(), "--accept _http") {
		t.Errorf("adding to issuers that do not verify: %v", err)
	}
	if err := a.Issuers(nil, []string{"https://two.example.com"}); err != nil {
		t.Errorf("removing from issuers that do not verify: %v", err)
	}
	if h, _ = os.ReadFile(a.Path(GatewayHTTPIssuers)); strings.Contains(string(h), "signature: ") {
		t.Errorf("written signed:\n%s", h)
	}
	if got, why := a.TrustedIssuers(); got != nil || !strings.Contains(why, "does not match its seal") {
		t.Errorf("trusted issuers %v %q", got, why)
	}
}

// Freshness (security review 0.6, N4): each signature names its record,
// time and host; show reports a signed file that is not the latest record,
// and one signed on another host.
func TestSealFreshness(t *testing.T) {
	a, mem := diagnosticsHost(t)
	a.JournalJSON = dataJournal(mem)
	host.WriteFile(a.Path("/etc/machine-id"), []byte("aaaa\n"), 0o444)
	if err := a.SetMode("patch-pilot", "supervised"); err != nil {
		t.Fatal(err)
	}
	pub, _, _ := a.sealInfo()
	b, _ := os.ReadFile(a.gatewayFile("patch-pilot"))
	got, ok := verifyEntry(pub, "patch-pilot", b)
	if !ok || got["host"] != "aaaa" || !strings.HasPrefix(fmt.Sprint(got["record"]), "d-") || got["at"] == nil {
		t.Fatalf("payload %v %v", got, ok)
	}
	if recs := records(mem, journal.GatewayData); recs[len(recs)-1].Fields["DATA_RECORD"] != got["record"] {
		t.Errorf("the record is not journaled: %+v", recs[len(recs)-1].Fields)
	}
	host.WriteFile(a.Path("/etc/machine-id"), []byte("bbbb\n"), 0o444)
	p, _ := a.Show()
	if tm := p.Agents["patch-pilot"].Tampered; !strings.Contains(tm, "signed on another host (machine-id aaaa)") {
		t.Errorf("another host: %q", tm)
	}
}

// seal --rekey (security review 0.6, N2): a new key, with which it signs
// again what verifies against the seal so far, and what is accepted;
// nothing changes when something else is in the way.
func TestRekey(t *testing.T) {
	a, mem := diagnosticsHost(t)
	a.JournalJSON = dataJournal(mem)
	old, _, _ := a.sealInfo()
	os.Remove(a.Path(filepath.Join(CredstoreDir, SealCredential)))
	file := a.gatewayFile("patch-pilot")
	b, _ := os.ReadFile(file)
	os.WriteFile(file, []byte(strings.Replace(string(b), "mode: supervised", "mode: bounded", 1)), 0o644)
	if err := a.Rekey("root", nil); err == nil || !strings.Contains(err.Error(), "patch-pilot") {
		t.Errorf("rekey with data that does not verify: %v", err)
	}
	if pub, _, _ := a.sealInfo(); pub != old {
		t.Fatal("the seal changed though the rekey was refused")
	}
	os.WriteFile(file, b, 0o644) // put back as agent-admin wrote it
	if err := a.Rekey("root", nil); err != nil {
		t.Fatal(err)
	}
	pub, agents, _ := a.sealInfo()
	if pub == old || !agents {
		t.Fatalf("no new seal: agents %v", agents)
	}
	if !a.entryVerified(pub, "patch-pilot") || !a.entryVerified(pub, HTTPIssuersName) {
		t.Error("not signed with the new key")
	}
	e, _ := a.GetEnablement()
	if !verifySeal(pub, e) {
		t.Error("the categories' state is not signed with the new key")
	}
	p, _ := a.Show()
	if p.Enablement.Tampered != "" || p.Agents["patch-pilot"].Tampered != "" {
		t.Errorf("show after the rekey: %q %q", p.Enablement.Tampered, p.Agents["patch-pilot"].Tampered)
	}
	if err := a.Enable("alice", []string{"patch-errata"}, false); err != nil {
		t.Errorf("enable with the new key: %v", err)
	}
}
