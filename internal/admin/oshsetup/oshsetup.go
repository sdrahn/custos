// Package oshsetup is agent-admin openshell-setup: the OpenShell side of
// OpenShell agents on this host (CONTRACT.md §16a, §17), for either compute
// driver. It installs nothing: it checks that OpenShell, the container
// runtime or the systemd compute driver are installed, then configures
// them, registers OpenShell's gateway for root and puts the gateway's
// certificate authority where the driver needs it. Every step looks
// first and changes only what differs, so it is safe to run again.
package oshsetup

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/host"
)

// Options are the command's arguments.
type Options struct {
	User    string // the user OpenShell's gateway runs as
	Driver  string // container or systemd (agent.yaml's sandbox.openshell_driver)
	Gateway string // the gateway's name (openshell)
	CA      string // a PEM file: the CA that signed mcp-gateway's certificate; "" for none
	Check   bool   // check and say what would change, change nothing
}

// Paths the setup writes or reads.
const (
	DelegateDropIn  = "/etc/systemd/system/user@.service.d/custos-openshell.conf"
	HelperDropInDir = "/etc/systemd/system/openshell-systemd-helper.service.d"
	HelperDropIn    = HelperDropInDir + "/custos.conf"
	HelperSocket    = "/run/openshell-systemd-helper/helper.sock"
	TrustAnchor     = "/etc/pki/trust/anchors/custos-mcp-gateway-ca.pem"
	RootConfig      = "/root/.config/openshell"
	LingerDir       = "/var/lib/systemd/linger"
	// The subordinate IDs given to a rootless Podman user without any.
	subIDFirst = 200000
	subIDCount = 65536
)

var binDirs = []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/sbin", "/sbin"}

// The systemd compute driver's installed files (its package
// openshell-driver-systemd).
var driverFiles = []string{
	"/usr/bin/openshell-driver-systemd",
	"/usr/libexec/openshell/openshell-systemd-helper",
	"/usr/lib/systemd/system/openshell-systemd-helper.service",
	"/usr/lib/systemd/user/openshell-driver-systemd.service",
}

type account struct {
	name, home string
	uid, gid   int
}

type setup struct {
	a       *admin.Admin
	o       Options
	u       account
	runtime string // podman or docker (container driver)
	changed []string
}

// Setup runs the checks and, unless o.Check, the configuration.
func Setup(a *admin.Admin, o Options) error {
	if o.Gateway == "" {
		o.Gateway = "openshell"
	}
	if o.Driver != "container" && o.Driver != "systemd" {
		return fmt.Errorf("openshell-setup: --driver %q: want container or systemd", o.Driver)
	}
	if o.User == "" {
		return errors.New("openshell-setup: --user: the user OpenShell's gateway runs as")
	}
	s := &setup{a: a, o: o}
	if err := s.check(); err != nil {
		return err
	}
	steps := []func() error{s.linger}
	if o.Driver == "container" {
		steps = append(steps, s.podmanUser)
	} else {
		steps = append(steps, s.helper, s.driver)
	}
	steps = append(steps, s.ca, s.gatewayConfig, s.registration)
	for _, step := range steps {
		if err := step(); err != nil {
			return fmt.Errorf("openshell-setup: %w", err)
		}
	}
	if o.Check {
		if len(s.changed) == 0 {
			s.say("ok", "nothing to change")
		} else {
			s.say("todo", fmt.Sprintf("%d change(s): run without --check to make them", len(s.changed)))
		}
		return nil
	}
	if err := s.verify(); err != nil {
		return fmt.Errorf("openshell-setup: %w", err)
	}
	s.next()
	return nil
}

func (s *setup) say(tag, msg string) { fmt.Fprintf(s.a.Out, "%-5s%s\n", tag, msg) }

// change records a change: made, or only said under --check.
func (s *setup) change(what string) {
	s.changed = append(s.changed, what)
	if s.o.Check {
		s.say("todo", what)
	} else {
		s.say("set", what)
	}
}

