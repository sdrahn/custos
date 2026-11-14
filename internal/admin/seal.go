package admin

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// The seal (CONTRACT.md §6b, #109). agent-admin signs the enablement state
// (ES256, a JWS over its state, enabled and record) with a private key it
// keeps as a systemd credential, encrypted to the host (and its TPM, where
// it has one). The public key is gateway data, _seal.yaml, and the agents
// module counts the state only with a valid signature: edited by hand,
// unsigned or deleted, the host is diagnostics-only. Root can still decrypt
// the key and sign a state of its own: the seal makes a change past
// agent-admin a deliberate forgery, not an edit.
const (
	SealCredential = "custos-enablement-seal"
	GatewaySeal    = "/etc/mcp-gateway/policy/agents/_seal.yaml"
)

// sealPayload is what the signature covers.
type sealPayload struct {
	State   string   `json:"state"`
	Enabled []string `json:"enabled"`
	Record  string   `json:"record"`
}

func payloadOf(e *Enablement) sealPayload {
	en := append([]string{}, e.Enabled...)
	return sealPayload{State: e.State, Enabled: en, Record: e.Record}
}

// output runs a command and returns its standard output.
func (a *Admin) output(stdin io.Reader, name string, args ...string) ([]byte, error) {
	if a.Output != nil {
		return a.Output(stdin, name, args...)
	}
	c := exec.Command(name, args...)
	c.Stdin, c.Stderr = stdin, os.Stderr
	return c.Output()
}

// sealKey is the signing key: the credential's, or a new one, encrypted
// with systemd-creds (its default key: the TPM and the host's, where
// there is a TPM).
func (a *Admin) sealKey() (*ecdsa.PrivateKey, error) {
	path := a.Path(filepath.Join(CredstoreDir, SealCredential))
	if _, err := os.Stat(path); err == nil {
		b, err := a.output(nil, "systemd-creds", "decrypt", "--name="+SealCredential, path, "-")
		if err != nil {
			return nil, fmt.Errorf("decrypting the seal's key (%s): %w", path, err)
		}
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, fmt.Errorf("the seal's key (%s) is no PEM", path)
		}
		return x509.ParseECPrivateKey(blk.Bytes)
	}
	return a.newSealKey()
}

