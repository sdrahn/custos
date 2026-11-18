package admin

import (
	"strings"
	"testing"

	"github.com/sdrahn/custos/internal/config"
)

// OpenShell's gateway renews tokens in a sweep once a minute: a
// refresh_before_seconds below 90 gets a note, none or 90 and more do not,
// and an agent without an OAuth client has nothing to note.
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
		"":                              false,
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
		if want && !strings.Contains(n[0], "set 90 or more") {
			t.Errorf("%q: %v", oauth, n)
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