// --- checks: what must be installed (the administrator's part) ---

func (s *setup) has(path string) bool {
	_, err := os.Stat(s.a.Path(path))
	return err == nil
}

func (s *setup) program(name string) bool {
	for _, d := range binDirs {
		if fi, err := os.Stat(s.a.Path(filepath.Join(d, name))); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

func (s *setup) check() error {
	var missing []string
	need := func(ok bool, what string) {
		if !ok {
			missing = append(missing, what)
		}
	}
	for _, p := range []string{"openshell", "openshell-gateway"} {
		need(s.program(p), p+": not installed (OpenShell's installer or package)")
	}
	u, err := lookupUser(s.a.Path("/etc/passwd"), s.o.User)
	if err != nil {
		missing = append(missing, err.Error())
	} else {
		s.u = u
		// OpenShell's installer registers the gateway for its user; a
		// gateway.toml it may leave to the setup.
		reg := s.userConfig("gateways", s.o.Gateway)
		need(s.has(reg), fmt.Sprintf("%s: OpenShell's gateway %q is not set up for %s; install OpenShell for that user first (or --gateway)", reg, s.o.Gateway, u.name))
		need(s.has("/usr/lib/systemd/user/openshell-gateway.service") || s.has("/etc/systemd/user/openshell-gateway.service") ||
			s.has(filepath.Join(u.home, ".config/systemd/user/openshell-gateway.service")),
			"openshell-gateway.service: no user unit of OpenShell's gateway")
	}
	switch s.o.Driver {
	case "container":
		if s.runtime = s.containerRuntime(); s.runtime == "" {
			missing = append(missing, "podman or docker: neither is installed (the container driver needs one)")
		}
	case "systemd":
		for _, f := range driverFiles {
			need(s.has(f), f+": not installed (the package openshell-driver-systemd)")
		}
	}
	if s.o.CA != "" {
		if err := checkCA(s.o.CA); err != nil {
			missing = append(missing, err.Error())
		}
		if s.o.Driver == "systemd" {
			need(s.program("update-ca-certificates"), "update-ca-certificates: not installed (the package ca-certificates)")
		}
	}
	if len(missing) > 0 {
		for _, m := range missing {
			s.say("FAIL", m)
		}
		return fmt.Errorf("openshell-setup: %d thing(s) missing; nothing was changed", len(missing))
	}
	s.say("ok", "installed: openshell, openshell-gateway, "+map[string]string{"container": s.runtime, "systemd": "the systemd compute driver"}[s.o.Driver])
	return nil
}

// containerRuntime is the runtime the container driver uses: the one
// gateway.toml already names, else Docker when it runs (OpenShell's own
// detection prefers it), else Podman, else Docker. The setup never
// switches a gateway from one runtime to the other.
func (s *setup) containerRuntime() string {
	if s.u.home != "" {
		if b, err := s.readHome(s.userConfig("gateway.toml")); err == nil {
			cur, _ := getTOML(string(b), "openshell.gateway", "compute_driver")
			for _, r := range []string{"docker", "podman"} {
				if cur == tomlString(r) && s.program(r) {
					return r
				}
			}
		}
	}
	switch {
	case s.program("docker") && s.a.Probe("docker", "info") == nil:
		return "docker"
	case s.program("podman"):
		return "podman"
	case s.program("docker"):
		return "docker"
	}
	return ""
}

func checkCA(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("--ca: %w", err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return fmt.Errorf("--ca %s: not a PEM certificate", path)
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return fmt.Errorf("--ca %s: %w", path, err)
	}
	if !c.IsCA {
		return fmt.Errorf("--ca %s: %q is not a CA certificate (give the CA that signed mcp-gateway's certificate)", path, c.Subject.CommonName)
	}
	return nil
}

func lookupUser(passwd, name string) (account, error) {
	b, err := os.ReadFile(passwd)
	if err != nil {
		return account{}, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 || f[0] != name {
			continue
		}
		uid, err1 := strconv.Atoi(f[2])
		gid, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			break
		}
		return account{name: name, uid: uid, gid: gid, home: f[5]}, nil
	}
	return account{}, fmt.Errorf("user %s: no such user (create it, with a home, for OpenShell's gateway)", name)
}

// --- the configuration ---

func (s *setup) userConfig(parts ...string) string {
	return filepath.Join(append([]string{s.u.home, ".config", "openshell"}, parts...)...)
}

func (s *setup) runtimeDir() string { return fmt.Sprintf("/run/user/%d", s.u.uid) }

// userctl runs systemctl --user as the gateway's user.
func (s *setup) userctl(args ...string) error {
	cmd := append([]string{"-u", s.u.name, "--", "env", "HOME=" + s.u.home, "XDG_RUNTIME_DIR=" + s.runtimeDir(), "systemctl", "--user"}, args...)
	return s.a.Run(nil, "runuser", cmd...)
}

// write makes a file of root's (under /etc) have content; it says whether
// it changed. A file in the gateway user's home goes through writeHome.
func (s *setup) write(path string, content []byte, mode os.FileMode) (bool, error) {
	old, err := os.ReadFile(s.a.Path(path))
	if err == nil && bytes.Equal(old, content) {
		return false, nil
	}
	if s.o.Check {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(s.a.Path(path)), 0o755); err != nil {
		return false, err
	}
	if err := host.WriteFile(s.a.Path(path), content, mode); err != nil {
		return false, err
	}
	return true, nil
}

func (s *setup) linger() error {
	if s.has(filepath.Join(LingerDir, s.u.name)) {
		s.say("ok", "lingering for "+s.u.name)
		return nil
	}
	s.change("lingering for " + s.u.name + " (its services run without a login)")
	if s.o.Check {
		return nil
	}
	return s.a.Run(nil, "loginctl", "enable-linger", s.u.name)
}

// podmanUser: rootless Podman's cgroup controllers and subordinate IDs.
func (s *setup) podmanUser() error {
	if s.runtime != "podman" || s.u.uid == 0 {
		return nil
	}
	changed, err := s.write(DelegateDropIn, []byte("# agent-admin openshell-setup: rootless Podman sets the sandboxes' limits.\n[Service]\nDelegate=cpu cpuset io memory pids\n"), 0o644)
	if err != nil {
		return err
	}
	if changed {
		s.change("cgroup controllers delegated to user sessions (" + DelegateDropIn + "); " + s.u.name + "'s user manager restarts")
		if !s.o.Check {
			if err := s.a.Run(nil, "systemctl", "daemon-reload"); err != nil {
				return err
			}
			if err := s.a.Run(nil, "systemctl", "restart", fmt.Sprintf("user@%d.service", s.u.uid)); err != nil {
				return err
			}
		}
	} else {
		s.say("ok", "cgroup controllers delegated to user sessions")
	}
	for _, f := range []string{"/etc/subuid", "/etc/subgid"} {
		has, next, err := subIDs(s.a.Path(f), s.u.name)
		if err != nil {
			return err
		}
		if has {
			s.say("ok", "subordinate IDs for "+s.u.name+" in "+f)
			continue
		}
		r := fmt.Sprintf("%d-%d", next, next+subIDCount-1)
		s.change(fmt.Sprintf("subordinate IDs %s for %s in %s", r, s.u.name, f))
		if s.o.Check {
			continue
		}
		flag := "--add-subuids"
		if f == "/etc/subgid" {
			flag = "--add-subgids"
		}
		if err := s.a.Run(nil, "usermod", flag, r, s.u.name); err != nil {
			return err
		}
	}
	return nil
}

// subIDs says whether name has a range in an /etc/subuid-like file, and
// where a new range of subIDCount can start without overlapping any.
func subIDs(path, name string) (bool, int, error) {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, 0, err
	}
	next := subIDFirst
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) != 3 {
			continue
		}
		if f[0] == name {
			return true, 0, nil
		}
		start, err1 := strconv.Atoi(f[1])
		n, err2 := strconv.Atoi(f[2])
		if err1 == nil && err2 == nil && start+n > next {
			next = start + n
		}
	}
	return false, next, nil
}

