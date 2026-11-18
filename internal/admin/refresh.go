package admin

import (
	"fmt"

	"github.com/sdrahn/custos/internal/config"
)

// minRefreshBefore is the least gateway.oauth.refresh_before_seconds that
// keeps an OpenShell agent's token renewed: OpenShell's gateway looks for
// tokens due for renewal once a minute (0.1.8), so a token due later than
// that sweep can expire before the next one. A sandbox that starts then
// gets no token at all (CONTRACT.md §16a, token).
const minRefreshBefore = 90

// refreshNotes says when the token may expire before OpenShell renews it.
func refreshNotes(cfg *config.Config) []string {
	o := cfg.Gateway.OAuth
	if o == nil || o.RefreshBefore == 0 || o.RefreshBefore >= minRefreshBefore {
		return nil
	}
	return []string{fmt.Sprintf("gateway.oauth.refresh_before_seconds: %d: OpenShell's gateway renews tokens in a sweep once a minute, so a token can expire before it is renewed, and a sandbox started then has no token; set %d or more, or leave it out (OpenShell's default, 300)", o.RefreshBefore, minRefreshBefore)}
}

// noteRefresh prints the notes, which do not stop anything.
func (a *Admin) noteRefresh(cfg *config.Config) {
	for _, n := range refreshNotes(cfg) {
		fmt.Fprintf(a.Out, "agent %s: note: %s\n", cfg.Name, n)
	}
}
