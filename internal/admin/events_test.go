package admin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/journal"
)

const eventYml = `version: 1
name: patch-pilot
model: {provider: openai, name: m, endpoint: "http://ai:8000/v1"}
gateway: {servers: [zypp]}
events:
  - {name: web-down, on_unit_failure: [nginx.service, php-fpm.service], prompt: "The unit {unit} failed.", min_interval_seconds: 600}
`

// eventAdmin has patch-pilot with the event web-down, and records the
// activities it starts.
func eventAdmin(t *testing.T) (*Admin, *rec, *[]map[string]any) {
	a, r := newAdmin(t)
	f := filepath.Join(t.TempDir(), "a.yaml")
	os.WriteFile(f, []byte(eventYml), 0o644)
	if err := a.Create("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	var started []map[string]any
	a.Start = func(sock string, body map[string]any) error {
		if sock != "/run/agents/patch-pilot/api.sock" {
			t.Errorf("socket %s", sock)
		}
		started = append(started, body)
		return nil
	}
	a.Getenv = func(string) string { return "" }
	return a, r, &started
}

// create puts an OnFailure= drop-in on each unit an event names; edit
// rewrites them and removes those of units no longer named; delete
// removes the rest, and leaves the administrator's own drop-ins.
func TestEventDropIns(t *testing.T) {
	a, r, _ := eventAdmin(t)
	read := func(p string) string { b, _ := os.ReadFile(a.Path(p)); return string(b) }
	nginx := "/etc/systemd/system/nginx.service.d/50-custos-event-patch-pilot_web-down.conf"
	if s := read(nginx); !strings.Contains(s, "[Unit]\nOnFailure=agent-event@patch-pilot_web-down_nginx.service.service\n") {
		t.Errorf("nginx drop-in %q", s)
	}
	if s := read("/etc/systemd/system/php-fpm.service.d/50-custos-event-patch-pilot_web-down.conf"); !strings.Contains(s, "OnFailure=agent-event@patch-pilot_web-down_php-fpm.service.service") {
		t.Errorf("php-fpm drop-in %q", s)
	}
	if !strings.Contains(strings.Join(r.cmds, "\n"), "systemctl daemon-reload") {
		t.Error("no daemon-reload")
	}
	// The administrator's own drop-in on nginx stays through all of it.
	own := a.Path("/etc/systemd/system/nginx.service.d/override.conf")
	os.WriteFile(own, []byte("[Service]\nRestart=on-failure\n"), 0o644)

	f := filepath.Join(t.TempDir(), "b.yaml")
	os.WriteFile(f, []byte(strings.Replace(eventYml, "[nginx.service, php-fpm.service]", "[php-fpm.service]", 1)), 0o644)
	if err := a.Edit("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path(nginx)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the drop-in of a unit no longer named stays: %v", err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Errorf("the administrator's drop-in: %v", err)
	}
	if err := a.Delete("patch-pilot", false); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(a.Path("/etc/systemd/system/*.d/50-custos-event-*"))
	if len(left) != 0 {
		t.Errorf("left after delete: %v", left)
	}
	if _, err := os.Stat(a.Path("/etc/systemd/system/php-fpm.service.d")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an emptied drop-in directory stays: %v", err)
	}
}

// An event that runs a playbook names the unit that failed, for the
// playbook's steps that need one (§14c).
func TestEventPlaybookUnit(t *testing.T) {
	a, _, started := eventAdmin(t)
	f := filepath.Join(t.TempDir(), "b.yaml")
	os.WriteFile(f, []byte(strings.Replace(eventYml, `prompt: "The unit {unit} failed.",`, `playbook: service-health,`, 1)), 0o644)
	if err := a.Edit("patch-pilot", f); err != nil {
		t.Fatal(err)
	}
	if err := a.Event("patch-pilot_web-down_php-fpm.service"); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 || (*started)[0]["playbook"] != "service-health" || (*started)[0]["unit"] != "php-fpm.service" {
		t.Errorf("started %v", *started)
	}
}

// An event starts the activity as root with origin event, its {unit}
// filled in and systemd's account of the failure added; then it is
// damped for that unit within the interval, not for another unit.
func TestEventStartsAndDamps(t *testing.T) {
	a, r, started := eventAdmin(t)
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	a.Getenv = func(k string) string {
		return map[string]string{"MONITOR_SERVICE_RESULT": "exit-code", "MONITOR_EXIT_CODE": "exited", "MONITOR_EXIT_STATUS": "1"}[k]
	}
	if err := a.Event("patch-pilot_web-down_nginx.service"); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 {
		t.Fatalf("started %v", *started)
	}
	b := (*started)[0]
	o := b["origin"].(map[string]any)
	if o["kind"] != "event" || o["by"] != "unit-failure:nginx.service" || o["event"] != "web-down" {
		t.Errorf("origin %v", o)
	}
	if b["prompt"] != "The unit nginx.service failed.\n\n(systemd: nginx.service failed: result exit-code, exited 1.)" {
		t.Errorf("prompt %q", b["prompt"])
	}
	if _, ok := b["unit"]; ok || b["playbook"] != nil {
		t.Errorf("without a playbook, no unit: %v", b)
	}
	if s, _ := os.ReadFile(a.Path(filepath.Join(EventStateDir, "patch-pilot_web-down_nginx.service"))); !strings.HasPrefix(string(s), "2026-11-07T10:00:00Z") {
		t.Errorf("state %q", s)
	}

	// Within the interval: skipped and recorded, for the same unit only.
	now = now.Add(5 * time.Minute)
	if err := a.Event("patch-pilot_web-down_nginx.service"); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 {
		t.Errorf("damped event started: %v", *started)
	}
	sk := r.journal.Events(journal.EventSkipped)
	if len(sk) != 1 || sk[0].Fields["ORIGIN_EVENT"] != "web-down" || sk[0].Fields["ORIGIN_BY"] != "unit-failure:nginx.service" ||
		!strings.Contains(sk[0].Fields["REASON"], "minimum interval") {
		t.Errorf("event.skipped %+v", sk)
	}
	if err := a.Event("patch-pilot_web-down_php-fpm.service"); err != nil || len(*started) != 2 {
		t.Errorf("another unit is not damped: %v %d", err, len(*started))
	}

	// After the interval, but its activity for that unit still runs.
	now = now.Add(10 * time.Minute)
	a.Activities = func(string) ([]map[string]any, error) {
		return []map[string]any{
			{"id": "e-old", "ended": "2026-11-07T09:00:00Z", "origin": map[string]any{"kind": "event", "by": "unit-failure:nginx.service", "event": "web-down"}},
			{"id": "e-run", "origin": map[string]any{"kind": "event", "by": "unit-failure:nginx.service", "event": "web-down"}},
		}, nil
	}
	if err := a.Event("patch-pilot_web-down_nginx.service"); err != nil || len(*started) != 2 {
		t.Errorf("started while its activity runs: %v %d", err, len(*started))
	}
	if sk := r.journal.Events(journal.EventSkipped); len(sk) != 2 || !strings.Contains(sk[1].Fields["REASON"], "e-run for nginx.service still runs") {
		t.Errorf("event.skipped %+v", sk)
	}
	a.Activities = func(string) ([]map[string]any, error) { return nil, nil }
	if err := a.Event("patch-pilot_web-down_nginx.service"); err != nil || len(*started) != 3 {
		t.Errorf("after the interval, with nothing running: %v %d", err, len(*started))
	}
}

// An agent that refuses (paused, stopped) fails the event's unit and is
// recorded; an instance the configuration does not name starts nothing.
func TestEventRefused(t *testing.T) {
	a, r, started := eventAdmin(t)
	a.Start = func(string, map[string]any) error { return errors.New("agent answered 409: paused") }
	if err := a.Event("patch-pilot_web-down_nginx.service"); err == nil {
		t.Error("a refused start is no error")
	}
	if sk := r.journal.Events(journal.EventSkipped); len(sk) != 1 || !strings.Contains(sk[0].Fields["REASON"], "paused") {
		t.Errorf("event.skipped %+v", sk)
	}
	if _, err := os.Stat(a.Path(filepath.Join(EventStateDir, "patch-pilot_web-down_nginx.service"))); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused start counts toward the interval")
	}
	for _, bad := range []string{"patch-pilot_web-down_sshd.service", "patch-pilot_nope_nginx.service", "patch-pilot_web-down", "../x_y_z.service", "patch-pilot"} {
		if err := a.Event(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if len(*started) != 0 {
		t.Errorf("started %v", *started)
	}
}