// helper: the driver's privileged helper serves the gateway's user.
func (s *setup) helper() error {
	entries, _ := os.ReadDir(s.a.Path(HelperDropInDir))
	for _, e := range entries {
		if e.Name() == filepath.Base(HelperDropIn) {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(s.a.Path(HelperDropInDir), e.Name()))
		for _, line := range strings.Split(string(b), "\n") {
			if _, v, ok := strings.Cut(line, "OPENSHELL_SYSTEMD_HELPER_ALLOW_USER="); ok && strings.TrimSpace(v) != s.u.name {
				return fmt.Errorf("%s/%s lets the helper serve %s; the helper serves one user: remove that, or use --user %s", HelperDropInDir, e.Name(), strings.TrimSpace(v), strings.TrimSpace(v))
			}
		}
	}
	changed, err := s.write(HelperDropIn, []byte("# agent-admin openshell-setup: the user OpenShell's gateway runs as.\n[Service]\nEnvironment=OPENSHELL_SYSTEMD_HELPER_ALLOW_USER="+s.u.name+"\n"), 0o644)
	if err != nil {
		return err
	}
	if !changed {
		if s.a.Probe("systemctl", "is-active", "--quiet", "openshell-systemd-helper.service") == nil {
			s.say("ok", "the driver's helper serves "+s.u.name+" and runs")
			return nil
		}
		s.change("the driver's helper started")
		if s.o.Check {
			return nil
		}
		if err := s.a.Run(nil, "systemctl", "enable", "--now", "openshell-systemd-helper.service"); err != nil {
			return err
		}
		return s.helperRuns()
	}
	s.change("the driver's helper serves " + s.u.name + " (" + HelperDropIn + ")")
	if s.o.Check {
		return nil
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "openshell-systemd-helper.service"}, {"restart", "openshell-systemd-helper.service"}} {
		if err := s.a.Run(nil, "systemctl", args...); err != nil {
			return err
		}
	}
	return s.helperRuns()
}

