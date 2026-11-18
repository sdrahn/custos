package oshsetup

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

// The gateway user prepares their home before root runs the setup
// (security review 0.8, S1–S4). Nothing outside the home is read for
// them, written or given to them.

// S2: gateway.toml a link to a file only root may read. The setup refuses
// it, and the file's content does not reach the user's home.
func TestHomeLinkedGatewayTOML(t *testing.T) {
	a, _, _ := oshHost(t, "container")
	put(t, a, "/etc/shadow", "root:secret-hash:::::::\n")
	p := a.Path("/home/osh/.config/openshell/gateway.toml")
	os.Remove(p)
	if err := os.Symlink(a.Path("/etc/shadow"), p); err != nil {
		t.Fatal(err)
	}
	if err := Setup(a, Options{User: "osh", Driver: "container"}); err == nil {
		t.Error("the setup took a gateway.toml that leads out of the home")
	}
	if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("gateway.toml was replaced: %v", err)
	}
	if b := read(t, a, "/etc/shadow"); b != "root:secret-hash:::::::\n" {
		t.Errorf("/etc/shadow changed: %q", b)
	}
	// A link to a file in the home itself is the user's own: no harm.
	put(t, a, "/home/osh/real.toml", gatewayTOML)
	os.Remove(p)
	os.Symlink("../../real.toml", p)
	if _, err := (&setup{a: a, u: account{name: "osh", uid: 1001, gid: 1001, home: "/home/osh"}}).readHome("/home/osh/.config/openshell/gateway.toml"); err != nil {
		t.Errorf("a link inside the home: %v", err)
	}
}

// S1: the drop-in's directory a link to /etc/systemd/system. The setup
// writes nothing there and gives it to nobody.
func TestHomeLinkedDropInDir(t *testing.T) {
	a, r, _ := oshHost(t, "systemd")
	sys := a.Path("/etc/systemd/system")
	if err := os.MkdirAll(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	d := a.Path("/home/osh/.config/systemd/user")
	os.MkdirAll(d, 0o755)
	if err := os.Symlink(sys, d+"/openshell-driver-systemd.service.d"); err != nil {
		t.Fatal(err)
	}
	if err := Setup(a, Options{User: "osh", Driver: "systemd"}); err == nil {
		t.Error("the setup wrote through a link out of the home")
	}
	if _, err := os.Stat(sys + "/custos.conf"); err == nil {
		t.Error("custos.conf was written to /etc/systemd/system")
	}
	for _, c := range r.Cmds {
		if strings.Contains(c, "chown") && strings.Contains(c, "/etc/systemd/system") && !strings.Contains(c, "-R root:root") {
			t.Errorf("ran %s", c)
		}
	}
}

// S4: root's copy of the registration takes regular files only: not a
// link to a file of root's, and a FIFO does not hold the setup up.
func TestHomeRegistrationRegularFilesOnly(t *testing.T) {
	a, _, _ := oshHost(t, "systemd")
	put(t, a, "/root/.ssh/id_ed25519", "root's key")
	dir := a.Path("/home/osh/.config/openshell/gateways/openshell")
	if err := os.Symlink(a.Path("/root/.ssh/id_ed25519"), dir+"/stolen"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(dir+"/fifo", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Setup(a, Options{User: "osh", Driver: "systemd"}); err != nil {
		t.Fatal(err, a.Out)
	}
	for _, f := range []string{"stolen", "fifo"} {
		if _, err := os.Lstat(a.Path("/root/.config/openshell/gateways/openshell/" + f)); err == nil {
			t.Errorf("%s was copied", f)
		}
	}
	fi, err := os.Stat(a.Path("/root/.config/openshell/gateways/openshell/mtls/tls.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("tls.key: %v %v", fi, err)
	}
}

// writeHome makes missing directories and the file as the user's: given
// through the confined root and the file's descriptor.
func TestWriteHomeOwnership(t *testing.T) {
	a, r, _ := oshHost(t, "container")
	s := &setup{a: a, u: account{name: "osh", uid: 1001, gid: 1001, home: "/home/osh"}}
	changed, err := s.writeHome("/home/osh/.config/new/dir/f.conf", []byte("x"), 0o640)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	for _, want := range []string{"lchown .config/new 1001:1001", "lchown .config/new/dir 1001:1001", "/home/osh/.config/new/dir/f.conf.custos-"} {
		if !ran(r, want) {
			t.Errorf("not: %s\n%s", want, strings.Join(r.Cmds, "\n"))
		}
	}
	if fi, err := os.Stat(a.Path("/home/osh/.config/new/dir/f.conf")); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("f.conf: %v %v", fi, err)
	}
	if changed, err := s.writeHome("/home/osh/.config/new/dir/f.conf", []byte("x"), 0o640); err != nil || changed {
		t.Errorf("again: %v %v", changed, err)
	}
	if _, err := s.writeHome("/etc/passwd", []byte("x"), 0o644); err == nil {
		t.Error("a path outside the home was written")
	}
}
