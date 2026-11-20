package admin

import (
	"fmt"
	"time"

	"github.com/sdrahn/custos/internal/config"
)

// minRefreshBefore is the least gateway.oauth.refresh_before_seconds that
// keeps an OpenShell agent's token renewed: OpenShell's gateway looks for
// tokens due for renewal once a minute (0.1.8), so a token due later than
// that sweep can expire before the next one. A sandbox that starts then
// gets no token at all (CONTRACT.md §16a, token).
const minRefreshBefore = 90

// DraftRefreshBefore is the refresh_before_seconds of an OpenShell agent's
// drafted configuration. OpenShell's own default, 300 s, renews a token of
// 5 minutes or less (Keycloak's, idp-client --keycloak) at every sweep,
// once a minute, and each renewal closes the sandbox's open connections:
// the agent's calls end in EOF. 120 renews such a token every 3 minutes.
const DraftRefreshBefore = 120

// OpenShellRefreshBefore is OpenShell's default when the agent sets none.
const OpenShellRefreshBefore = 300

// RenewalNote says, for a token valid for lifetime, when OpenShell renews
// it at every sweep (a renewal window not at least a minute shorter than
// the token's life), and what to set instead; "" when it does not.
func RenewalNote(o *config.OAuth, lifetime time.Duration) string {
	before := time.Duration(OpenShellRefreshBefore) * time.Second
	set := "unset: OpenShell's default, 300 s"
	if o != nil && o.RefreshBefore > 0 {
		before = time.Duration(o.RefreshBefore) * time.Second
		set = fmt.Sprintf("%d s", o.RefreshBefore)
	}
	if lifetime <= 0 || before+time.Minute < lifetime {
		return ""
	}
	return fmt.Sprintf("the token lives %s and gateway.oauth.refresh_before_seconds is %s: OpenShell renews it every minute, and each renewal closes the sandbox's open connections (calls end in EOF); set refresh_before_seconds to at least %d and at most %d, or make the token live longer",
		lifetime, set, minRefreshBefore, int((lifetime-time.Minute-30*time.Second)/time.Second))
}

// refreshNotes says when the token may expire before OpenShell renews it.
func refreshNotes(cfg *config.Config) []string {
	o := cfg.Gateway.OAuth
	switch {
	case o == nil || o.RefreshBefore >= minRefreshBefore:
		return nil
	case o.RefreshBefore == 0:
		return []string{fmt.Sprintf("gateway.oauth.refresh_before_seconds is unset: OpenShell renews the token %d s before it expires, which for a token of 5 minutes or less is at every sweep, once a minute, and each renewal closes the sandbox's open connections (calls end in EOF); set it to %d (agent-admin check-token says whether the token's lifetime suits it)", OpenShellRefreshBefore, DraftRefreshBefore)}
	}
	return []string{fmt.Sprintf("gateway.oauth.refresh_before_seconds: %d: OpenShell's gateway renews tokens in a sweep once a minute, so a token can expire before it is renewed, and a sandbox started then has no token; set %d or more (%d suits a token of 5 minutes)", o.RefreshBefore, minRefreshBefore, DraftRefreshBefore)}
}

// noteRefresh prints the notes, which do not stop anything.
func (a *Admin) noteRefresh(cfg *config.Config) {
	for _, n := range refreshNotes(cfg) {
		fmt.Fprintf(a.Out, "agent %s: note: %s\n", cfg.Name, n)
	}
}