// helperRuns waits for the helper's socket: systemctl's start succeeds
// as soon as the helper is started (Type=exec), also when it exits at
// once, and the driver then fails for want of it. The helper's journal
// says why (the driver's says only that there is no helper).
func (s *setup) helperRuns() error {
	return s.waitFor(HelperSocket, 15, "journalctl -u openshell-systemd-helper.service (the driver's helper does not run)")
}

func (s *setup) driverSocket() string { return s.runtimeDir() + "/openshell/systemd-driver.sock" }

// driver: the driver as the gateway's user service, with the CA its
// sandboxes' supervisors verify the gateway with.
func (s *setup) driver() error {
	ca := s.userConfig("gateways", s.o.Gateway, "mtls", "ca.crt")
	if !s.has(ca) {
		return fmt.Errorf("%s: the gateway's CA is not there; OpenShell's installer puts it there for the gateway %q", ca, s.o.Gateway)
	}
	dropIn := filepath.Join(s.u.home, ".config/systemd/user/openshell-driver-systemd.service.d/custos.conf")
	content := fmt.Sprintf("# agent-admin openshell-setup\n[Service]\nEnvironment=OPENSHELL_SYSTEMD_TLS_CA=%s\nEnvironment=OPENSHELL_GATEWAY_NAME=%s\n", ca, s.o.Gateway)
	changed, err := s.writeHome(dropIn, []byte(content), 0o644)
	if err != nil {
		return err
	}
	active := !changed && s.a.Probe("runuser", "-u", s.u.name, "--", "env", "XDG_RUNTIME_DIR="+s.runtimeDir(), "systemctl", "--user", "is-active", "--quiet", "openshell-driver-systemd.service") == nil
	if active {
		s.say("ok", "the systemd compute driver runs as "+s.u.name)
		return nil
	}
	s.change("the systemd compute driver as " + s.u.name + "'s service, verifying the gateway with " + ca)
	if s.o.Check {
		return nil
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "openshell-driver-systemd.service"}, {"restart", "openshell-driver-systemd.service"}} {
		if err := s.userctl(args...); err != nil {
			return err
		}
	}
	return s.waitFor(s.driverSocket(), 30, "journalctl --user -u openshell-driver-systemd as "+s.u.name)
}

