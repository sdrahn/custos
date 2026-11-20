package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/config"
)

// OpenShell's gateway renews tokens in a sweep once a minute: a
// refresh_before_seconds below 90 gets a note, and so does none (OpenShell's
// 300 renews a token of 5 minutes at every sweep); 90 and more do not, and
// an agent without an OAuth client has nothing to note.
func TestRefreshNotes(t *testing.T) {
	parse := func(oauth string) *config.Config {
		t.Helper()
		cfg, err := config.Parse([]byte(strings.Replace(oshOAuthYml, "scopes: [mcp]}", "scopes: [mcp]"+oauth+"}", 1)))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	for oauth, want := range map[string]bool{
		"":                              true,
		", refresh_before_seconds: 30":  true,
		", refresh_before_seconds: 60":  true,
		", refresh_before_seconds: 89":  true,
		", refresh_before_seconds: 90":  false,
		", refresh_before_seconds: 120": false,
	} {
		n := refreshNotes(parse(oauth))
		if (len(n) > 0) != want {
			t.Errorf("%q: %v", oauth, n)
		}
		if want && oauth != "" && !strings.Contains(n[0], "set 90 or more") {
			t.Errorf("%q: %v", oauth, n)
		}
		if want && oauth == "" && !strings.Contains(n[0], "is unset") {
			t.Errorf("unset: %v", n)
		}
	}
	cfg, _ := config.Parse([]byte("version: 1\nname: sb\nmodel: {provider: openai, name: m, endpoint: \"http://127.0.0.1:8008/v1\"}\ngateway: {servers: [zypp]}\n"))
	if n := refreshNotes(cfg); n != nil {
		t.Errorf("a systemd agent: %v", n)
	}
	// check prints it, and the configuration stays valid.
	a, _ := newAdmin(t)
	var out strings.Builder
	a.Out = &out
	a.noteRefresh(parse(", refresh_before_seconds: 30"))
	if !strings.Contains(out.String(), "agent sb: note: gateway.oauth.refresh_before_seconds: 30:") {
		t.Errorf("printed %q", out.String())
	}
}

// A renewal window not at least a minute shorter than the token's life
// renews it at every sweep: noted, with what to set; one that leaves time
// between renewals is not.
func TestRenewalNote(t *testing.T) {
	min5 := 5 * time.Minute
	for _, tc := range []struct {
		before   int
		lifetime time.Duration
		note     bool
	}{
		{0, min5, true},              // OpenShell's 300 s: every minute
		{240, min5, true},            // 4 minutes of a 5-minute token
		{120, min5, false},           // the drafts' 120 s: every 3 minutes
		{0, 15 * time.Minute, false}, // a longer token suits OpenShell's default
		{120, 2 * time.Minute, true}, // a token shorter than the window
	} {
		o := &config.OAuth{RefreshBefore: tc.before}
		n := RenewalNote(o, tc.lifetime)
		if (n != "") != tc.note {
			t.Errorf("before %d, lifetime %s: %q", tc.before, tc.lifetime, n)
		}
		if tc.note && !strings.Contains(n, "every minute") {
			t.Errorf("before %d, lifetime %s: %q", tc.before, tc.lifetime, n)
		}
	}
	if n := RenewalNote(&config.OAuth{}, min5); !strings.Contains(n, "at least 90 and at most 210") {
		t.Errorf("what to set: %q", n)
	}
}
