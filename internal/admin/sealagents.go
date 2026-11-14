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
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/host"
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
	b, err := os.ReadFile(a.Path(GatewaySeal))
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
	return host.WriteFile(a.Path(GatewaySeal), buf.Bytes(), 0o644)
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

// sealedYAML is the file with entry signed with k: the YAML without a
// signature, its payload signed, then the YAML with it. set puts the
// signature into the entry. The payload also says when, on which host and
// as which record it was signed (security review 0.6, N4): show compares
// the record with the journal's latest and the host with this one.
func (a *Admin) sealedYAML(k *ecdsa.PrivateKey, head, name string, entry any, set func(string)) ([]byte, map[string]any, error) {
	set("")
	y, err := encodeEntry(head, name, entry)
	if err != nil {
		return nil, nil, err
	}
	payload, err := signedEntry(name, y)
	if err != nil {
		return nil, nil, err
	}
	id := "d-" + a.Secret()
	if len(id) > 18 {
		id = id[:18]
	}
	payload["record"] = id
	payload["at"] = a.Now().UTC().Format(time.RFC3339)
	if h := a.machineID(); h != "" {
		payload["host"] = h
	}
	sig, err := signJWS(k, payload)
	if err != nil {
		return nil, nil, err
	}
	set(sig)
	y, err = encodeEntry(head, name, entry)
	return y, payload, err
}

