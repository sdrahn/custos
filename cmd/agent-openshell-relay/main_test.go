package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The log command learns where the last stream ended, and the relay's
// stop does not signal it (systemd ends it with the unit, #221): a
// cancelled context leaves it running.
func TestLogCommand(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	ctx, cancel := context.WithCancel(context.Background())
	since := time.Date(2026, 11, 18, 12, 0, 0, 5, time.FixedZone("x", 3600))
	cmd := logCommand(ctx, `echo "$AGENT_LOGS_SINCE" >`+out+`; sleep 2; echo done >>`+out, since)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	cmd.Wait()
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(b)); len(got) != 2 || got[0] != "2026-11-18T11:00:00.000000005Z" || got[1] != "done" {
		t.Errorf("the command wrote %q: want the time in UTC, then done (not stopped by the relay)", b)
	}
}
