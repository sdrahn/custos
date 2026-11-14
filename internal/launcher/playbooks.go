package launcher

// Diagnostic playbooks: the prompt and steps of a playbook's activity, and
// which playbooks this agent's tools allow (CONTRACT.md §14c).

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/adk/session"
	"google.golang.org/genai"

	"github.com/sdrahn/custos/internal/governance"
	"github.com/sdrahn/custos/internal/playbook"
)

// playbookPrompt is the model's prompt for a playbook and the steps the
// host runs before the model's first turn: refused when the playbook is
// unknown or broken (400), or when the gateway does not offer this agent
// a tool of a step that is not optional (422).
func (l *Launcher) playbookPrompt(ctx context.Context, name, unit, extra string) (string, []governance.Step, []string, int, error) {
	if !playbook.ValidName(name) {
		return "", nil, nil, http.StatusBadRequest, fmt.Errorf("playbook: %q is not a playbook name", name)
	}
	all, err := playbook.Load(l.playbookRoot)
	if err != nil {
		return "", nil, nil, http.StatusInternalServerError, fmt.Errorf("reading the playbooks: %w", err)
	}
	p := all[name]
	switch {
	case p == nil:
		return "", nil, nil, http.StatusNotFound, fmt.Errorf("no playbook %q on this host", name)
	case p.Error != "":
		return "", nil, nil, http.StatusBadRequest, fmt.Errorf("playbook %s (%s): %s", name, p.File, p.Error)
	}
	offered, err := l.offeredTools(ctx)
	if err != nil {
		return "", nil, nil, http.StatusBadGateway, fmt.Errorf("listing the gateway's tools: %w", err)
	}
	if m := p.Missing(offered); len(m) > 0 {
		return "", nil, nil, http.StatusUnprocessableEntity, fmt.Errorf("playbook %s needs tools the gateway does not offer agent %s: %s",
			name, l.cfg.Name, strings.Join(m, ", "))
	}
	var steps []governance.Step
	for _, s := range p.Run(offered, unit) {
		steps = append(steps, governance.Step{Tool: s.Tool, Why: s.Why, Args: s.Args})
	}
	// The run shows the model only the playbook's tools (#155): a small
	// model's prompt carries every tool's schema it is shown.
	return p.Prompt(offered, unit, extra), steps, p.Offer(offered), 0, nil
}

// PlaybookOffer is a playbook as GET /v1/playbooks shows it to this
// agent: whether it can run it, and if not, why (CONTRACT.md §5, §14c).
type PlaybookOffer struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Runnable    bool   `json:"runnable"`
	// Missing are the tools of steps that are not optional which the
	// gateway does not offer this agent; Error why the file cannot be
	// used.
	Missing []string `json:"missing,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// playbooks answers which of the host's playbooks this agent can run
// now: those whose every step that is not optional has a tool the
// gateway offers it. The others are listed with the tools they lack.
func (l *Launcher) playbooks(w http.ResponseWriter, r *http.Request) {
	all, err := playbook.Load(l.playbookRoot)
	if err != nil {
		http.Error(w, "reading the playbooks: "+err.Error(), http.StatusInternalServerError)
		return
	}
	offered, err := l.offeredTools(r.Context())
	if err != nil {
		http.Error(w, "listing the gateway's tools: "+err.Error(), http.StatusBadGateway)
		return
	}
	out := map[string]PlaybookOffer{}
	for name, p := range all {
		o := PlaybookOffer{Title: p.Title, Description: p.Description, Error: p.Error}
		if p.Error == "" {
			o.Missing = p.Missing(offered)
			o.Runnable = len(o.Missing) == 0
		}
		out[name] = o
	}
	writeJSON(w, http.StatusOK, map[string]any{"playbooks": out})
}

// offeredTools are the gateway's tools the model is shown now: what the
// gateway's discovery offers this agent, narrowed by its configuration.
func (l *Launcher) offeredTools(ctx context.Context) (map[string]bool, error) {
	ts, err := l.tools.Tools(listContext{ctx})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, t := range ts {
		out[t.Name()] = true
	}
	return out, nil
}

// listContext is the context for listing tools outside a run: the
// toolsets use it only as a context.
type listContext struct{ context.Context }

func (listContext) UserContent() *genai.Content          { return nil }
func (listContext) InvocationID() string                 { return "" }
func (listContext) AgentName() string                    { return "" }
func (listContext) ReadonlyState() session.ReadonlyState { return nil }
func (listContext) UserID() string                       { return "" }
func (listContext) AppName() string                      { return "" }
func (listContext) SessionID() string                    { return "" }
func (listContext) Branch() string                       { return "" }
