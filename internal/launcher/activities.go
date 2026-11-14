package launcher

// The control API's activities: the list, one, starting one (a prompt, a
// schedule, an event, a playbook, a delegation) and cancelling it
// (CONTRACT.md §10).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/sdrahn/custos/internal/governance"
	"github.com/sdrahn/custos/internal/peercred"
	"github.com/sdrahn/custos/internal/playbook"
)

func (l *Launcher) activities(w http.ResponseWriter, r *http.Request) {
	c, _ := peercred.From(r.Context())
	if _, isAgent := c.Agent(); isAgent {
		http.Error(w, "agents may only read the activities they started", http.StatusForbidden)
		return
	}
	acts := l.Governor.Activities()
	for i := range acts {
		acts[i].Response = "" // GET /v1/activities/{id} has it; the list stays small
	}
	writeJSON(w, http.StatusOK, map[string]any{"activities": nonNil(acts)})
}

func (l *Launcher) activity(w http.ResponseWriter, r *http.Request) {
	c, _ := peercred.From(r.Context())
	a, ok := l.Governor.Activity(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if name, isAgent := c.Agent(); isAgent && (a.Parent == nil || a.Parent.Agent != name) {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// StartRequest is POST /v1/activities.
type StartRequest struct {
	Prompt  string `json:"prompt"`
	Session string `json:"session"`
	// User and Origin may be set by root only (schedules, events).
	User   string            `json:"user"`
	Origin governance.Origin `json:"origin"`
	// Trace is the delegating activity's (agents only).
	Trace *Trace `json:"trace"`
	// Wait: answer when the activity has ended, with the activity.
	Wait bool `json:"wait"`
	// Playbook names a diagnostic playbook to run (CONTRACT.md §14c);
	// Prompt is then optional and added to it.
	Playbook string `json:"playbook"`
	// Unit is the systemd unit the playbook is about: the steps that
	// need one run with it (§14c).
	Unit string `json:"unit"`
}

type Trace struct {
	RootAgent      string `json:"root_agent"`
	RootActivity   string `json:"root_activity"`
	ParentAgent    string `json:"parent_agent"`
	ParentActivity string `json:"parent_activity"`
}

func (l *Launcher) startActivity(w http.ResponseWriter, r *http.Request) {
	c, _ := peercred.From(r.Context())
	var req StartRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || (strings.TrimSpace(req.Prompt) == "" && req.Playbook == "") {
		http.Error(w, "want {\"prompt\": …} or {\"playbook\": …}", http.StatusBadRequest)
		return
	}
	if req.Unit != "" && (req.Playbook == "" || !playbook.ValidUnit(req.Unit)) {
		http.Error(w, "unit: a systemd unit name, with a playbook", http.StatusBadRequest)
		return
	}
	st := governance.Start{}
	prompt := req.Prompt
	if req.Playbook != "" {
		var status int
		var err error
		if prompt, st.Steps, st.Tools, status, err = l.playbookPrompt(r.Context(), req.Playbook, req.Unit, req.Prompt); err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		st.Playbook = req.Playbook
	}
	user := c.Name
	switch name, isAgent := c.Agent(); {
	case isAgent:
		// Delegation: the caller is the delegating agent, by the kernel;
		// its trace is believed only where it names that agent.
		st.Origin = governance.Origin{Kind: "delegation", By: c.Name}
		if t := req.Trace; t != nil && t.ParentAgent == name {
			st.Parent = &governance.Ref{Agent: t.ParentAgent, Activity: t.ParentActivity}
			st.Root = &governance.Ref{Agent: t.RootAgent, Activity: t.RootActivity}
			if t.RootAgent == "" {
				st.Root = st.Parent
			}
		}
	case c.Root():
		st.Origin = req.Origin
		if st.Origin.Kind == "" {
			st.Origin = governance.Origin{Kind: "api", By: "root"}
		}
		switch st.Origin.Kind {
		case "schedule", "event", "api", "chat":
		default:
			http.Error(w, "origin.kind: want schedule, event, api or chat", http.StatusBadRequest)
			return
		}
		if req.User != "" {
			user = req.User
		} else if st.Origin.Kind == "schedule" || st.Origin.Kind == "event" {
			user = "system:" + st.Origin.Kind
		}
	default:
		st.Origin = governance.Origin{Kind: "api", By: c.Name}
	}

	sid := req.Session
	ctx := context.WithoutCancel(r.Context())
	if sid == "" {
		cs, err := l.Sessions.Create(ctx, &session.CreateRequest{AppName: l.cfg.Name, UserID: user})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sid = cs.Session.ID()
	} else if _, err := l.Sessions.Get(ctx, &session.GetRequest{AppName: l.cfg.Name, UserID: user, SessionID: sid}); err != nil {
		http.Error(w, "session: "+err.Error(), http.StatusNotFound)
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, l.cfg.Limits.MaxInvocation())
	st.Cancel = cancel
	started := make(chan string, 1)
	var startedID atomicString
	st.Started = func(id string) { startedID.Store(id); started <- id }
	l.Governor.Expect(sid, st)
	done := make(chan error, 1)
	l.runs.Add(1)
	go func() {
		defer l.runs.Done()
		defer cancel()
		var runErr error
		var id string
		for ev, err := range l.Runner.Run(runCtx, user, sid, genai.NewContentFromText(prompt, genai.RoleUser), agent.RunConfig{}) {
			if ev != nil && id == "" {
				id = ev.InvocationID
			}
			if err != nil {
				runErr = err
			}
		}
		if runErr != nil {
			l.Governor.Failed(firstNonEmpty(id, startedID.Load()), runErr)
		}
		done <- runErr
	}()
	var id string
	select {
	case id = <-started:
	case err := <-done:
		http.Error(w, fmt.Sprintf("the activity did not start: %v", err), http.StatusInternalServerError)
		return
	}
	if id == "" {
		http.Error(w, "the agent is paused", http.StatusConflict)
		return
	}
	if !req.Wait {
		writeJSON(w, http.StatusAccepted, map[string]string{"id": id, "session": sid})
		return
	}
	select {
	case <-done:
	case <-r.Context().Done():
		return // the activity goes on; the caller can read it later
	}
	a, _ := l.Governor.Activity(id)
	writeJSON(w, http.StatusOK, a)
}

func (l *Launcher) cancel(w http.ResponseWriter, r *http.Request) {
	c, _ := peercred.From(r.Context())
	a, ok := l.Governor.Activity(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !c.Root() && a.User != c.Name && a.Origin.By != c.Name {
		http.Error(w, "only root or the activity's user may cancel it", http.StatusForbidden)
		return
	}
	if !l.Governor.Cancel(a.ID, c.Name) {
		http.Error(w, "the activity is not running", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
