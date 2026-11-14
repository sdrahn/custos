//go:build keycloak

package admin

// A real Keycloak (#111): the CI job keycloak runs it and this test, with
//
//	KEYCLOAK_URL  where it answers, e.g. http://127.0.0.1:8180
//	KCADM         its kcadm.sh, logged in to its master realm,
//	              e.g. "docker exec -i kc /opt/keycloak/bin/kcadm.sh"
//
//	go test -tags keycloak -run TestKeycloak ./internal/admin/
//
// The script idp-client --keycloak prints creates agent sb's client in a
// new realm; check-token then mints a token with it and finds every claim
// the gateway relies on.

import (
	"fmt"
	"github.com/sdrahn/custos/internal/gatewayconf"
	"github.com/sdrahn/custos/internal/host"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKeycloak(t *testing.T) {
	base, kcadm := os.Getenv("KEYCLOAK_URL"), os.Getenv("KCADM")
	if base == "" || kcadm == "" {
		t.Fatal("KEYCLOAK_URL and KCADM are not set (the keycloak tag needs a Keycloak)")
	}
	realm := fmt.Sprintf("custos%d", time.Now().Unix())
	run := func(script string) string {
		t.Helper()
		c := exec.Command("bash", "-c", script)
		c.Env = append(os.Environ(), "KCADM="+kcadm)
		out, err := c.Output()
		if err != nil {
			stderr := ""
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = string(ee.Stderr)
			}
			t.Fatalf("%v\n%s\n%s\n--- script:\n%s", err, out, stderr, script)
		}
		return string(out)
	}
	run(`$KCADM create realms -s realm=` + realm + ` -s enabled=true >/dev/null`)

	a, _ := newAdmin(t)
	a.Now = time.Now
	issuer := strings.TrimRight(base, "/") + "/realms/" + realm
	gw := strings.Replace(gatewayYml, "https://idp.example.com/realms/mcp", issuer, 1)
	os.MkdirAll(a.Path(filepath.Dir(gatewayconf.Config)), 0o755)
	os.WriteFile(a.Path(gatewayconf.Config), []byte(gw), 0o644)
	host.WriteFile(a.Path(GatewayHTTPIssuers), []byte("_http:\n  issuers: [\""+issuer+"\"]\n"), 0o644)
	dir := a.agentDir("sb")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "agent.yaml"),
		[]byte(strings.Replace(oshOAuthYml, "https://idp.example.com/token", issuer+"/protocol/openid-connect/token", 1)), 0o640)

	var script strings.Builder
	a.Out = &script
	if err := a.IDPClient("sb", true); err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(run(script.String()))
	if secret == "" {
		t.Fatalf("the script printed no secret:\n%s", script.String())
	}
	var out strings.Builder
	a.Out = &out
	if err := a.CheckToken("sb", strings.NewReader(secret+"\n")); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Logf("check-token against Keycloak:\n%s", out.String())
}
