package oshsetup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/admin"
)

// proc puts a process into the test host's /proc: its program and its
// real user.
func proc(t *testing.T, a *admin.Admin, pid, exe string, uid int) {
	t.Helper()
	dir := a.Path(filepath.Join("/proc", pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
	u := strconv.Itoa(uid)
	status := "Name:\tx\nUid:\t" + u + "\t" + u + "\t" + u + "\t" + u + "\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
}

// OpenShell's gateway and driver as root too, as after systemctl --user
// as root: the setup stops and disables root's services before it writes
// root's registration again, and says so under --check without doing it.
func TestSetupStopsAnotherUsersGateway(t *testing.T) {
	a, r, _ := oshHost(t, "systemd")
	proc(t, a, "100", "/usr/bin/openshell-gateway", 1001)
	proc(t, a, "101", "/usr/bin/openshell-driver-systemd", 1001)
	proc(t, a, "200", "/usr/bin/openshell-gateway", 0)
	proc(t, a, "201", "/usr/bin/openshell-driver-systemd (deleted)", 0)
	proc(t, a, "300", "/usr/bin/bash", 0)
	proc(t, a, "self", "/usr/bin/openshell-gateway", 0) // not a process ID

	stop := "runuser -u root -- env HOME=/root XDG_RUNTIME_DIR=/run/user/0 systemctl --user disable --now openshell-gateway.service openshell-driver-systemd.service"
	if err := Setup(a, Options{User: "osh", Driver: "systemd", Check: true}); err != nil {
		t.Fatal(err)
	}
	out := a.Out.(*strings.Builder).String()
	if !strings.Contains(out, "todo OpenShell's gateway and driver also run as root") {
		t.Errorf("--check output:\n%s", out)
	}
	if ran(r, stop) {
		t.Error("--check stopped root's services")
	}

	var out2 strings.Builder
	a.Out = &out2
	r.Cmds = nil
	if err := Setup(a, Options{User: "osh", Driver: "systemd"}); err != nil {
		t.Fatal(err, out2.String())
	}
	if !ran(r, stop) {
		t.Errorf("root's services not stopped:\n%s", strings.Join(r.Cmds, "\n"))
	}
	// Before root's registration is written again: a gateway of root's
	// would write its own client certificates there.
	iStop, iReg := -1, -1
	for i, c := range r.Cmds {
		if strings.Contains(c, stop) && iStop < 0 {
			iStop = i
		}
		if strings.Contains(c, "chown -R root:root /root/.config/openshell") && iReg < 0 {
			iReg = i
		}
	}
	if iStop < 0 || iReg < 0 || iStop > iReg {
		t.Errorf("stop at %d, root's registration at %d:\n%s", iStop, iReg, strings.Join(r.Cmds, "\n"))
	}
}

// Only the gateway's user runs them: nothing to stop.
func TestSetupGatewayOnlyAsItsUser(t *testing.T) {
	a, r, _ := oshHost(t, "systemd")
	proc(t, a, "100", "/usr/bin/openshell-gateway", 1001)
	if err := Setup(a, Options{User: "osh", Driver: "systemd"}); err != nil {
		t.Fatal(err)
	}
	if out := a.Out.(*strings.Builder).String(); !strings.Contains(out, "ok   OpenShell's gateway and driver run as osh only") {
		t.Errorf("output:\n%s", out)
	}
	if ran(r, "disable --now") {
		t.Errorf("stopped something:\n%s", strings.Join(r.Cmds, "\n"))
	}
}

// A user ID that /etc/passwd does not name is an error, not a guess.
func TestSetupGatewayOfUnknownUser(t *testing.T) {
	a, _, _ := oshHost(t, "systemd")
	proc(t, a, "100", "/usr/bin/openshell-gateway", 4242)
	err := Setup(a, Options{User: "osh", Driver: "systemd", Check: true})
	if err == nil || !strings.Contains(err.Error(), "user ID 4242") {
		t.Errorf("err = %v", err)
	}
}
