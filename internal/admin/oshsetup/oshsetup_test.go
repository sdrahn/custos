package oshsetup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/admintest"
	"github.com/sdrahn/custos/internal/host/hosttest"
)

// OpenShell's RPM default, as far as the setup touches it.
const gatewayTOML = `# OpenShell gateway configuration
[openshell]
version = 2

[openshell.gateway]
bind_address = "127.0.0.1:17670"
compute_driver = "podman"
`

func put(t *testing.T, a *admin.Admin, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(a.Path(path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.Path(path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, a *admin.Admin, path string) string {
	t.Helper()
	b, err := os.ReadFile(a.Path(path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// caPEM is a CA certificate (ca) or a server's (not).
func caPEM(t *testing.T, ca bool) string {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: ca, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// oshHost is a host with OpenShell installed for osh (uid 1001) and,
// for the systemd driver, the driver's packages.
func oshHost(t *testing.T, driver string) (*admin.Admin, *hosttest.Rec, string) {
	t.Helper()
	a, r := admintest.New(t)
	var out strings.Builder
	a.Out = &out
	put(t, a, "/etc/passwd", "root:x:0:0:root:/root:/bin/bash\nosh:x:1001:1001::/home/osh:/bin/bash\n")
	for _, p := range []string{"/usr/local/bin/openshell", "/usr/local/bin/openshell-gateway", "/usr/bin/update-ca-certificates", "/usr/sbin/restorecon",
		"/usr/lib/systemd/user/openshell-gateway.service"} {
		put(t, a, p, "")
	}
	put(t, a, "/home/osh/.config/openshell/gateway.toml", gatewayTOML)
	put(t, a, "/home/osh/.config/openshell/gateways/openshell/metadata.json", `{"name":"openshell"}`)
	put(t, a, "/home/osh/.config/openshell/gateways/openshell/mtls/ca.crt", "gateway CA")
	put(t, a, "/home/osh/.config/openshell/gateways/openshell/mtls/tls.key", "key")
	switch driver {
	case "systemd":
		for _, f := range driverFiles {
			put(t, a, f, "")
		}
		// The driver's socket, once its service runs.
		put(t, a, "/run/user/1001/openshell/systemd-driver.sock", "")
	case "container":
		put(t, a, "/usr/bin/podman", "")
	}
	// Ownership through descriptors and the confined home, recorded (a
	// test does not run as root).
	oldF, oldL := fchown, lchown
	t.Cleanup(func() { fchown, lchown = oldF, oldL })
	fchown = func(f *os.File, uid, gid int) error {
		r.Cmds = append(r.Cmds, fmt.Sprintf("fchown %s %d:%d", f.Name(), uid, gid))
		return nil
	}
	lchown = func(_ *os.Root, name string, uid, gid int) error {
		r.Cmds = append(r.Cmds, fmt.Sprintf("lchown %s %d:%d", name, uid, gid))
		return nil
	}
	// The gateway answers root; it names the systemd driver.
	r.Gone = func(cmd string) error {
		if strings.HasPrefix(cmd, "openshell -g openshell status") {
			return nil
		}
		return errors.New("not active")
	}
	a.Output = func(_ io.Reader, name string, args ...string) ([]byte, error) {
		r.Cmds = append(r.Cmds, name+" "+strings.Join(args, " "))
		return []byte("Driver: openshell-driver-systemd 0.1.4\n"), nil
	}
	return a, r, a.Root
}

func ran(r *hosttest.Rec, want string) bool {
	for _, c := range r.Cmds {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}

// The systemd driver: the helper serves osh, the driver runs as osh's
// service with the gateway's CA, gateway.toml names it, mcp-gateway's CA
// goes into the host's trust store, root gets its own registration, and
// a second run changes nothing.
func TestSetupSystemdDriver(t *testing.T) {
	a, r, _ := oshHost(t, "systemd")
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, []byte(caPEM(t, true)), 0o644)
	// A CA bundle from the container driver: OpenShell refuses it with this one.
	put(t, a, "/home/osh/.config/openshell/gateway.toml", gatewayTOML+"\n[openshell.supervisor.network]\nadditional_ca_bundle = \"/x.pem\"\n")
	if err := Setup(a, Options{User: "osh", Driver: "systemd", CA: ca}); err != nil {
		t.Fatal(err, a.Out)
	}
	for _, want := range []string{
		"loginctl enable-linger osh",
		"systemctl restart openshell-systemd-helper.service",
		"runuser -u osh -- env HOME=/home/osh XDG_RUNTIME_DIR=/run/user/1001 systemctl --user restart openshell-driver-systemd.service",
		"runuser -u osh -- env HOME=/home/osh XDG_RUNTIME_DIR=/run/user/1001 systemctl --user restart openshell-gateway.service",
		"update-ca-certificates",
		"chown -R root:root /root/.config/openshell",
		"restorecon -R /root/.config/openshell",
		"openshell -g openshell gateway info",
	} {
		if !ran(r, want) {
			t.Errorf("not run: %s\n%s", want, strings.Join(r.Cmds, "\n"))
		}
	}
	if h := read(t, a, HelperDropIn); !strings.Contains(h, "OPENSHELL_SYSTEMD_HELPER_ALLOW_USER=osh") {
		t.Errorf("helper: %s", h)
	}
	d := read(t, a, "/home/osh/.config/systemd/user/openshell-driver-systemd.service.d/custos.conf")
	if !strings.Contains(d, "OPENSHELL_SYSTEMD_TLS_CA=/home/osh/.config/openshell/gateways/openshell/mtls/ca.crt") || !strings.Contains(d, "OPENSHELL_GATEWAY_NAME=openshell") {
		t.Errorf("driver drop-in: %s", d)
	}
	g := read(t, a, "/home/osh/.config/openshell/gateway.toml")
	for _, want := range []string{`compute_driver = "systemd"`, "[openshell.drivers.systemd]\nsocket_path = \"/run/user/1001/openshell/systemd-driver.sock\"", `bind_address = "127.0.0.1:17670"`, "# OpenShell gateway configuration"} {
		if !strings.Contains(g, want) {
			t.Errorf("gateway.toml lacks %q:\n%s", want, g)
		}
	}
	if strings.Contains(g, "additional_ca_bundle") || strings.Contains(g, `"podman"`) {
		t.Errorf("gateway.toml:\n%s", g)
	}
	if read(t, a, TrustAnchor) == "" {
		t.Error("no trust anchor")
	}
	if read(t, a, RootConfig+"/gateways/openshell/mtls/tls.key") != "key" {
		t.Error("root's registration")
	}
	out := a.Out.(*strings.Builder).String()
	if !strings.Contains(out, "openshell_driver: systemd}") || strings.Contains(out, "CUSTOS_OPENSHELL_USER") {
		t.Errorf("output:\n%s", out)
	}

	// Again, as it is now (lingering, the services running): nothing changes.
	put(t, a, filepath.Join(LingerDir, "osh"), "")
	r.Gone = func(string) error { return nil }
	r.Cmds = nil
	var again strings.Builder
	a.Out = &again
	if err := Setup(a, Options{User: "osh", Driver: "systemd", CA: ca}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(again.String(), "set  ") {
		t.Errorf("second run changed:\n%s", again.String())
	}
	for _, c := range r.Cmds {
		if strings.Contains(c, "restart") || strings.Contains(c, "update-ca-certificates") || strings.Contains(c, "loginctl") {
			t.Errorf("second run ran %s", c)
		}
	}
}

// The container driver with rootless Podman: the controllers delegated,
// subordinate IDs after the ones taken, the CA bundle in gateway.toml, and
// CUSTOS_OPENSHELL_USER for agent-admin.
func TestSetupPodman(t *testing.T) {
	a, r, _ := oshHost(t, "container")
	put(t, a, "/etc/subuid", "alice:100000:65536\nbob:200000:65536\n")
	put(t, a, "/etc/subgid", "osh:300000:65536\n")
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, []byte(caPEM(t, true)), 0o644)
	if err := Setup(a, Options{User: "osh", Driver: "container", CA: ca}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, a, DelegateDropIn), "Delegate=cpu cpuset io memory pids") {
		t.Error("no delegation")
	}
	if !ran(r, "systemctl restart user@1001.service") || !ran(r, "usermod --add-subuids 265536-331071 osh") || ran(r, "--add-subgids") {
		t.Errorf("commands:\n%s", strings.Join(r.Cmds, "\n"))
	}
	g := read(t, a, "/home/osh/.config/openshell/gateway.toml")
	if !strings.Contains(g, "[openshell.supervisor.network]\nadditional_ca_bundle = \"/home/osh/.config/openshell/mcp-gateway-ca.pem\"") || !strings.Contains(g, `compute_driver = "podman"`) {
		t.Errorf("gateway.toml:\n%s", g)
	}
	if !strings.Contains(read(t, a, "/home/osh/.config/openshell/mcp-gateway-ca.pem"), "CERTIFICATE") || !ran(r, "/home/osh/.config/openshell/mcp-gateway-ca.pem.custos-") {
		t.Errorf("the CA for the supervisors: %q", r.Cmds)
	}
	if ran(r, "update-ca-certificates") || ran(r, "openshell-systemd-helper") {
		t.Errorf("systemd driver's steps:\n%s", strings.Join(r.Cmds, "\n"))
	}
	if out := a.Out.(*strings.Builder).String(); !strings.Contains(out, "export CUSTOS_OPENSHELL_USER=osh") {
		t.Errorf("output:\n%s", out)
	}
}

// Without a gateway.toml (OpenShell's installer may leave none), the
// setup writes one.
func TestSetupNoGatewayTOML(t *testing.T) {
	a, _, _ := oshHost(t, "systemd")
	os.Remove(a.Path("/home/osh/.config/openshell/gateway.toml"))
	if err := Setup(a, Options{User: "osh", Driver: "systemd"}); err != nil {
		t.Fatal(err)
	}
	g := read(t, a, "/home/osh/.config/openshell/gateway.toml")
	if !strings.HasPrefix(g, "[openshell]\nversion = 2\n\n[openshell.gateway]\ncompute_driver = \"systemd\"\n") {
		t.Errorf("gateway.toml:\n%s", g)
	}
}

// A gateway on Docker stays on Docker, also where Podman is installed:
// no delegation, no subordinate IDs, gateway.toml's driver left to
// OpenShell's detection; a gateway coming from the systemd driver gets
// the runtime named.
func TestSetupDocker(t *testing.T) {
	a, r, _ := oshHost(t, "container")
	put(t, a, "/usr/bin/docker", "")
	put(t, a, "/home/osh/.config/openshell/gateway.toml", "[openshell]\nversion = 2\n")
	r.Gone = func(cmd string) error {
		if strings.HasPrefix(cmd, "openshell -g openshell status") || cmd == "docker info" {
			return nil
		}
		return errors.New("not active")
	}
	if err := Setup(a, Options{User: "osh", Driver: "container"}); err != nil {
		t.Fatal(err)
	}
	if ran(r, "user@1001.service") || ran(r, "usermod") || ran(r, "restart openshell-gateway") {
		t.Errorf("Podman's steps on Docker:\n%s", strings.Join(r.Cmds, "\n"))
	}
	if _, err := os.Stat(a.Path(DelegateDropIn)); err == nil {
		t.Error("a delegation for Docker")
	}
	if g := read(t, a, "/home/osh/.config/openshell/gateway.toml"); strings.Contains(g, "compute_driver") {
		t.Errorf("gateway.toml:\n%s", g)
	}
	if out := a.Out.(*strings.Builder).String(); strings.Contains(out, "CUSTOS_OPENSHELL_USER") {
		t.Errorf("output:\n%s", out)
	}
	put(t, a, "/home/osh/.config/openshell/gateway.toml", "[openshell]\nversion = 2\n\n[openshell.gateway]\ncompute_driver = \"systemd\"\n")
	if err := Setup(a, Options{User: "osh", Driver: "container"}); err != nil {
		t.Fatal(err)
	}
	if g := read(t, a, "/home/osh/.config/openshell/gateway.toml"); !strings.Contains(g, `compute_driver = "docker"`) {
		t.Errorf("from the systemd driver:\n%s", g)
	}
}

// --check says what would change and changes nothing.
func TestSetupCheck(t *testing.T) {
	a, r, _ := oshHost(t, "systemd")
	if err := Setup(a, Options{User: "osh", Driver: "systemd", Check: true}); err != nil {
		t.Fatal(err)
	}
	out := a.Out.(*strings.Builder).String()
	if !strings.Contains(out, "todo the driver's helper serves osh") || !strings.Contains(out, "run without --check") {
		t.Errorf("output:\n%s", out)
	}
	for _, c := range r.Cmds {
		if !strings.Contains(c, "is-active") {
			t.Errorf("--check ran %s", c)
		}
	}
	if g := read(t, a, "/home/osh/.config/openshell/gateway.toml"); g != gatewayTOML {
		t.Errorf("--check changed gateway.toml:\n%s", g)
	}
	if _, err := os.Stat(a.Path(HelperDropIn)); err == nil {
		t.Error("--check wrote the helper's drop-in")
	}
}

// What is not installed is named, and nothing is changed.
func TestSetupMissing(t *testing.T) {
	a, r := admintest.New(t)
	var out strings.Builder
	a.Out = &out
	put(t, a, "/etc/passwd", "osh:x:1001:1001::/home/osh:/bin/bash\n")
	err := Setup(a, Options{User: "osh", Driver: "systemd"})
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err %v", err)
	}
	for _, want := range []string{"openshell: not installed", `OpenShell's gateway "openshell" is not set up for osh`, "/usr/bin/openshell-driver-systemd: not installed (the package openshell-driver-systemd)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in:\n%s", want, out.String())
		}
	}
	if len(r.Cmds) != 0 {
		t.Errorf("ran %v", r.Cmds)
	}
	out.Reset()
	if err := Setup(a, Options{User: "nobody", Driver: "container"}); err == nil || !strings.Contains(out.String(), "user nobody: no such user") || !strings.Contains(out.String(), "podman or docker") {
		t.Errorf("err %v:\n%s", err, out.String())
	}
	if err := Setup(a, Options{User: "osh", Driver: "kubernetes"}); err == nil {
		t.Error("a driver of neither kind")
	}
}

// Refusals: a certificate that is no CA, a helper serving someone else,
// a gateway.toml of format version 1.
func TestSetupRefusals(t *testing.T) {
	a, _, _ := oshHost(t, "systemd")
	leaf := filepath.Join(t.TempDir(), "leaf.pem")
	os.WriteFile(leaf, []byte(caPEM(t, false)), 0o644)
	if err := Setup(a, Options{User: "osh", Driver: "systemd", CA: leaf}); err == nil || !strings.Contains(a.Out.(*strings.Builder).String(), "is not a CA certificate") {
		t.Errorf("a server certificate as CA: %v", err)
	}
	put(t, a, HelperDropInDir+"/user.conf", "[Service]\nEnvironment=OPENSHELL_SYSTEMD_HELPER_ALLOW_USER=alice\n")
	if err := Setup(a, Options{User: "osh", Driver: "systemd"}); err == nil || !strings.Contains(err.Error(), "serve alice") {
		t.Errorf("another user's helper: %v", err)
	}
	os.Remove(a.Path(HelperDropInDir + "/user.conf"))
	put(t, a, "/home/osh/.config/openshell/gateway.toml", "[openshell]\nversion = 1\n")
	if err := Setup(a, Options{User: "osh", Driver: "systemd"}); err == nil || !strings.Contains(err.Error(), "version 2 first") {
		t.Errorf("version 1: %v", err)
	}
}

func TestTOML(t *testing.T) {
	in := "# c\n[openshell]\nversion = 2\n\n[openshell.gateway]\ncompute_driver = \"podman\" # here\n\n[other]\nx = 1\n"
	if v, ok := getTOML(in, "openshell.gateway", "compute_driver"); !ok || v != `"podman" # here` {
		t.Errorf("get %q %v", v, ok)
	}
	if _, ok := getTOML(in, "other", "compute_driver"); ok {
		t.Error("a key of another table")
	}
	out := setTOML(in, "openshell.gateway", "compute_driver", `"systemd"`)
	if !strings.Contains(out, "[openshell.gateway]\ncompute_driver = \"systemd\"\n\n[other]") {
		t.Errorf("replace:\n%s", out)
	}
	out = setTOML(out, "openshell", "port", "1")
	if !strings.Contains(out, "version = 2\nport = 1\n\n[openshell.gateway]") {
		t.Errorf("add to a table:\n%s", out)
	}
	out = setTOML(out, "openshell.drivers.systemd", "socket_path", `"/s"`)
	if !strings.HasSuffix(out, "x = 1\n\n[openshell.drivers.systemd]\nsocket_path = \"/s\"\n") {
		t.Errorf("a new table:\n%s", out)
	}
	if out := unsetTOML(out, "other", "x"); strings.Contains(out, "x = 1") || !strings.Contains(out, "[other]") {
		t.Errorf("unset:\n%s", out)
	}
	if got := setTOML("", "a", "b", "1"); got != "[a]\nb = 1\n" {
		t.Errorf("empty: %q", got)
	}
}
