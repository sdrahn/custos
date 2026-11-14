package admin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The seal (#109): the first change creates the key, encrypted with
// systemd-creds, and the gateway's _seal.yaml; every change is signed, with
// the same key; an edit, a missing signature or a removed state is
// reported as the gateway enforces it, diagnostics-only.
func TestSeal(t *testing.T) {
	a, _, r := withCategoriesRec(t)
	if err := a.SetPermissions("patch-pilot", strings.NewReader(diagPerms)); err != nil {
		t.Fatal(err)
	}
	a.JournalJSON = func(...string) ([]byte, error) { return nil, errNoJournal }
	if err := a.InitDiagnosticsOnly("setup"); err != nil {
		t.Fatal(err)
	}
	cred := a.Path(filepath.Join(CredstoreDir, SealCredential))
	if fi, err := os.Stat(cred); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key's credential: %v %v", fi, err)
	}
	if !strings.Contains(strings.Join(r.cmds, "\n"), "systemd-creds encrypt --name="+SealCredential+" - "+cred) {
		t.Errorf("not encrypted with systemd-creds: %v", r.cmds)
	}
	pub, err := a.sealPublicKey()
	if err != nil || !strings.HasPrefix(pub, "-----BEGIN PUBLIC KEY-----") {
		t.Fatalf("_seal.yaml: %q %v", pub, err)
	}
	e, _ := a.GetEnablement()
	if e.Signature == "" || !verifySeal(pub, e) {
		t.Fatalf("not signed: %+v", e)
	}
	p, _ := a.Show()
	if !p.Enablement.Sealed || p.Enablement.Tampered != "" {
		t.Errorf("show: %+v", p.Enablement)
	}

	// The next change decrypts the same key: the seal stays.
	r.cmds = nil
	if err := a.Enable("alice", []string{"patch-errata"}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.cmds, "\n"), "systemd-creds decrypt --name="+SealCredential+" "+cred+" -") {
		t.Errorf("the key was not decrypted: %v", r.cmds)
	}
	if pub2, _ := a.sealPublicKey(); pub2 != pub {
		t.Error("the seal changed")
	}
	e, _ = a.GetEnablement()
	if !verifySeal(pub, e) {
		t.Errorf("the enabled state is not signed: %+v", e)
	}

	// Edits the signature does not cover.
	for name, edit := range map[string]func(*Enablement){
		"state":     func(e *Enablement) { e.State = DiagnosticsOnly },
		"enabled":   func(e *Enablement) { e.Enabled = append(e.Enabled, "config-lifecycle") },
		"record":    func(e *Enablement) { e.Record = "n-forged" },
		"signature": func(e *Enablement) { e.Signature = "" },
	} {
		c := *e
		c.Enabled = append([]string{}, e.Enabled...)
		edit(&c)
		if verifySeal(pub, &c) {
			t.Errorf("%s edited: still verifies", name)
		}
	}

	// Edited on file: show reports diagnostics-only, as the gateway does.
	f := a.Path(GatewayEnablement)
	b, _ := os.ReadFile(f)
	os.WriteFile(f, []byte(strings.Replace(string(b), "- patch-errata", "- patch-errata\n    - config-lifecycle", 1)), 0o644)
	p, _ = a.Show()
	if p.Enablement.State != DiagnosticsOnly || !strings.Contains(p.Enablement.Tampered, "does not match its seal") ||
		!strings.Contains(p.Enablement.Tampered, "config-lifecycle") {
		t.Errorf("edited: %+v", p.Enablement)
	}
	// Removed: likewise.
	os.Remove(f)
	p, _ = a.Show()
	if p.Enablement == nil || p.Enablement.State != DiagnosticsOnly || !strings.Contains(p.Enablement.Tampered, "was removed") {
		t.Errorf("removed: %+v", p.Enablement)
	}
	// agent-admin writes a signed state again.
	if err := a.Enable("alice", []string{"config-lifecycle"}, false); err != nil {
		t.Fatal(err)
	}
	if p, _ = a.Show(); p.Enablement.Tampered != "" || !p.Enablement.Sealed {
		t.Errorf("rewritten: %+v", p.Enablement)
	}
}

var errNoJournal = errors.New("no journal")