// machineID is this host's /etc/machine-id, "" without one.
func (a *Admin) machineID() string {
	b, err := os.ReadFile(a.Path("/etc/machine-id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// verifyEntry is the payload of the entry's signature in the file y, when
// it verifies with the public key and covers the entry as it is, naming
// it; ok false otherwise.
func verifyEntry(pub, name string, y []byte) (map[string]any, bool) {
	if pub == "" {
		return nil, false
	}
	want, err := signedEntry(name, y)
	if err != nil {
		return nil, false
	}
	var doc map[string]map[string]any
	if yaml.Unmarshal(y, &doc) != nil {
		return nil, false
	}
	sig, _ := doc[name]["signature"].(string)
	pb, ok := verifiedPayload(pub, sig)
	var got map[string]any
	if !ok || json.Unmarshal(pb, &got) != nil {
		return nil, false
	}
	wb, _ := json.Marshal(want)
	var wantAny map[string]any
	_ = json.Unmarshal(wb, &wantAny)
	if !reflect.DeepEqual(got["agent"], wantAny["agent"]) || !reflect.DeepEqual(got["data"], wantAny["data"]) {
		return nil, false
	}
	return got, true
}

// entryFile is the file of a sealed entry: an agent's data, or _http.
func (a *Admin) entryFile(name string) string {
	if name == HTTPIssuersName {
		return a.Path(GatewayHTTPIssuers)
	}
	return a.gatewayFile(name)
}

// entryVerified reports whether the entry's file (absent: nothing to
// check) verifies against the seal's public key.
func (a *Admin) entryVerified(pub, name string) bool {
	y, err := os.ReadFile(a.entryFile(name))
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	_, ok := verifyEntry(pub, name, y)
	return ok
}

// existingSealKey is the seal's key from its credential; it never makes
// a new one (a new key would not match the seal the gateway has).
func (a *Admin) existingSealKey() (*ecdsa.PrivateKey, error) {
	if _, err := os.Stat(a.Path(filepath.Join(CredstoreDir, SealCredential))); err != nil {
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
// before. On a sealed host agent-admin builds only on data that
// verifies (security review 0.6, N1): a change to an agent whose file
// does not is refused, unless it only takes from the agent (reduces: a
// pause, a demotion, a lower mode). Such a change is written unsigned
// then, and so is it when the key cannot be used (N3): the gateway denies
// an agent with unsigned data everything, which is stricter still.
func (a *Admin) gatewayYAML(name string, d *GatewayData, reduces bool) ([]byte, func(), error) {
	d.Signature = ""
	pub, agents, err := a.sealInfo()
	if err != nil {
		return nil, nil, err
	}
	unsigned := func() ([]byte, func(), error) {
		y, err := encodeEntry(fmt.Sprintf(gatewayHead, name), name, d)
		return y, func() {}, err
	}
	if !agents {
		return unsigned()
	}
	if !a.entryVerified(pub, name) {
		if !reduces {
			return nil, nil, fmt.Errorf("agent %s: its gateway data does not match the seal (changed outside agent-admin): check it "+
				"(agent-admin show %s), then accept it as it is (agent-admin seal --agents --accept %s) before changing it", name, name, name)
		}
		fmt.Fprintf(a.Out, "agent %s: its gateway data does not match the seal; the change is written unsigned, and the gateway "+
			"denies the agent everything until it is accepted (agent-admin seal --agents --accept %s)\n", name, name)
		return unsigned()
	}
	k, err := a.existingSealKey()
	if err == nil && !matchesSeal(k, pub) {
		err = fmt.Errorf("the seal's key (%s) does not match the seal (%s)", filepath.Join(CredstoreDir, SealCredential), GatewaySeal)
	}
	if err != nil {
		if !reduces {
			return nil, nil, err
		}
		fmt.Fprintf(a.Out, "agent %s: the seal's key cannot be used (%v); the change is written unsigned, and the gateway denies "+
			"the agent everything until it is signed again (agent-admin seal --rekey)\n", name, err)
		return unsigned()
	}
	y, payload, err := a.sealedYAML(k, fmt.Sprintf(sealedGatewayHead, name), name, d, func(s string) { d.Signature = s })
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
	a.Record(journal.GatewayData, agent, fmt.Sprintf("%s signed %s", a.Who(), what),
		journal.Fields{"DATA": name, "DATA_SHA256": entryDigest(payload), "DATA_RECORD": fmt.Sprint(payload["record"]), "BY": a.Who()})
}

// agentFiles are the names of the agents with gateway data.
func (a *Admin) agentFiles() ([]string, error) {
	m, err := filepath.Glob(a.Path(filepath.Join(GatewayAgents, "*.yaml")))
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

// resign signs every agent's data and _http.yaml with k, then writes
// the seal covering them: the signatures first, so that the gateway never
// sees a seal over agent data that is not signed yet. An entry that does
// not verify against oldPub (the seal so far; "" for none) is signed only
// when accept names it, or when strict is false: a host that never
// sealed agent data, whose files are signed as they are (security review
// 0.6, N1). Nothing is written when one is refused.
func (a *Admin) resign(k *ecdsa.PrivateKey, oldPub string, accept map[string]bool, strict bool) error {
	names, err := a.agentFiles()
	if err != nil {
		return err
	}
	var refused []string
	for _, n := range append(append([]string{}, names...), HTTPIssuersName) {
		if strict && !accept[n] && !a.entryVerified(oldPub, n) {
			refused = append(refused, n)
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("these do not match the seal (changed outside agent-admin): %s; check each (agent-admin show), "+
			"then accept it as it is with --accept NAME (_http for the token issuers)", strings.Join(refused, ", "))
	}
	for _, n := range names {
		d, err := a.readGateway(n)
		if err != nil {
			return err
		}
		if d.Permissions == nil {
			d.Permissions = []map[string]any{}
		}
		y, payload, err := a.sealedYAML(k, fmt.Sprintf(sealedGatewayHead, n), n, d, func(s string) { d.Signature = s })
		if err != nil {
			return err
		}
		if err := host.WriteFile(a.gatewayFile(n), y, 0o644); err != nil {
			return err
		}
		a.recordData(n, payload)
	}
	issuers, err := a.AgentIssuers()
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

// sealedBefore reports whether agent-admin signed agent data on this host
// before (gateway.data records): its files are then signed again only
// when they verify, or are accepted by name.
func (a *Admin) sealedBefore() bool { return len(a.latestDataRecords()) > 0 }

// SealAgents makes the host's seal cover the agents' data and _http.yaml
// (agent-admin seal --agents): a host sealed before this release, whose
// agents' data is not signed yet. The host needs a seal (an enablement
// state, §6b). On a host that sealed agent data before, a file that does
// not verify is signed only when accept names it.
func (a *Admin) SealAgents(accept []string) error {
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
		return fmt.Errorf("%w (a lost key: agent-admin seal --rekey)", err)
	}
	if !matchesSeal(k, pub) {
		return fmt.Errorf("the seal's key (%s) does not match the seal (%s)", filepath.Join(CredstoreDir, SealCredential), GatewaySeal)
	}
	if err := a.resign(k, pub, setOf(accept), agents || a.sealedBefore()); err != nil {
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

// Rekey gives the host's seal a new key (agent-admin seal --rekey: the
// key was lost, or should change), security review 0.6, N2. It signs
// again only what verifies against the seal so far (its public key,
// _seal.yaml, is all it needs), and what accept names
// (_enablement for the categories' state, _http for the issuers). Nothing
// changes when one is refused.
func (a *Admin) Rekey(by string, accept []string) error {
	oldPub, agents, err := a.sealInfo()
	if err != nil {
		return err
	}
	acc := setOf(accept)
	e, err := a.GetEnablement()
	if err != nil {
		return err
	}
	if e != nil && !acc["_enablement"] && !verifySeal(oldPub, e) {
		return errors.New("the task categories' state does not match the seal: check it (agent-admin show), " +
			"then accept it as it is with --accept _enablement")
	}
	if agents || a.sealedBefore() {
		names, err := a.agentFiles()
		if err != nil {
			return err
		}
		var refused []string
		for _, n := range append(names, HTTPIssuersName) {
			if !acc[n] && !a.entryVerified(oldPub, n) {
				refused = append(refused, n)
			}
		}
		if len(refused) > 0 {
			return fmt.Errorf("these do not match the seal (changed outside agent-admin): %s; check each (agent-admin show), "+
				"then accept it as it is with --accept NAME (_http for the token issuers)", strings.Join(refused, ", "))
		}
	}
	k, err := a.newSealKey()
	if err != nil {
		return err
	}
	if e != nil {
		if e.Signature, err = sign(k, e); err != nil {
			return err
		}
		if err := a.writeEnablementFile(e); err != nil {
			return err
		}
	}
	if agents || a.sealedBefore() {
		if err := a.resign(k, "", nil, false); err != nil {
			return err
		}
	} else {
		pub, err := publicPEM(k)
		if err != nil {
			return err
		}
		if err := a.writeSealFile(sealFile{PublicKey: pub}); err != nil {
			return err
		}
	}
	a.Record(journal.GatewayData, "", by+" gave the seal a new key", journal.Fields{"DATA": "_seal", "BY": by})
	fmt.Fprintln(a.Out, "the seal has a new key; signed again with it: the task categories' state, the agents' data and the token issuers")
	return nil
}

func setOf(xs []string) map[string]bool {
	out := map[string]bool{}
	for _, x := range xs {
		out[x] = true
	}
	return out
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
		return host.WriteFile(a.Path(GatewayHTTPIssuers), y, 0o644)
	}
	y, payload, err := a.sealedYAML(k, sealedIssuersHead, HTTPIssuersName, h, func(s string) { h.Signature = s })
	if err != nil {
		return err
	}
	if err := host.WriteFile(a.Path(GatewayHTTPIssuers), y, 0o644); err != nil {
		return err
	}
	a.recordData(HTTPIssuersName, payload)
	return nil
}

// Issuers changes the token issuers trusted for agents (agent-admin
// issuers --add/--remove) and prints them. With neither it only prints.
// On a host whose seal covers agent data the file is signed.
func (a *Admin) Issuers(add, remove []string) error {
	issuers, err := a.AgentIssuers()
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
			// Only on issuers that verify (security review 0.6, N1); a
			// removal alone is written unsigned then: no issuer trusted.
			if !a.entryVerified(pub, HTTPIssuersName) {
				if len(add) > 0 {
					return errors.New("_http.yaml does not match the seal (changed outside agent-admin): check it, " +
						"then accept it as it is (agent-admin seal --agents --accept _http) before adding an issuer")
				}
				fmt.Fprintln(a.Out, "_http.yaml does not match the seal; written unsigned: the gateway trusts no issuer until it is accepted")
			} else {
				if k, err = a.existingSealKey(); err != nil {
					return err
				}
				if !matchesSeal(k, pub) {
					return fmt.Errorf("the seal's key (%s) does not match the seal (%s)", filepath.Join(CredstoreDir, SealCredential), GatewaySeal)
				}
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
	host := a.machineID()
	check := func(name string, y []byte, readErr error) string {
		if readErr != nil {
			return "its file cannot be read (" + readErr.Error() + ")"
		}
		got, ok := verifyEntry(pub, name, y)
		if !ok {
			return "it does not match its seal (changed outside agent-admin, or never signed)"
		}
		// Freshness (security review 0.6, N4): the record agent-admin
		// signed last, by the journal; and this host's signature.
		if l, ok := latest[name]; ok {
			rec, _ := got["record"].(string)
			if (l.record != "" && rec != l.record) || (l.record == "" && l.digest != entryDigest(got)) {
				return "an earlier version was put back: it is signed, but not the latest agent-admin wrote (journal, gateway.data); the gateway counts it"
			}
		}
		if h, _ := got["host"].(string); h != "" && host != "" && h != host {
			return "it was signed on another host (machine-id " + h + "): the gateway counts it, as the hosts share the seal's key"
		}
		return ""
	}
	for n, d := range p.Agents {
		y, err := os.ReadFile(a.gatewayFile(n))
		if why := check(n, y, err); why != "" {
			d.Tampered = why
			if strings.HasPrefix(why, "it does not match") || strings.HasPrefix(why, "its file") {
				d.Tampered += ": the gateway denies the agent everything until an administrator checks it and accepts it (agent-admin seal --agents --accept " + n + ")"
			}
		}
	}
	y, err := os.ReadFile(a.Path(GatewayHTTPIssuers))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if why := check(HTTPIssuersName, y, err); why != "" {
		p.IssuersTampered = "_http.yaml: " + why
		if strings.HasPrefix(why, "it does not match") || strings.HasPrefix(why, "its file") {
			p.IssuersTampered += ": the gateway trusts no issuer for agents until an administrator checks it and accepts it (agent-admin seal --agents --accept _http)"
		}
	}
	return nil
}

// dataRecord is a gateway.data record's digest and record id.
type dataRecord struct{ digest, record string }

// latestDataRecords is the latest gateway.data record of each name;
// empty when the journal cannot be read.
func (a *Admin) latestDataRecords() map[string]dataRecord {
	out := map[string]dataRecord{}
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
		rec, _ := r["AGENT_DATA_RECORD"].(string)
		if n != "" && d != "" {
			out[n] = dataRecord{d, rec} // in order: the last one stays
		}
	}
	return out
}