func (s *setup) waitFor(path string, tries int, journal string) error {
	for range tries {
		if s.has(path) {
			return nil
		}
		s.sleep(time.Second)
	}
	return fmt.Errorf("%s did not appear: see %s", path, journal)
}

func (s *setup) sleep(d time.Duration) {
	if s.a.Sleep != nil {
		s.a.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (s *setup) userCA() string { return s.userConfig("mcp-gateway-ca.pem") }

// ca: mcp-gateway's CA where the supervisors read it. The container
// driver hands gateway.toml's additional_ca_bundle to each sandbox's
// supervisor; OpenShell refuses that with an external driver, whose
// supervisors run on the host and trust its store.
func (s *setup) ca() error {
	if s.o.CA == "" {
		return nil
	}
	b, err := os.ReadFile(s.o.CA)
	if err != nil {
		return err
	}
	if s.o.Driver == "container" {
		changed, err := s.writeHome(s.userCA(), b, 0o644)
		if err != nil {
			return err
		}
		if changed {
			s.change("mcp-gateway's CA for the sandboxes' supervisors: " + s.userCA())
		} else {
			s.say("ok", "mcp-gateway's CA: "+s.userCA())
		}
		return nil
	}
	changed, err := s.write(TrustAnchor, b, 0o644)
	if err != nil {
		return err
	}
	if !changed {
		s.say("ok", "mcp-gateway's CA in the host's trust store: "+TrustAnchor)
		return nil
	}
	s.change("mcp-gateway's CA in the host's trust store: " + TrustAnchor + " (sandboxes that run take it at their next start)")
	if s.o.Check {
		return nil
	}
	return s.a.Run(nil, "update-ca-certificates")
}

// gatewayConfig: gateway.toml names the driver (and the CA bundle for
// the container driver); the gateway restarts when it changed.
func (s *setup) gatewayConfig() error {
	path := s.userConfig("gateway.toml")
	b, err := s.readHome(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(b)
	if v, ok := getTOML(text, "openshell", "version"); ok && v != "2" {
		return fmt.Errorf("%s: [openshell] version = %s; migrate it to version 2 first (OpenShell's gateway configuration documentation)", path, v)
	}
	text = setTOML(text, "openshell", "version", "2")
	switch s.o.Driver {
	case "systemd":
		text = setTOML(text, "openshell.gateway", "compute_driver", tomlString("systemd"))
		text = setTOML(text, "openshell.drivers.systemd", "socket_path", tomlString(s.driverSocket()))
		text = unsetTOML(text, "openshell.supervisor.network", "additional_ca_bundle")
	case "container":
		// Unset, OpenShell detects the runtime itself: left so. Set only
		// when the gateway used the systemd driver.
		if cur, _ := getTOML(text, "openshell.gateway", "compute_driver"); cur == tomlString("systemd") {
			text = setTOML(text, "openshell.gateway", "compute_driver", tomlString(s.runtime))
		}
		if s.o.CA != "" {
			text = setTOML(text, "openshell.supervisor.network", "additional_ca_bundle", tomlString(s.userCA()))
		}
	}
	changed, err := s.writeHome(path, []byte(text), 0o600)
	if err != nil {
		return err
	}
	if !changed {
		s.say("ok", path+" names the "+s.o.Driver+" driver")
		return nil
	}
	if cur, _ := getTOML(string(b), "openshell.gateway", "compute_driver"); cur != "" && cur != tomlString(map[string]string{"systemd": "systemd", "container": s.runtime}[s.o.Driver]) {
		s.say("note", "the gateway used "+cur+": sandboxes that driver made stay its; delete them (agent-admin delete, or create them again)")
	}
	s.change(path + ": the " + s.o.Driver + " driver; OpenShell's gateway restarts")
	if s.o.Check {
		return nil
	}
	return s.userctl("restart", "openshell-gateway.service")
}

// registration: root's copy of the gateway's registration, as root's own
// files: agent-admin and the relays run the openshell CLI as root, and the
// relay reads it in its SELinux domain, which may not read another user's
// files (no dac_override).
func (s *setup) registration() error {
	files, err := s.homeFiles(s.userConfig())
	if err != nil {
		return err
	}
	var differs []string
	for rel, a := range files {
		b, err := os.ReadFile(filepath.Join(s.a.Path(RootConfig), rel))
		if err != nil || !bytes.Equal(a, b) {
			differs = append(differs, rel)
		}
	}
	sort.Strings(differs)
	if len(differs) == 0 {
		s.say("ok", "OpenShell's gateway registered for root ("+RootConfig+")")
	} else {
		s.change(fmt.Sprintf("OpenShell's gateway registered for root (%s: %d file(s) from %s)", RootConfig, len(differs), s.userConfig()))
		if s.o.Check {
			return nil
		}
		// Root's own files, readable by root only, whatever their mode in
		// the user's home.
		for _, rel := range differs {
			dst := filepath.Join(s.a.Path(RootConfig), rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return err
			}
			if err := host.WriteFile(dst, files[rel], 0o600); err != nil {
				return err
			}
		}
	}
	if s.o.Check {
		return nil
	}
	if err := s.a.Run(nil, "chown", "-R", "root:root", RootConfig); err != nil {
		return err
	}
	// The label the relay may read (agent_openshell_home_t, custos.fc), on
	// what was copied and what the openshell CLI wrote there before.
	if s.program("restorecon") {
		return s.a.Run(nil, "restorecon", "-R", RootConfig)
	}
	return nil
}

// verify: root's openshell reaches the gateway, which names the driver.
func (s *setup) verify() error {
	var err error
	for range 30 {
		if err = s.a.Probe("openshell", "-g", s.o.Gateway, "status"); err == nil {
			break
		}
		s.sleep(2 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("openshell -g %s status: the gateway does not answer root (see journalctl --user -u openshell-gateway as %s)", s.o.Gateway, s.u.name)
	}
	s.say("ok", "OpenShell's gateway "+s.o.Gateway+" answers root")
	if s.o.Driver != "systemd" {
		return nil
	}
	if s.a.Output == nil {
		return nil
	}
	out, err := s.a.Output(nil, "openshell", "-g", s.o.Gateway, "gateway", "info")
	if err != nil || !bytes.Contains(out, []byte("openshell-driver-systemd")) {
		return fmt.Errorf("openshell -g %s gateway info does not name the systemd compute driver", s.o.Gateway)
	}
	s.say("ok", "OpenShell's gateway uses the systemd compute driver")
	return nil
}

// next says what an agent's configuration and agent-admin need.
func (s *setup) next() {
	fmt.Fprintf(s.a.Out, "\nOpenShell is set up for agents. In agent.yaml:\n  sandbox: {kind: openshell, openshell_gateway: %s, openshell_driver: %s}\n", s.o.Gateway, s.o.Driver)
	if s.o.Driver == "container" && s.runtime == "podman" && s.u.uid != 0 {
		fmt.Fprintf(s.a.Out, "and for agent-admin create and edit:\n  export CUSTOS_OPENSHELL_USER=%s\n", s.u.name)
	}
	if s.o.CA == "" {
		fmt.Fprintln(s.a.Out, "No --ca: mcp-gateway's certificate must then be one a public CA signed.")
	}
}
