package admin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func modelYAML(cred string) string {
	return strings.Replace(yml, `endpoint: "http://ai:8000/v1"}`, `endpoint: "http://ai:8000/v1", credential: `+cred+`}`, 1)
}

// A key given as model.credential is refused, without repeating it, by
// check, create, edit and defaults: the agent would not start, and the key
// would sit in a file others read.
func TestKeyAsCredentialName(t *testing.T) {
	a, _ := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(modelYAML("sk-litellm-local")), 0o644)
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "looks like a key") || strings.Contains(err.Error(), "litellm") {
			t.Errorf("%s: %v", what, err)
		}
	}
	refused("check", a.Check(f))
	refused("create", a.Create("patch-pilot", f))
	if _, err := os.Stat(a.agentDir("patch-pilot")); !os.IsNotExist(err) {
		t.Fatal("created all the same")
	}
	refused("defaults", a.SetDefaults([]byte("model: {provider: openai, name: m, endpoint: \"http://ai/v1\", credential: sk-litellm-local}\n")))
	if _, err := os.Stat(a.Path(DefaultsFile)); !os.IsNotExist(err) {
		t.Error("defaults written all the same")
	}

	ok := filepath.Join(t.TempDir(), "ok.yaml")
	os.WriteFile(ok, []byte(modelYAML("model-key")), 0o644)
	if err := a.Create("patch-pilot", ok); err != nil {
		t.Fatal(err)
	}
	refused("edit", a.Edit("patch-pilot", f))

	// Stored under that name, it is a name after all (odd, but it works).
	os.MkdirAll(a.Path(CredstoreDir), 0o700)
	os.WriteFile(a.Path(filepath.Join(CredstoreDir, "agent-patch-pilot.sk-litellm-local")), []byte("x"), 0o600)
	if err := a.Edit("patch-pilot", f); err != nil {
		t.Errorf("edit with the credential stored: %v", err)
	}
	if err := a.Check(f); err != nil {
		t.Errorf("check with the credential stored: %v", err)
	}
}

// After create and edit, agent-admin says when the model's credential is
// not stored yet, with the command that stores it; not once it is.
func TestNoteUnstoredCredential(t *testing.T) {
	a, _ := newAdmin(t)
	var out bytes.Buffer
	a.Out = &out
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(modelYAML("model-key")), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	want := "agent-admin set-credential patch-pilot model-key"
	if !strings.Contains(out.String(), "model-key is not stored yet") || !strings.Contains(out.String(), want) {
		t.Errorf("create says %q", out.String())
	}
	out.Reset()
	os.MkdirAll(a.Path(CredstoreDir), 0o700)
	os.WriteFile(a.Path(filepath.Join(CredstoreDir, "agent-patch-pilot.model-key")), []byte("x"), 0o600)
	if err := a.Edit("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "not stored") {
		t.Errorf("edit with the credential stored says %q", out.String())
	}
	out.Reset()
	os.WriteFile(f, []byte(yml), 0o644) // no credential: nothing to say
	if err := a.Edit("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "credential") {
		t.Errorf("edit without a credential says %q", out.String())
	}
}

// agent-admin credentials lists names and what uses them, never values;
// the model's credential that is not stored is missing, as is an OpenShell
// agent's gateway credential before set-credential.
func TestCredentialsList(t *testing.T) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(modelYAML("model-key")), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(f, []byte(oshYml), 0o644)
	if err := a.Create("sb", f); err != nil {
		t.Fatal(err)
	}
	c, err := a.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	pp := c["patch-pilot"]
	if len(pp.Missing) != 1 || pp.Missing[0] != "model-key" || len(pp.Credentials) != 1 || pp.Credentials[0].Stored ||
		pp.Credentials[0].UsedBy[0] != "model" {
		t.Errorf("before set-credential: %+v", pp)
	}
	if sb := c["sb"]; len(sb.Missing) != 1 || sb.Missing[0] != "gateway-token" || sb.Credentials[0].HeldBy != "openshell" {
		t.Errorf("OpenShell agent: %+v", sb)
	}

	os.MkdirAll(a.Path(CredstoreDir), 0o700)
	os.WriteFile(a.Path(filepath.Join(CredstoreDir, "agent-patch-pilot.model-key")), []byte("secret-value"), 0o600)
	os.WriteFile(a.Path(filepath.Join(CredstoreDir, "agent-patch-pilot.old-key")), []byte("secret-value"), 0o600)
	// Another agent's whose name starts the same is not this one's.
	os.WriteFile(a.Path(filepath.Join(CredstoreDir, "agent-patch-pilot-2.model-key")), []byte("x"), 0o600)
	r.gone = func(cmd string) error { return nil } // the relay's socket is enabled
	c, err = a.Credentials("patch-pilot", "sb")
	if err != nil {
		t.Fatal(err)
	}
	pp = c["patch-pilot"]
	if len(pp.Missing) != 0 || len(pp.Credentials) != 2 || pp.Credentials[0].Name != "model-key" || !pp.Credentials[0].Stored ||
		pp.Credentials[0].StoredAt == "" || pp.Credentials[1].Name != "old-key" || len(pp.Credentials[1].UsedBy) != 0 {
		t.Errorf("stored: %+v", pp)
	}
	if sb := c["sb"]; len(sb.Missing) != 0 || !sb.Credentials[0].Stored {
		t.Errorf("OpenShell agent with its sandbox: %+v", sb)
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "secret-value") {
		t.Error("a value is listed")
	}
	if _, err := a.Credentials("../x"); err == nil {
		t.Error("an invalid name is listed")
	}
}

// remove-credential removes a stored credential; one the model uses only
// with --force, and never an OpenShell agent's gateway credential.
func TestRemoveCredential(t *testing.T) {
	a, _ := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(modelYAML("model-key")), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(a.Path(CredstoreDir), 0o700)
	key := a.Path(filepath.Join(CredstoreDir, "agent-patch-pilot.model-key"))
	old := a.Path(filepath.Join(CredstoreDir, "agent-patch-pilot.old-key"))
	os.WriteFile(key, []byte("x"), 0o600)
	os.WriteFile(old, []byte("x"), 0o600)
	if err := a.RemoveCredential("patch-pilot", "old-key", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old-key stays")
	}
	if err := a.RemoveCredential("patch-pilot", "old-key", false); err == nil || !strings.Contains(err.Error(), "no stored credential") {
		t.Errorf("twice: %v", err)
	}
	if err := a.RemoveCredential("patch-pilot", "model-key", false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("the model's: %v", err)
	}
	if _, err := os.Stat(key); err != nil {
		t.Error("removed without --force")
	}
	if err := a.RemoveCredential("patch-pilot", "model-key", true); err != nil {
		t.Fatal(err)
	}
	if err := a.RemoveCredential("patch-pilot", "gateway-token", true); err == nil || !strings.Contains(err.Error(), "OpenShell") {
		t.Errorf("gateway-token: %v", err)
	}
	if err := a.RemoveCredential("patch-pilot", "../../etc/passwd", true); err == nil {
		t.Error("a path is removed")
	}
}
