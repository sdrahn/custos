package admin

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/config"
)

// A systemd agent reaches its model endpoint's addresses and the
// nameservers, besides the host (security review M4): a model on this
// host adds nothing, an address is taken as it is, a name as it resolves.
func TestFenceAddresses(t *testing.T) {
	a, _ := newAdmin(t)
	os.MkdirAll(a.p("/etc"), 0o755)
	os.WriteFile(a.p(ResolvConf), []byte("# resolv\nnameserver 10.0.2.3\nnameserver 127.0.0.53\nsearch example.com\n"), 0o644)
	a.LookupHost = func(h string) ([]string, error) {
		switch h {
		case "ai.example.com":
			return []string{"10.0.5.20", "fd00::20", "10.0.5.20"}, nil
		case "localhost":
			return []string{"127.0.0.1", "::1"}, nil
		}
		return nil, errors.New("no such host")
	}
	for endpoint, want := range map[string]string{
		"http://127.0.0.1:8000/v1":  "",
		"http://localhost:8000/v1":  "10.0.2.3", // the nameservers, as for any name
		"http://10.0.9.9:8000/v1":   "10.0.9.9",
		"https://ai.example.com/v1": "10.0.5.20 fd00::20 10.0.2.3",
		"http://[fd00::30]:8000/v1": "fd00::30",
	} {
		cfg := &config.Config{Model: config.Model{Endpoint: endpoint}}
		got, err := a.FenceAddresses(cfg)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("%s: %v %v, want %q", endpoint, got, err, want)
		}
	}
	if _, err := a.FenceAddresses(&config.Config{Model: config.Model{Endpoint: "https://nowhere.example.com/v1"}}); err == nil {
		t.Error("an endpoint that does not resolve: no error")
	}
}

// fence sets the addresses on the agent's unit for this start; an
// OpenShell agent's sandbox is OpenShell's to fence.
func TestFence(t *testing.T) {
	a, r := newAdmin(t)
	a.LookupHost = func(string) ([]string, error) { return []string{"10.0.5.20"}, nil }
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(strings.Replace(yml, "http://ai:8000/v1", "https://ai.example.com/v1", 1)), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil
	if err := a.Fence("patch-pilot"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.cmds, "systemctl set-property --runtime agent@patch-pilot.service IPAddressAllow=10.0.5.20") {
		t.Errorf("commands %v", r.cmds)
	}
	os.WriteFile(f, []byte(oshYml), 0o644)
	if err := a.Create("sb", f); err != nil {
		t.Fatal(err)
	}
	r.cmds = nil
	if err := a.Fence("sb"); err != nil || len(r.cmds) != 0 {
		t.Errorf("an OpenShell agent: %v %v", err, r.cmds)
	}
	if err := a.Fence("../x"); err == nil {
		t.Error("a bad name")
	}
}
