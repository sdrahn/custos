package admin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/journal"
)

// Events (CONTRACT.md §14, §15): an agent's event names systemd units;
// agent-admin puts an OnFailure= drop-in on each of them, which starts
// agent-event@<agent>_<event>_<unit>.service when the unit fails. That
// unit runs agent-admin event, which starts the event's activity on the
// agent's socket, as root, with origin event, unless it is damped.

// EventStateDir holds when each event last started an activity for a
// unit (agent-event@.service's RuntimeDirectory: gone at boot).
const EventStateDir = "/run/custos/events"

// eventUnit is the unit an event's drop-in starts for a unit.
func eventUnit(agent, event, unit string) string {
	return "agent-event@" + agent + "_" + event + "_" + unit + ".service"
}

// eventDropIn is the name of an event's drop-in on the units it
// watches. An agent name has no "_", so the agent's drop-ins are
// 50-custos-event-<agent>_*.conf and no other agent's.
func eventDropIn(agent, event string) string {
	return "50-custos-event-" + agent + "_" + event + ".conf"
}

// syncEvents writes the drop-ins of the agent's events on the units they
// watch and removes the ones that are no longer wanted. systemd reads them
// at the next daemon-reload, which it runs when anything changed.
func (a *Admin) syncEvents(cfg *config.Config) error {
	want := map[string]string{} // drop-in path → content
	for _, e := range cfg.Events {
		for _, u := range e.OnUnitFailure {
			path := a.Path(filepath.Join(UnitDir, u+".d", eventDropIn(cfg.Name, e.Name)))
			want[path] = fmt.Sprintf("# agent-admin: event %s of agent %s (CONTRACT.md §15)\n[Unit]\nOnFailure=%s\n",
				e.Name, cfg.Name, eventUnit(cfg.Name, e.Name, u))
		}
	}
	changed := false
	for path, conf := range want {
		if b, err := os.ReadFile(path); err == nil && string(b) == conf {
			continue
		}
		if err := host.WriteFile(path, []byte(conf), 0o644); err != nil {
			return err
		}
		changed = true
	}
	old, _ := filepath.Glob(a.Path(filepath.Join(UnitDir, "*.d", "50-custos-event-"+cfg.Name+"_*.conf")))
	for _, path := range old {
		if _, keep := want[path]; keep {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		os.Remove(filepath.Dir(path)) // only when empty: other drop-ins stay
		changed = true
	}
	if changed {
		return a.Run(nil, "systemctl", "daemon-reload")
	}
	return nil
}

// removeEvents removes an agent's event drop-ins, their directories
// when nothing else is in them, and the events' state (delete).
func (a *Admin) removeEvents(name string) {
	drops, _ := filepath.Glob(a.Path(filepath.Join(UnitDir, "*.d", "50-custos-event-"+name+"_*.conf")))
	for _, p := range drops {
		os.Remove(p)
		os.Remove(filepath.Dir(p)) // only when empty
	}
	state, _ := filepath.Glob(a.Path(filepath.Join(EventStateDir, name+"_*")))
	for _, p := range state {
		os.Remove(p)
	}
}

// Event starts an event's activity for the unit that failed: instance
// is <agent>_<event>_<unit>, as agent-event@.service gets it. It is
// damped: no new activity for the same event and unit while one it
// started runs, nor within the event's minimum interval after the last
// it started. A skipped event is recorded (event.skipped).
func (a *Admin) Event(instance string) error {
	name, rest, _ := strings.Cut(instance, "_")
	event, unit, ok := strings.Cut(rest, "_")
	if !ok || host.CheckName(name) != nil || event == "" || unit == "" {
		return fmt.Errorf("%q: want AGENT_EVENT_UNIT", instance)
	}
	cfg, err := config.Load(a.AgentDir(name))
	if err != nil {
		return err
	}
	var ev *config.Event
	for i := range cfg.Events {
		if cfg.Events[i].Name == event {
			ev = &cfg.Events[i]
		}
	}
	if ev == nil || !contains(ev.OnUnitFailure, unit) {
		// A drop-in agent-admin should have removed: say so, start nothing.
		return fmt.Errorf("agent %s has no event %q for the unit %s (agent-admin edit %s rewrites its drop-ins)", name, event, unit, name)
	}
	by := "unit-failure:" + unit
	skip := func(reason string) error {
		a.Record(journal.EventSkipped, name, fmt.Sprintf("Event %s of agent %s for %s skipped: %s", event, name, unit, reason),
			journal.Fields{"ORIGIN_EVENT": event, "ORIGIN_BY": by, "REASON": reason})
		fmt.Fprintf(a.Out, "event %s of agent %s for %s skipped: %s\n", event, name, unit, reason)
		return nil
	}
	state := a.Path(filepath.Join(EventStateDir, instance))
	now := a.Now()
	if b, err := os.ReadFile(state); err == nil {
		if last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil && now.Sub(last) < ev.MinInterval() {
			return skip(fmt.Sprintf("it started an activity for %s at %s, within its minimum interval (%s)",
				unit, last.Format(time.RFC3339), ev.MinInterval()))
		}
	}
	sock := filepath.Join(SocketDir, name, "api.sock")
	if a.Activities != nil {
		// Best effort: an agent that cannot answer refuses the start below.
		if acts, err := a.Activities(sock); err == nil {
			for _, act := range acts {
				o, _ := act["origin"].(map[string]any)
				if _, ended := act["ended"]; !ended && o != nil && o["kind"] == "event" && o["by"] == by && o["event"] == event {
					return skip(fmt.Sprintf("its activity %v for %s still runs", act["id"], unit))
				}
			}
		}
	}
	prompt := strings.ReplaceAll(ev.Prompt, "{unit}", unit)
	if r := a.getenv("MONITOR_SERVICE_RESULT"); r != "" {
		// systemd's account of the failure (systemd.exec(5), OnFailure=).
		detail := "result " + r
		if c := a.getenv("MONITOR_EXIT_CODE"); c != "" {
			detail += ", " + c + " " + a.getenv("MONITOR_EXIT_STATUS")
		}
		prompt = strings.TrimSpace(prompt + "\n\n(systemd: " + unit + " failed: " + strings.TrimSpace(detail) + ".)")
	}
	body := map[string]any{
		"prompt": prompt,
		"origin": map[string]any{"kind": "event", "by": by, "event": event},
	}
	if ev.Playbook != "" {
		body["playbook"], body["unit"] = ev.Playbook, unit
	}
	if err := a.Start(sock, body); err != nil {
		a.Record(journal.EventSkipped, name, fmt.Sprintf("Event %s of agent %s for %s: the agent did not start it: %v", event, name, unit, err),
			journal.Fields{"ORIGIN_EVENT": event, "ORIGIN_BY": by, "REASON": "the agent did not start it: " + err.Error()})
		return err
	}
	return host.WriteFile(state, []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o600)
}

func (a *Admin) getenv(k string) string {
	if a.Getenv != nil {
		return a.Getenv(k)
	}
	return os.Getenv(k)
}
