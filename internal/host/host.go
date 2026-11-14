// Package host is the system agent-admin and custos-setup work on: its
// files below a root, the commands they run, the clock, the person, and
// root's journal records. Tests give one of their own; every module of
// agent-admin reaches the system through it.
package host

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/journal"
)

// Host is a system. Its standard output is agent-admin's own messages
// (Out), which a control plane shows; what the programs it runs print
// (systemctl, openshell, …) goes to standard error (CONTRACT.md §17).
type Host struct {
	Root string // "" on a host; a temporary directory in tests
	// Run runs a command (systemctl, systemd-sysusers, systemd-creds).
	Run func(stdin io.Reader, name string, args ...string) error
	// Output runs a command and returns its output (systemd-creds
	// decrypt, the seal's key); nil: run it.
	Output func(stdin io.Reader, name string, args ...string) ([]byte, error)
	Out    io.Writer
	// In is read for a configuration given as "-" (--from -, check -).
	In io.Reader
	// Now is the clock (tests).
	Now func() time.Time
	// Getenv reads the environment (systemd's MONITOR_* for an event);
	// nil: os.Getenv.
	Getenv func(string) string
	// RunEnv runs a command with extra environment variables (secrets
	// handed to openshell, which reads them from its environment).
	RunEnv func(env []string, name string, args ...string) error
	// Probe runs a command for its exit status only, without its output
	// (openshell sandbox get while a sandbox is deleted); nil: Run.
	Probe func(name string, args ...string) error
	// Sleep waits between probes (tests: no wait); nil: time.Sleep.
	Sleep func(time.Duration)
	// Journal gives the promotion records' logger for an agent (§10);
	// nil: journald as custos-promotion.
	Journal func(name string) journal.Logger
	// JournalJSON reads journal records (journalctl -o json with these
	// matches, over the readiness window); nil: journalctl.
	JournalJSON func(args ...string) ([]byte, error)
	// By is the person, for the records of refusals (category.refused).
	By string
}

// New is the host itself.
func New() Host {
	return Host{
		Run: func(stdin io.Reader, name string, args ...string) error {
			c := exec.Command(name, args...)
			c.Stdin, c.Stdout, c.Stderr = stdin, os.Stderr, os.Stderr
			return c.Run()
		},
		Out: os.Stdout, In: os.Stdin, Now: time.Now,
		RunEnv: func(env []string, name string, args ...string) error {
			c := exec.Command(name, args...)
			c.Env = append(os.Environ(), env...)
			c.Stdout, c.Stderr = os.Stderr, os.Stderr
			return c.Run()
		},
		Probe: func(name string, args ...string) error {
			return exec.Command(name, args...).Run()
		},
	}
}

// Path is a path of the system, below Root.
func (h *Host) Path(path string) string { return filepath.Join(h.Root, path) }

// Who is the person for a record: By, else root.
func (h *Host) Who() string {
	if h.By != "" {
		return h.By
	}
	return "root"
}

// Record writes one of root's records about agent name (§10): to
// journald as custos-promotion, or to Journal's logger.
func (h *Host) Record(event, name, msg string, f journal.Fields) {
	l := h.Journal
	if l == nil {
		l = func(name string) journal.Logger { return journal.NewAs("custos-promotion", name, os.Stderr) }
	}
	l(name).Log(event, msg, f)
}

// ReadFrom reads a file, or In for "-" (a configuration or an envelope
// from a control plane).
func (h *Host) ReadFrom(from string) ([]byte, error) {
	if from == "-" {
		if h.In == nil {
			return nil, errors.New("no configuration on stdin")
		}
		return io.ReadAll(io.LimitReader(h.In, 1<<20))
	}
	return os.ReadFile(from)
}

// CheckName refuses what is no agent name (§13).
func CheckName(name string) error {
	if !config.ValidName(name) {
		return fmt.Errorf("%q is not a valid agent name", name)
	}
	return nil
}

// WriteFile writes a file atomically.
func WriteFile(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
