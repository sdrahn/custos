package admin

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/readiness"
)

// The seal of the agents' data (CONTRACT.md §6b, #158). On a host whose
// seal covers agent data (_seal.yaml's agents: true), agent-admin signs
// each agent's gateway data (agents/<name>.yaml) and the agents' token
// issuers (_http.yaml) with the seal's key, and the gateway counts them
// only with a valid signature: an agent whose data is edited by hand,
// unsigned or signed for another agent is denied everything, and an
// _http.yaml the seal does not cover trusts no issuer. The emergency
// stop (_control.yaml) is never sealed: stopping always works.
//
// The signature is a JWS over {"agent": <name>, "data": <the data>}: the
// data as the gateway reads it, the file's top-level entry without its
// signature. "_http" is the issuers' name.

// HTTPIssuersName is _http.yaml's name in a signature.
const HTTPIssuersName = "_http"

type sealFile struct {
	PublicKey string `yaml:"public_key"`
	// Agents: the seal covers the agents' data and _http.yaml too.
	Agents bool `yaml:"agents,omitempty"`
}

// sealInfo is the seal's public key ("" without a seal) and whether it
// covers the agents' data.
func (a *Admin) sealInfo() (string, bool, error) {
	b, err := os.ReadFile(a.p(GatewaySeal))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var doc struct {
		Seal sealFile `yaml:"_seal"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", false, fmt.Errorf("%s: %w", GatewaySeal, err)
	}
	return doc.Seal.PublicKey, doc.Seal.PublicKey != "" && doc.Seal.Agents, nil
}

// writeSealFile writes _seal.yaml.
func (a *Admin) writeSealFile(s sealFile) error {
	var buf bytes.Buffer
	buf.WriteString("# The seal (custos CONTRACT.md §6b), written by agent-admin: the public key\n" +
		"# the gateway checks the signatures of _enablement.yaml and, with agents: true,\n" +
		"# of each agent's data and _http.yaml with. Edited, unsigned or deleted, the\n" +
		"# state counts as diagnostics-only, and an agent's data denies it everything.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]sealFile{"_seal": s}); err != nil {
		return err
	}
	return writeFile(a.p(GatewaySeal), buf.Bytes(), 0o644)
}

// signedEntry is the payload of an entry's signature: the entry as YAML
// (the file's top-level key name), read back as the gateway reads it,
// without its signature.
func signedEntry(name string, y []byte) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(y, &doc); err != nil {
		return nil, err
	}
	m, ok := doc[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("no entry %s", name)
	}
	data := map[string]any{}
	for k, v := range m {
		if k != "signature" {
			data[k] = v
		}
	}
	return map[string]any{"agent": name, "data": data}, nil
}

// entryDigest is the SHA-256 of a payload's JSON (sorted keys): the
// DATA_SHA256 of its gateway.data record.
func entryDigest(payload map[string]any) string {
	b, _ := json.Marshal(payload)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// encodeEntry writes the file's YAML: the head (comments), then
// {name: entry}.
func encodeEntry(head, name string, entry any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(head)
	enc := yaml.NewEncoder(&buf) // yaml.Marshal's indent, as before
	if err := enc.Encode(map[string]any{name: entry}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sealedYAML is the file with entry signed with key: the YAML without a
// signature, its payload signed, then the YAML with it. set puts the
// signature into the entry.
func sealedYAML(k *ecdsa.PrivateKey, head, name string, entry any, set func(string)) ([]byte, map[string]any, error) {
	set("")
	y, err := encodeEntry(head, name, entry)
	if err != nil {
		return nil, nil, err
	}
	payload, err := signedEntry(name, y)
	if err != nil {
		return nil, nil, err
	}
	sig, err := signJWS(k, payload)
	if err != nil {
		return nil, nil, err
	}
	set(sig)
	y, err = encodeEntry(head, name, entry)
	return y, payload, err
}

// existingSealKey is the seal's key from its credential; it never makes
// a new one (a new key would not match the seal the gateway has).
func (a *Admin) existingSealKey() (*ecdsa.PrivateKey, error) {
	if _, err := os.Stat(a.p(filepath.Join(CredstoreDir, SealCredential))); err != nil {
		return nil, fmt.Errorf("the seal's key (%s) is missing: %w", filepath.Join(CredstoreDir, SealCredential), err)
	}
	return a.sealKey()
}

const gatewayHead = "# Written by agent-admin: the autonomy mode and permissions of agent %s\n" +
	"# (Custos CONTRACT.md §6). mcp-gateway's OPA reloads it by itself.\n"

const sealedGatewayHead = "# Written by agent-admin: the autonomy mode and permissions of agent %s\n" +
	"# (Custos CONTRACT.md §6), signed with the host's seal (§6b): changed by hand,\n" +
	"# the gateway denies the agent everything until agent-admin writes it again.\n" +
	"# mcp-gateway's OPA reloads it by itself.\n"

// gatewayYAML is the agent's file: signed when the seal covers agent
// data (with the record of it to write once it is in place), else as
// before.
func (a *Admin) gatewayYAML(name string, d *GatewayData) ([]byte, func(), error) {
	d.Signature = ""
	pub, agents, err := a.sealInfo()
	if err != nil {
		return nil, nil, err
	}
	if !agents {
		y, err := encodeEntry(fmt.Sprintf(gatewayHead, name), name, d)
		return y, func() {}, err
	}
	k, err := a.existingSealKey()
	if err != nil {
		return nil, nil, err
	}
	if !matchesSeal(k, pub) {
		return nil, nil, fmt.Errorf("the seal's key (%s) does not match the seal (%s)", filepath.Join(CredstoreDir, SealCredential), GatewaySeal)
	}
	y, payload, err := sealedYAML(k, fmt.Sprintf(sealedGatewayHead, name), name, d, func(s string) { d.Signature = s })
	if err != nil {
		return nil, nil, err
	}
	return y, func() { a.recordData(name, payload) }, nil
}

// matchesSeal reports whether the key's public half is the seal's.
func matchesSeal(k *ecdsa.PrivateKey, pub string) bool {
	p, err := publicPEM(k)
	return err == nil && p == pub
}

// recordData writes the gateway.data record of a signed entry: its
// digest, which show compares with the file's (an earlier signed file
// put back).
func (a *Admin) recordData(name string, payload map[string]any) {
	what := "the data of agent " + name
	if name == HTTPIssuersName {
		what = "the agents' token issuers"
	}
	agent := name
	if name == HTTPIssuersName {
		agent = "" // no agent's: AGENT_DATA names it
	}
	a.record(journal.GatewayData, agent, fmt.Sprintf("%s signed %s", a.who(), what),
		journal.Fields{"DATA": name, "DATA_SHA256": entryDigest(payload), "BY": a.who()})
}

// agentFiles are the names of the agents with gateway data.
func (a *Admin) agentFiles() ([]string, error) {
	m, err := filepath.Glob(a.p(filepath.Join(GatewayAgents, "*.yaml")))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, f := range m {
		if n := strings.TrimSuffix(filepath.Base(f), ".yaml"); config.ValidName(n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// sealAgentsWith signs every agent's data and _http.yaml with k, then
// writes the seal covering them: the signatures first, so that the
// gateway never sees a seal over agent data that is not signed yet.
func (a *Admin) sealAgentsWith(k *ecdsa.PrivateKey) error {
	names, err := a.agentFiles()
	if err != nil {
		return err
	}
	for _, n := range names {
		d, err := a.readGateway(n)
		if err != nil {
			return err
		}
		if d.Permissions == nil {
			d.Permissions = []map[string]any{}
		}
		y, payload, err := sealedYAML(k, fmt.Sprintf(sealedGatewayHead, n), n, d, func(s string) { d.Signature = s })
		if err != nil {
			return err
		}
		if err := writeFile(a.gatewayFile(n), y, 0o644); err != nil {
			return err
		}
		a.recordData(n, payload)
	}
	issuers, err := a.agentIssuers()
	if err != nil {
		return err
	}
	if err := a.writeIssuersWith(k, issuers); err != nil {
		return err
	}
	pub, err := publicPEM(k)
	if err != nil {
		return err
	}
	return a.writeSealFile(sealFile{PublicKey: pub, Agents: true})
}

// SealAgents makes the host's seal cover the agents' data and _http.yaml
// (agent-admin seal --agents): a host sealed before this release, whose
// agents' data is not signed yet. The host needs a seal (an enablement
// state, §6b).
func (a *Admin) SealAgents() error {
	pub, agents, err := a.sealInfo()
	if err != nil {
		return err
	}
	if pub == "" {
		return errors.New("this host has no seal: the seal comes with the task categories' state " +
			"(agent-admin enable or disable, custos-setup), and covers the agents' data from then on")
	}
	k, err := a.existingSealKey()
	if err != nil {
		return err
	}
	if !matchesSeal(k, pub) {
		return fmt.Errorf("the seal's key (%s) does not match the seal (%s)", filepath.Join(CredstoreDir, SealCredential), GatewaySeal)
	}
	if err := a.sealAgentsWith(k); err != nil {
		return err
	}
	names, _ := a.agentFiles()
	if agents {
		fmt.Fprintf(a.Out, "the seal covered the agents' data already; signed again: %s and the token issuers\n", countAgents(len(names)))
	} else {
		fmt.Fprintf(a.Out, "the seal covers the agents' data now: %s and the token issuers signed; "+
			"a change outside agent-admin denies that agent everything\n", countAgents(len(names)))
	}
	return a.waitPolicy("mcp/agents/_seal/agents", func(v any) bool { return v == true })
}

// countAgents is "1 agent", "2 agents", "no agents".
func countAgents(n int) string {
	switch n {
	case 0:
		return "no agents"
	case 1:
		return "1 agent"
	}
	return fmt.Sprintf("%d agents", n)
}

// --- the agents' token issuers (_http.yaml) ---------------------------------

const issuersHead = "# The token issuers whose agent-<name> subjects are agents over the gateway's\n" +
	"# HTTPS listener (custos CONTRACT.md §6, §16a), written by agent-admin issuers.\n"

const sealedIssuersHead = issuersHead +
	"# Signed with the host's seal (§6b): changed by hand, the gateway trusts no\n" +
	"# issuer until agent-admin issuers writes it again.\n"

type httpIssuers struct {
	Issuers   []string `yaml:"issuers"`
	Signature string   `yaml:"signature,omitempty"`
}

// writeIssuersWith writes _http.yaml, signed with k when k is not nil.
func (a *Admin) writeIssuersWith(k *ecdsa.PrivateKey, issuers []string) error {
	h := &httpIssuers{Issuers: append([]string{}, issuers...)}
	if k == nil {
		y, err := encodeEntry(issuersHead, HTTPIssuersName, h)
		if err != nil {
			return err
		}
		return writeFile(a.p(GatewayHTTPIssuers), y, 0o644)
	}
	y, payload, err := sealedYAML(k, sealedIssuersHead, HTTPIssuersName, h, func(s string) { h.Signature = s })
	if err != nil {
		return err
	}
	if err := writeFile(a.p(GatewayHTTPIssuers), y, 0o644); err != nil {
		return err
	}
	a.recordData(HTTPIssuersName, payload)
	return nil
}

// Issuers changes the token issuers trusted for agents (agent-admin
// issuers --add/--remove) and prints them. With neither it only prints.
// On a host whose seal covers agent data the file is signed.
func (a *Admin) Issuers(add, remove []string) error {
	issuers, err := a.agentIssuers()
	if err != nil {
		return err
	}
	next := append([]string{}, issuers...)
	for _, i := range add {
		if !strings.HasPrefix(i, "https://") || strings.ContainsAny(i, " \t\n\"") {
			return fmt.Errorf("issuer %q: want an https:// URL, as the tokens' iss", i)
		}
		if !slices.Contains(next, i) {
			next = append(next, i)
		}
	}
	for _, i := range remove {
		j := slices.Index(next, i)
		if j < 0 {
			return fmt.Errorf("issuer %q is not listed", i)
		}
		next = slices.Delete(next, j, j+1)
	}
	if len(add)+len(remove) > 0 {
		pub, agents, err := a.sealInfo()
		if err != nil {
			return err
		}
		var k *ecdsa.PrivateKey
		if agents {
			if k, err = a.existingSealKey(); err != nil {
				return err
			}
			if !matchesSeal(k, pub) {
				return fmt.Errorf("the seal's key (%s) does not match the seal (%s)", filepath.Join(CredstoreDir, SealCredential), GatewaySeal)
			}
		}
		if err := a.writeIssuersWith(k, next); err != nil {
			return err
		}
		want := next
		if err := a.waitPolicy("mcp/agents/_http/issuers", func(v any) bool {
			got, _ := v.([]any)
			if len(got) != len(want) {
				return false
			}
			for i := range want {
				if got[i] != want[i] {
					return false
				}
			}
			return true
		}); err != nil {
			return err
		}
	}
	for _, i := range next {
		fmt.Fprintln(a.Out, i)
	}
	return nil
}

// --- show -------------------------------------------------------------------

// checkAgentSeals makes show report what the gateway enforces on a host
// whose seal covers agent data: an agent whose data the seal does not
// cover is denied everything, and so is _http.yaml's list ignored; an
// earlier signed file put back (its digest is not the latest record's) is
// reported, as the gateway still counts it.
func (a *Admin) checkAgentSeals(p *Posture) error {
	pub, agents, err := a.sealInfo()
	if err != nil {
		return err
	}
	p.AgentsSealed = agents
	latest := a.latestDataRecords()
	if !agents {
		// agent-admin signed agent data here before: the seal's agent
		// part (or the seal) was removed since.
		if len(latest) > 0 {
			p.SealTampered = "the seal no longer covers the agents' data, though agent-admin sealed it (journal, gateway.data): " +
				"the gateway counts their data and _http.yaml as they are, edited or not; agent-admin seal --agents seals them again"
		}
		return nil
	}
	check := func(name string, y []byte, readErr error) string {
		if readErr != nil {
			return "its file cannot be read (" + readErr.Error() + ")"
		}
		payload, err := signedEntry(name, y)
		if err != nil {
			return "its file has no entry " + name
		}
		var doc map[string]map[string]any
		_ = yaml.Unmarshal(y, &doc)
		sig, _ := doc[name]["signature"].(string)
		pb, ok := verifiedPayload(pub, sig)
		var got map[string]any
		if !ok || json.Unmarshal(pb, &got) != nil {
			return "it does not match its seal (changed outside agent-admin, or never signed)"
		}
		want, _ := json.Marshal(payload)
		var wantAny map[string]any
		_ = json.Unmarshal(want, &wantAny)
		if !reflect.DeepEqual(got, wantAny) {
			return "it does not match its seal (changed outside agent-admin)"
		}
		if d, ok := latest[name]; ok && d != entryDigest(payload) {
			return "an earlier version was put back: it is signed, but not the latest agent-admin wrote (journal, gateway.data); the gateway counts it"
		}
		return ""
	}
	for n, d := range p.Agents {
		y, err := os.ReadFile(a.gatewayFile(n))
		if why := check(n, y, err); why != "" {
			d.Tampered = why
			if !strings.HasPrefix(why, "an earlier version") {
				d.Tampered += ": the gateway denies the agent everything until agent-admin writes its data again (or agent-admin seal --agents)"
			}
		}
	}
	y, err := os.ReadFile(a.p(GatewayHTTPIssuers))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if why := check(HTTPIssuersName, y, err); why != "" {
		p.IssuersTampered = "_http.yaml: " + why
		if !strings.HasPrefix(why, "an earlier version") {
			p.IssuersTampered += ": the gateway trusts no issuer for agents until agent-admin issuers writes it again"
		}
	}
	return nil
}

// latestDataRecords is the digest of the latest gateway.data record of
// each name; empty when the journal cannot be read.
func (a *Admin) latestDataRecords() map[string]string {
	out := map[string]string{}
	read := a.JournalJSON
	if read == nil {
		read = func(args ...string) ([]byte, error) {
			return exec.Command("journalctl", append([]string{"-o", "json", "--no-pager"}, args...)...).Output()
		}
	}
	b, err := read("_UID=0", "SYSLOG_IDENTIFIER=custos-promotion", "AGENT_EVENT="+journal.GatewayData)
	if err != nil {
		return out
	}
	for _, line := range bytes.Split(b, []byte("\n")) {
		var r map[string]any
		// agent-admin's records as root only: an agent could write one
		// with any other field (readiness.ByRoot).
		if json.Unmarshal(line, &r) != nil || !readiness.ByRoot(stringsOf(r)) {
			continue
		}
		n, _ := r["AGENT_DATA"].(string)
		d, _ := r["AGENT_DATA_SHA256"].(string)
		if n != "" && d != "" {
			out[n] = d // in order: the last one stays
		}
	}
	return out
}
