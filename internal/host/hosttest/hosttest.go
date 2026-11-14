// Package hosttest is a host for tests: files below a temporary
// directory, commands recorded instead of run, a fixed clock, and root's
// records in memory.
package hosttest

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/journal"
)

// Rec is what a test host was asked to do, and how it answers.
type Rec struct {
	// Cmds are the commands run, each as one line.
	Cmds []string
	// Fail makes the commands it reports fail (systemctl is-active, say).
	Fail func(cmd string) bool
	// Gone answers the probes (openshell sandbox get): nil when the
	// sandbox is there. Without it, every probe finds nothing.
	Gone func(cmd string) error
	// Journal holds root's records.
	Journal journal.Memory
	// Credential, when set, is a credential systemd-creds keeps for the
	// test: "encrypted" as it is, and decrypted again (the seal's key).
	Credential string
}

// Now is the test host's time.
var Now = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// New is a test host and its record.
func New(t *testing.T) (host.Host, *Rec) {
	t.Helper()
	r := &Rec{}
	h := host.Host{
		Root: t.TempDir(),
		Run: func(stdin io.Reader, name string, args ...string) error {
			c := name + " " + strings.Join(args, " ")
			r.Cmds = append(r.Cmds, c)
			if r.Fail != nil && r.Fail(c) {
				return errors.New("failed")
			}
			if r.Credential != "" && name == "systemd-creds" && len(args) == 4 && args[0] == "encrypt" && args[1] == "--name="+r.Credential {
				b, _ := io.ReadAll(stdin)
				return os.WriteFile(args[3], b, 0o600)
			}
			return nil
		},
		Output: func(_ io.Reader, name string, args ...string) ([]byte, error) {
			r.Cmds = append(r.Cmds, name+" "+strings.Join(args, " "))
			if name == "systemd-creds" && len(args) == 4 && args[0] == "decrypt" {
				return os.ReadFile(args[2])
			}
			return nil, errors.New("no output of " + name)
		},
		Out: io.Discard, Now: func() time.Time { return Now },
		RunEnv: func(env []string, name string, args ...string) error {
			r.Cmds = append(r.Cmds, strings.Join(env, " ")+" "+name+" "+strings.Join(args, " "))
			return nil
		},
		Probe: func(name string, args ...string) error {
			c := name + " " + strings.Join(args, " ")
			r.Cmds = append(r.Cmds, c)
			if r.Gone != nil {
				return r.Gone(c)
			}
			return errors.New("not found")
		},
		Sleep: func(time.Duration) {},
		// No records: nothing observed yet.
		JournalJSON: func(...string) ([]byte, error) { return nil, nil },
		Journal:     func(string) journal.Logger { return &r.Journal },
	}
	return h, r
}
