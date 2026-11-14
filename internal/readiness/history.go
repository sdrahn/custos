package readiness

// An agent's permissions over time: a call counts for the category its
// tool was in when it ran (O11).

import (
	"encoding/json"
	"sort"
	"time"
)

// AgentsStopped is root's record of the emergency stop (agent-admin
// stop-all; §8, §10): an approved call whose activity it stopped is an
// incident. AgentsReleased is its end.
const (
	AgentsStopped  = "agents.stopped"
	AgentsReleased = "agents.released"
)

// PermissionsChanged is root's record of a change of an agent's
// permissions (agent-admin; §10), with the permissions before and after
// as JSON.
const PermissionsChanged = "permissions.changed"

// History is an agent's permissions now and their changes, oldest first:
// which permissions were in force at a time (O11).
type History struct {
	Current []map[string]any
	Changes []Change
}

// Change is one change of the permissions.
type Change struct {
	At       time.Time
	From, To []map[string]any
}

// Now is a history without changes: the current permissions at any time.
func Now(perms []map[string]any) History { return History{Current: perms} }

// NewHistory is the current permissions and the changes root recorded
// among recs (permissions.changed with _UID 0; ByRoot). An agent's own
// record of a change is none.
func NewHistory(current []map[string]any, recs []Record) History {
	h := History{Current: current}
	for _, r := range recs {
		if r["AGENT_EVENT"] != PermissionsChanged || !ByRoot(r) {
			continue
		}
		var c Change
		if json.Unmarshal([]byte(r["AGENT_PERMISSIONS_FROM"]), &c.From) != nil ||
			json.Unmarshal([]byte(r["AGENT_PERMISSIONS_TO"]), &c.To) != nil {
			continue
		}
		c.At = At(r)
		h.Changes = append(h.Changes, c)
	}
	sort.SliceStable(h.Changes, func(i, j int) bool { return h.Changes[i].At.Before(h.Changes[j].At) })
	return h
}

// At is the permissions in force at t: those after the last change
// before t; before the first change read, those it changed from; without
// changes, the current ones.
func (h History) At(t time.Time) []map[string]any {
	if len(h.Changes) == 0 {
		return h.Current
	}
	if t.Before(h.Changes[0].At) {
		return h.Changes[0].From
	}
	in := h.Changes[0].To
	for _, c := range h.Changes[1:] {
		if t.Before(c.At) {
			break
		}
		in = c.To
	}
	return in
}

// InCategory is InCategory with the permissions in force at t.
func (h History) InCategory(category, tool string, t time.Time) bool {
	return InCategory(category, tool, h.At(t))
}