// newSealKey makes a new key and keeps it as the credential, replacing
// one there was (agent-admin seal --rekey).
func (a *Admin) newSealKey() (*ecdsa.PrivateKey, error) {
	path := a.Path(filepath.Join(CredstoreDir, SealCredential))
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := a.Run(bytes.NewReader(pemKey), "systemd-creds", "encrypt", "--name="+SealCredential, "-", path); err != nil {
		return nil, fmt.Errorf("encrypting the seal's key: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return k, nil
}

var b64 = base64.RawURLEncoding

// sign is the JWS (compact, ES256) of the state.
func sign(k *ecdsa.PrivateKey, e *Enablement) (string, error) { return signJWS(k, payloadOf(e)) }

// signJWS is the JWS (compact, ES256) of a payload.
func signJWS(k *ecdsa.PrivateKey, payload any) (string, error) {
	h, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT"})
	p, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	in := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	sum := sha256.Sum256([]byte(in))
	r, s, err := ecdsa.Sign(rand.Reader, k, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return in + "." + b64.EncodeToString(sig), nil
}

// verifySeal reports whether the state's signature verifies with the
// public key (PEM) and covers its state, enabled and record.
func verifySeal(pubPEM string, e *Enablement) bool {
	if e == nil {
		return false
	}
	pb, ok := verifiedPayload(pubPEM, e.Signature)
	if !ok {
		return false
	}
	var got sealPayload
	if json.Unmarshal(pb, &got) != nil {
		return false
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(payloadOf(e))
	return bytes.Equal(gb, wb)
}

// verifiedPayload is the payload of a JWS (compact, ES256) whose
// signature verifies with the public key (PEM).
func verifiedPayload(pubPEM, jws string) ([]byte, bool) {
	blk, _ := pem.Decode([]byte(pubPEM))
	if blk == nil {
		return nil, false
	}
	pk, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, false
	}
	pub, ok := pk.(*ecdsa.PublicKey)
	if !ok {
		return nil, false
	}
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		return nil, false
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, false
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	return pb, true
}

// sealPublicKey is the public key in the gateway's data, "" when the host
// has no seal.
func (a *Admin) sealPublicKey() (string, error) {
	pub, _, err := a.sealInfo()
	return pub, err
}

// publicPEM is the key's public half, PEM.
func publicPEM(k *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

// writeSeal puts the key's public half into the gateway's data, when it
// is not there already. A new seal (or a new key) covers the agents' data
// from the start (#158): it signs every agent's data and _http.yaml
// first, then writes the seal.
func (a *Admin) writeSeal(k *ecdsa.PrivateKey) error {
	pub, err := publicPEM(k)
	if err != nil {
		return err
	}
	have, _, err := a.sealInfo()
	if err == nil && have == pub {
		return nil
	}
	// A new seal (the host had none): what is on disk is signed as it
	// is. A seal that was removed is reported by show (security review
	// 0.5, M5); a missing key of a seal that is there is an error, never
	// a new key (enablementKey; 0.6, N2).
	return a.resign(k, "", nil, false)
}

// checkSeal makes show report what the gateway enforces on a sealed host:
// a state the seal does not cover (edited, unsigned, deleted) counts as
// diagnostics-only, and says why.
func (a *Admin) checkSeal(p *Posture) error {
	pub, err := a.sealPublicKey()
	if err != nil || pub == "" {
		return err
	}
	e := p.Enablement
	switch {
	case e == nil:
		p.Enablement = &Enablement{State: DiagnosticsOnly, Enabled: []string{Diagnostics}, Sealed: true,
			Tampered: "the state was removed though the host is sealed: the gateway treats the host as diagnostics-only until agent-admin enable or disable writes it again"}
	case !verifySeal(pub, e):
		was, _ := json.Marshal(map[string]any{"state": e.State, "enabled": e.Enabled})
		why := "the state on file (" + string(was) + ") does not match its seal: the gateway treats the host as diagnostics-only until agent-admin enable or disable writes it again"
		if e.Tampered != "" {
			why += "; " + e.Tampered // the journal's account
		}
		p.Enablement = &Enablement{State: DiagnosticsOnly, Enabled: []string{Diagnostics}, Sealed: true, ChangedBy: e.ChangedBy, Record: e.Record, Tampered: why}
	default:
		e.Sealed = true
	}
	return nil
}

// currentEnablement is the state the gateway enforces, which agent-admin
// decides by: on a sealed host, a state the seal does not cover (edited,
// unsigned, removed) is diagnostics-only.
func (a *Admin) currentEnablement() (*Enablement, error) {
	e, err := a.GetEnablement()
	if err != nil {
		return nil, err
	}
	pub, err := a.sealPublicKey()
	if err != nil || pub == "" {
		return e, err
	}
	if e == nil || !verifySeal(pub, e) {
		return &Enablement{State: DiagnosticsOnly, Enabled: []string{Diagnostics}}, nil
	}
	return e, nil
}

// checkSealRemoved makes show report a seal that was removed: a host
// without _seal.yaml whose journal says agent-admin sealed it. The gateway
// then counts the state as it is, or every category without one (security
// review M5).
func (a *Admin) checkSealRemoved(p *Posture) error {
	if pub, err := a.sealPublicKey(); err != nil || pub != "" {
		return err
	}
	read := a.JournalJSON
	if read == nil {
		read = func(args ...string) ([]byte, error) {
			return exec.Command("journalctl", append([]string{"-o", "json", "--no-pager"}, args...)...).Output()
		}
	}
	recs, err := a.enablementRecords(read)
	if err != nil {
		return nil
	}
	sealed := false
	for _, r := range recs {
		sealed = sealed || r["AGENT_SEALED"] == "1"
	}
	if !sealed {
		return nil
	}
	why := "the seal (" + GatewaySeal + ") was removed, though agent-admin sealed this host: the gateway counts the state on file as it is, " +
		"edited or not; agent-admin enable or disable seals it again"
	if p.Enablement == nil {
		cats, _ := a.Categories()
		var all []string
		for c := range cats {
			all = append(all, c)
		}
		slices.Sort(all)
		p.Enablement = &Enablement{State: SomeCategories, Enabled: all, Tampered: why + " (no state: every category)"}
		return nil
	}
	if p.Enablement.Tampered != "" {
		why += "; " + p.Enablement.Tampered
	}
	p.Enablement.Tampered = why
	return nil
}
