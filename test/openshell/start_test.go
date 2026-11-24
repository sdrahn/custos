// Package openshell checks agent-openshell-start, the relay unit's
// ExecStartPre, against a fake openshell CLI that plays through the
// sandbox phases a restart sees.
package openshell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const script = "../../packaging/openshell/agent-openshell-start"

// fake is an openshell CLI: each "sandbox get" answers the next line of
// phases (the last one repeats) in openshell's JSON, each "sandbox
// start" the next line of starts ("ok" or a refusal; the last repeats).
// It logs its calls.
const fake = `#!/bin/sh
d=$FAKE_DIR
next() { # file: its first line, removed unless it is the last
	l=$(head -n 1 "$d/$1")
	[ "$(wc -l <"$d/$1")" -gt 1 ] && sed -i 1d "$d/$1"
	echo "$l"
}
echo "$* timeout=${OPENSHELL_LIFECYCLE_TIMEOUT:-}" >>"$d/calls"
case "$3 $4" in
"sandbox get")
	p=$(next phases)
	[ "$p" = none ] && { echo "sandbox not found" >&2; exit 1; }
	printf '{\n  "name": "%s",\n  "provisioning": {\n    "phase": "admission"\n  },\n  "phase": "%s"\n}\n' "$5" "$p"
	;;
"sandbox start")
	s=$(next starts)
	[ "$s" = ok ] && { echo "Started sandbox $5"; exit 0; }
	# As openshell 0.1 once did: the sandbox is Ready, the start does not
	# see it and waits out its timeout.
	[ "$s" = hang ] && { sleep "${OPENSHELL_LIFECYCLE_TIMEOUT:-300}"; echo "timed out after ${OPENSHELL_LIFECYCLE_TIMEOUT:-300}s waiting for sandbox to reach Ready" >&2; exit 1; }
	echo "$s" >&2
	exit 1
	;;
esac
`

type run struct {
	out   string
	err   error
	calls []string
}

func start(t *testing.T, phases, starts []string, env ...string) run {
	t.Helper()
	d := t.TempDir()
	bin := filepath.Join(d, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"bin/openshell": fake,
		"phases":        strings.Join(phases, "\n") + "\n",
		"starts":        strings.Join(starts, "\n") + "\n",
	} {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", script, "osh", "agent-sb")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_DIR="+d, "AGENT_START_STEP=1")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	b, _ := os.ReadFile(filepath.Join(d, "calls"))
	return run{string(out), err, strings.Split(strings.TrimSpace(string(b)), "\n")}
}

func count(calls []string, verb string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, "-g osh sandbox "+verb+" agent-sb") {
			n++
		}
	}
	return n
}

func TestReadyNeedsNoStart(t *testing.T) {
	r := start(t, []string{"Ready"}, []string{"ok"})
	if r.err != nil || count(r.calls, "start") != 0 {
		t.Fatalf("%v, calls %q: %s", r.err, r.calls, r.out)
	}
}

func TestStoppedIsStarted(t *testing.T) {
	r := start(t, []string{"Stopped"}, []string{"ok"})
	if r.err != nil || count(r.calls, "start") != 1 {
		t.Fatalf("%v, calls %q: %s", r.err, r.calls, r.out)
	}
}

// The race of a restart: ExecStopPost's stop is still finishing when the
// unit starts again. No start is asked while it is Stopping (OpenShell
// would refuse it, and the old unit ignored that refusal).
func TestWaitsForAStopInProgress(t *testing.T) {
	r := start(t, []string{"Stopping", "Stopping", "Stopped"}, []string{"ok"})
	if r.err != nil {
		t.Fatalf("%v: %s", r.err, r.out)
	}
	if count(r.calls, "get") != 3 || count(r.calls, "start") != 1 {
		t.Fatalf("calls %q", r.calls)
	}
}

func TestRetriesARefusedStart(t *testing.T) {
	r := start(t, []string{"Stopped", "Stopped", "Ready"},
		[]string{"previous driver operation is still pending; retry and deletion are blocked", "ok"})
	if r.err != nil {
		t.Fatalf("%v: %s", r.err, r.out)
	}
	if n := count(r.calls, "start"); n != 1 && n != 2 {
		t.Fatalf("calls %q", r.calls)
	}
}

func TestStartingBecomesReady(t *testing.T) {
	r := start(t, []string{"Starting", "Ready"}, []string{"ok"})
	if r.err != nil || count(r.calls, "start") != 0 {
		t.Fatalf("%v, calls %q: %s", r.err, r.calls, r.out)
	}
}

func TestFailsWhenNeverReady(t *testing.T) {
	for _, c := range []struct {
		phases, starts []string
		says           string
	}{
		{[]string{"Stopping"}, []string{"ok"}, "sandbox agent-sb is Stopping"},
		{[]string{"Stopped"}, []string{"sandbox must be Stopped, Suspended, Completed"}, "sandbox must be Stopped"},
		{[]string{"none"}, []string{"ok"}, "openshell does not know sandbox agent-sb"},
	} {
		r := start(t, c.phases, c.starts, "AGENT_START_SECONDS=2")
		if r.err == nil {
			t.Errorf("%v: succeeded: %s", c.phases, r.out)
			continue
		}
		if !strings.Contains(r.out, "is not Ready after 2s") || !strings.Contains(r.out, c.says) {
			t.Errorf("%v: %q, want %q", c.phases, r.out, c.says)
		}
	}
}

// A start that never sees Ready (the sandbox is Ready all the same) is
// bounded, and the phase decides: the unit starts instead of timing out.
func TestStartThatMissesReady(t *testing.T) {
	t0 := time.Now()
	r := start(t, []string{"Stopped", "Ready"}, []string{"hang"}, "AGENT_START_CALL_SECONDS=1")
	if r.err != nil {
		t.Fatalf("%v: %s", r.err, r.out)
	}
	if d := time.Since(t0); d > 10*time.Second {
		t.Errorf("took %s", d)
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "sandbox start agent-sb timeout=1") {
		t.Errorf("the start's own wait is not bounded: %q", r.calls)
	}
}

// The limit is on the clock, so a start that hangs cannot take the unit
// past it.
func TestLimitCountsTheStartsTime(t *testing.T) {
	t0 := time.Now()
	r := start(t, []string{"Stopped"}, []string{"hang"}, "AGENT_START_SECONDS=3", "AGENT_START_CALL_SECONDS=2")
	if r.err == nil || !strings.Contains(r.out, "is not Ready after 3s") || !strings.Contains(r.out, "timed out after 2s") {
		t.Fatalf("%v: %s", r.err, r.out)
	}
	if d := time.Since(t0); d > 12*time.Second {
		t.Errorf("took %s, the limit is 3s", d)
	}
}

func TestUnitUsesIt(t *testing.T) {
	b, err := os.ReadFile("../../systemd/agent-openshell@.service")
	if err != nil {
		t.Fatal(err)
	}
	unit := string(b)
	// Not "-": a sandbox that does not come up fails the unit.
	if !strings.Contains(unit, "\nExecStartPre=/usr/libexec/custos/agent-openshell-start ${OPENSHELL_GATEWAY} ${OPENSHELL_SANDBOX}\n") {
		t.Error("the unit does not start the sandbox with agent-openshell-start")
	}
	if strings.Contains(unit, "sandbox start") {
		t.Error("the unit still runs openshell sandbox start itself")
	}
	if !strings.Contains(unit, "\nTimeoutStartSec=180\n") {
		t.Error("TimeoutStartSec does not cover the helper's 120s")
	}
}
