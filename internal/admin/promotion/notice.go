package promotion

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/gatewayctl"
)

// demotionNotice is the notice of an automatic demotion of category c of
// agent name: for the servers its permissions name (globs aside, whose
// approvers the gateway cannot tell).
func demotionNotice(name, c, reason string, d *admin.GatewayData) gatewayctl.Notice {
	set := map[string]bool{}
	for _, p := range d.Permissions {
		s, _ := p["server"].(string)
		if cat, _ := p["category"].(string); cat == c && s != "" && !strings.ContainsAny(s, "*?[") {
			set[s] = true
		}
	}
	servers := make([]string, 0, len(set))
	for s := range set {
		servers = append(servers, s)
	}
	sort.Strings(servers)
	text := fmt.Sprintf("The demotion evaluator set task category %s of agent %s back to supervised: %s.\n\n"+
		"Its changes wait for an approval again. Once the cause is dealt with, an administrator can promote it again "+
		"(agent-admin readiness %s %s; agent-admin promote).", c, name, reason, name, c)
	if r := []rune(text); len(text) > 4000 {
		text = string(r[:1000]) + "…" // the gateway takes at most 4000 bytes
	}
	return gatewayctl.Notice{Principal: "agent-" + name, Servers: servers, Severity: "warning",
		Title: fmt.Sprintf("%s: %s demoted to supervised", name, c), Text: text}
}
