package launcher

// The control API on the agent's socket: status, pause and resume
// (CONTRACT.md §10), and the handlers' helpers.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/governance"
	"github.com/sdrahn/custos/internal/peercred"
)

// Status is GET /v1/status (CONTRACT.md §5).
type Status struct {
	Contract       int               `json:"contract"`
	Implementation string            `json:"implementation"`
	Units          map[string]string `json:"units,omitempty"`
	Lifecycle      map[string]string `json:"lifecycle"`
	Name           string            `json:"name"`
	Version        string            `json:"version"`
	Model          string            `json:"model"`
	Paused         bool              `json:"paused"`
	Gateway        GatewayStatus     `json:"gateway"`
	InFlight       []governance.Call `json:"in_flight"`
	Running        int               `json:"running"`
	Started        time.Time         `json:"started"`
	Limits         config.Limits     `json:"limits"`
	Interaction    map[string]any    `json:"interaction"`
	Schedules      []config.Schedule `json:"schedules"`
	Events         []config.Event    `json:"events"`
}

type GatewayStatus struct {
	Socket    string `json:"socket,omitempty"`
	URL       string `json:"url,omitempty"`
	Reachable bool   `json:"reachable"`
}

func (l *Launcher) status(w http.ResponseWriter, r *http.Request) {
	running := 0
	for _, a := range l.Governor.Activities() {
		if a.Ended == nil {
			running++
		}
	}
	var units map[string]string
	if lc := l.cfg.Lifecycle(); lc["kind"] == "systemd" {
		units = map[string]string{"service": lc["service"], "socket": lc["socket"]}
	}
	reachable := false
	if l.cfg.Gateway.URL != "" {
		reachable = true // not checked without a request; the calls show it
	} else if _, err := os.Stat(l.cfg.Gateway.Socket); err == nil {
		reachable = true
	}
	writeJSON(w, http.StatusOK, Status{
		Contract: config.ContractVersion, Implementation: "custos/adk-go", Name: l.cfg.Name, Version: Version,
		Units: units, Lifecycle: l.cfg.Lifecycle(),
		Model: l.cfg.Model.Provider + ":" + l.cfg.Model.Name, Paused: l.Governor.Paused(),
		Gateway:  GatewayStatus{Socket: l.cfg.Gateway.Socket, URL: l.cfg.Gateway.URL, Reachable: reachable},
		InFlight: nonNil(l.Governor.InFlight()), Running: running, Started: l.started, Limits: l.cfg.Limits,
		Interaction: map[string]any{"agui": l.cfg.Interaction.AGUI, "client_tools": nonNil(l.cfg.Interaction.ClientTools.Allow)},
		Schedules:   nonNil(l.cfg.Schedules),
		Events:      nonNil(l.cfg.Events),
	})
}

func (l *Launcher) pause(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, _ := peercred.From(r.Context())
		if !c.Root() {
			http.Error(w, "pausing needs administrative access", http.StatusForbidden)
			return
		}
		if on {
			l.Governor.Pause(c.Name)
		} else {
			l.Governor.Resume(c.Name)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

type atomicString struct{ v atomic.Value }

func (a *atomicString) Store(s string) { a.v.Store(s) }
func (a *atomicString) Load() string {
	s, _ := a.v.Load().(string)
	return s
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// peekJSON decodes the body into v and returns the request with the
// body intact for the next handler.
func peekJSON(r *http.Request, v any) (*http.Request, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return nil, errors.New("bad request body")
	}
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(b))
	return r2, nil
}
