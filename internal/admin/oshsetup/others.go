package oshsetup

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// OpenShell's programs that run as the gateway's user only. Another
// user's copy of them is a second gateway: started as root (systemctl
// --user as root), it makes its own CA and writes its client certificates
// into root's registration, whose copy agent-admin and the relays use, and
// root's openshell then trusts that CA, not the gateway's
// ("invalid peer certificate: BadSignature").
var userOnlyPrograms = []string{"openshell-gateway", "openshell-driver-systemd"}

// userUnits are their user services, as OpenShell's and the driver's
// packages name them.
var userUnits = []string{"openshell-gateway.service", "openshell-driver-systemd.service"}

// others: OpenShell's gateway and driver run as the gateway's user only.
// Where they run as another user too, that user's services are stopped
// and disabled.
func (s *setup) others() error {
	found, err := s.otherRunners()
	if err != nil {
		return err
	}
	if len(found) == 0 {
		s.say("ok", "OpenShell's gateway and driver run as "+s.u.name+" only")
		return nil
	}
	for _, o := range found {
		what := map[int]string{1: "OpenShell's " + o.programs[0] + " also runs", 2: "OpenShell's gateway and driver also run"}[len(o.programs)]
		s.change(fmt.Sprintf("%s as %s: its services stopped and disabled for %s (a second gateway has a CA of its own, and root's registration must trust %s's)",
			what, o.a.name, o.a.name, s.u.name))
		if s.o.Check {
			continue
		}
		args := append([]string{"-u", o.a.name, "--", "env", "HOME=" + o.a.home, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", o.a.uid), "systemctl", "--user", "disable", "--now"}, userUnits...)
		if err := s.a.Run(nil, "runuser", args...); err != nil {
			return fmt.Errorf("stopping OpenShell's services of %s: %w", o.a.name, err)
		}
	}
	return nil
}

type otherRunner struct {
	a        account
	programs []string
}

// otherRunners are the users other than the gateway's that run one of
// userOnlyPrograms, from /proc: a process's program (exe) and its real
// user (status, Uid). A process that ends while it is read is skipped.
func (s *setup) otherRunners() ([]otherRunner, error) {
	proc := s.a.Path("/proc")
	entries, err := os.ReadDir(proc)
	if os.IsNotExist(err) {
		return nil, nil // no /proc to look into (a test's host without one)
	} else if err != nil {
		return nil, err
	}
	byUID := map[int]map[string]bool{}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		exe, err := os.Readlink(filepath.Join(proc, e.Name(), "exe"))
		if err != nil {
			continue
		}
		prog := filepath.Base(strings.TrimSuffix(exe, " (deleted)"))
		if !slices.Contains(userOnlyPrograms, prog) {
			continue
		}
		uid, ok := realUID(filepath.Join(proc, e.Name(), "status"))
		if !ok || uid == s.u.uid {
			continue
		}
		if byUID[uid] == nil {
			byUID[uid] = map[string]bool{}
		}
		byUID[uid][prog] = true
	}
	var found []otherRunner
	for uid, progs := range byUID {
		a, err := lookupUID(s.a.Path("/etc/passwd"), uid)
		if err != nil {
			return nil, err
		}
		o := otherRunner{a: a}
		for _, p := range userOnlyPrograms {
			if progs[p] {
				o.programs = append(o.programs, p)
			}
		}
		found = append(found, o)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].a.uid < found[j].a.uid })
	return found, nil
}

// realUID is the real user ID in a /proc/<pid>/status.
func realUID(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				return 0, false
			}
			uid, err := strconv.Atoi(f[0])
			return uid, err == nil
		}
	}
	return 0, false
}

// lookupUID is the account of a user ID in /etc/passwd.
func lookupUID(passwd string, uid int) (account, error) {
	b, err := os.ReadFile(passwd)
	if err != nil {
		return account{}, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 || f[2] != strconv.Itoa(uid) {
			continue
		}
		return lookupUser(passwd, f[0])
	}
	return account{}, fmt.Errorf("user ID %d runs OpenShell's gateway or driver and is not in /etc/passwd", uid)
}
