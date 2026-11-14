package admin

import (
	"errors"
	"github.com/sdrahn/custos/internal/gatewayctl"
	"strings"
	"testing"
	"time"
)

// The notice of a demotion: about the agent's principal, for the servers
// of the category's permissions (not a glob's), a warning.
func TestDemotionNotice(t *testing.T) {
	d := &GatewayData{Permissions: []map[string]any{
		{"server": "zypp", "tool": "patch", "category": "patch-errata"},
		{"server": "zypp", "tool": "install", "category": "patch-errata"},
		{"server": "snapper", "tool": "create", "category": "patch-errata"},
		{"server": "sys*", "tool": "x", "category": "patch-errata"},
		{"server": "systemd", "tool": "restart_unit", "category": "config-lifecycle"},
	}}
	n := demotionNotice("patch-pilot", "patch-errata", "override rate 7.1 % above 5.0 %", d)
	if n.Principal != "agent-patch-pilot" || strings.Join(n.Servers, ",") != "snapper,zypp" || n.Severity != "warning" ||
		n.Title != "patch-pilot: patch-errata demoted to supervised" || !strings.Contains(n.Text, "override rate 7.1 % above 5.0 %") ||
		!strings.Contains(n.Text, "agent-admin readiness patch-pilot patch-errata") {
		t.Errorf("%+v", n)
	}
	if long := demotionNotice("a", "c", strings.Repeat("x", 5000), d); len(long.Text) > 4000 {
		t.Errorf("text of %d bytes", len(long.Text))
	}
}

// The evaluator posts a notice for each demotion; a gateway without
// notices, or one that refuses it, leaves the demotion as it is.
func TestEvaluatePostsANotice(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "approvers notified (notice n-1)"},
		{gatewayctl.ErrNoNotices, "no notice: this mcp-gateway takes none"},
		{errors.New("the gateway refused the notice (403): not allowed to post notices"), "no notice: the gateway refused the notice (403)"},
	} {
		a, _, j, _ := promoted(t, "rollback: {on: [tool_error]}\n")
		var posted []gatewayctl.Notice
		a.Notify = func(n gatewayctl.Notice) (string, error) {
			posted = append(posted, n)
			if tc.err != nil {
				return "", tc.err
			}
			return "n-1", nil
		}
		j.autonomous(a.Now().Add(-time.Hour), "e-2", "error")
		d := evaluate(t, a)
		if len(d) != 1 || !strings.HasPrefix(d[0].Notice, tc.want) || mode(a, "patch-errata") != "supervised" {
			t.Errorf("%v: demotions %+v", tc.err, d)
		}
		if len(posted) != 1 || posted[0].Principal != "agent-patch-pilot" || !strings.Contains(posted[0].Text, "rollback tool_error") {
			t.Errorf("posted %+v", posted)
		}
	}
	// Nothing to demote, nothing posted.
	a, _, _, _ := promoted(t, "rollback: {on: [tool_error]}\n")
	a.Notify = func(n gatewayctl.Notice) (string, error) { t.Errorf("posted %+v", n); return "", nil }
	evaluate(t, a)
}
