package idp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gatewayconf"
	"github.com/sdrahn/custos/internal/host"
	"github.com/sdrahn/custos/internal/host/hosttest"
)

const gatewayYml = `http:
  listen: ":8443"
  issuer: https://idp.example.com/realms/mcp
  audience: https://gw.example.com:8443/mcp
  scopes: [mcp]
  local_user_claim: preferred_username
`

// idpHost has agent sb (OpenShell, an OAuth client at a fake issuer that
// answers with claims), the gateway's settings and its agent issuers.
func idpHost(t *testing.T, claims map[string]any) (*admin.Admin, *hosttest.Rec, *string) {
	t.Helper()
	a, r := newAdmin(t)
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id, secret, _ := req.BasicAuth()
		req.ParseForm()
		gotAuth = id + ":" + secret + " " + req.Form.Get("grant_type") + " " + req.Form.Get("scope")
		w.Header().Set("Content-Type", "application/json") // RFC 6749, 5.1
		if secret != "s3cret" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"unauthorized_client","error_description":"Invalid client secret"}`))
			return
		}
		h, _ := json.Marshal(map[string]string{"alg": "RS256"})
		p, _ := json.Marshal(claims)
		e := base64.RawURLEncoding.EncodeToString
		json.NewEncoder(w).Encode(map[string]string{"access_token": e(h) + "." + e(p) + ".c2ln", "token_type": "Bearer"})
	}))
	t.Cleanup(srv.Close)
	dir := a.AgentDir("sb")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(strings.Replace(oshOAuthYml, "https://idp.example.com/token", srv.URL+"/token", 1)), 0o640)
	os.MkdirAll(a.Path(filepath.Dir(gatewayconf.Config)), 0o755)
	os.WriteFile(a.Path(gatewayconf.Config), []byte(gatewayYml), 0o644)
	host.WriteFile(a.Path(admin.GatewayHTTPIssuers), []byte("_http:\n  issuers: [\"https://idp.example.com/realms/mcp\"]\n"), 0o644)
	a.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	return a, r, &gotAuth
}

func goodClaims() map[string]any {
	return map[string]any{"iss": "https://idp.example.com/realms/mcp", "sub": "agent-sb",
		"aud": []any{"account", "https://gw.example.com:8443/mcp"}, "scope": "mcp", "iat": 1_800_000_000.0, "exp": 1_800_000_300.0}
}

// check-token mints a token with the agent's client and the secret given,
// and passes one with the claims the gateway expects.
func TestCheckToken(t *testing.T) {
	a, _, auth := idpHost(t, goodClaims())
	var out strings.Builder
	a.Out = &out
	if err := CheckToken(a, "sb", strings.NewReader("s3cret\n")); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if *auth != "agent-sb:s3cret client_credentials mcp" {
		t.Errorf("the token request: %q", *auth)
	}
	for _, want := range []string{"ok   sub agent-sb", "ok   aud includes https://gw.example.com:8443/mcp", "ok   scope mcp",
		"ok   exp in 5m0s", "ok   no preferred_username", "is what the gateway expects",
		// The agent sets no refresh_before_seconds: OpenShell's 300 s
		// renews this 5-minute token at every sweep.
		"note the token lives 5m0s and gateway.oauth.refresh_before_seconds is unset"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
}

// Each claim the identity provider's client configuration decides, wrong:
// check-token says which, and how to fix it.
func TestCheckTokenFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"keycloak's own sub": {func(c map[string]any) { c["sub"] = "6c1a0b5e-1234" }, "FAIL sub 6c1a0b5e-1234: want agent-sb (in Keycloak: the client's hardcoded sub"},
		"no audience":        {func(c map[string]any) { c["aud"] = "account" }, "FAIL aud includes https://gw.example.com:8443/mcp: aud is account"},
		"no scope":           {func(c map[string]any) { delete(c, "scope") }, "FAIL scope mcp"},
		"another issuer":     {func(c map[string]any) { c["iss"] = "https://idp.example.com/realms/people" }, "FAIL the issuer is listed for agents"},
		"long-lived":         {func(c map[string]any) { c["exp"] = 1_800_090_000.0 }, "FAIL lifetime 25h0m0s"},
		"a local account":    {func(c map[string]any) { c["preferred_username"] = "alice" }, "FAIL no preferred_username"},
	} {
		c := goodClaims()
		tc.edit(c)
		a, _, _ := idpHost(t, c)
		var out strings.Builder
		a.Out = &out
		if err := CheckToken(a, "sb", strings.NewReader("s3cret\n")); err == nil || !strings.Contains(out.String(), tc.want) {
			t.Errorf("%s: %v\n%s", name, err, out.String())
		}
	}
	// A wrong secret: the issuer's own words.
	a, _, _ := idpHost(t, goodClaims())
	if err := CheckToken(a, "sb", strings.NewReader("wrong")); err == nil || !strings.Contains(err.Error(), "refused client agent-sb (401): unauthorized_client Invalid client secret") {
		t.Errorf("wrong secret: %v", err)
	}
	// No secret given.
	if err := CheckToken(a, "sb", strings.NewReader("\n")); err == nil || !strings.Contains(err.Error(), "check-token sb < secret") {
		t.Errorf("no secret: %v", err)
	}
}

// idp-client says what the client must be, from the gateway's settings;
// --keycloak, a script that creates it with the subject, audience, scope
// and a short lifetime, and without the realm's default client scopes.
func TestIDPClient(t *testing.T) {
	a, _, _ := idpHost(t, goodClaims())
	var out strings.Builder
	a.Out = &out
	if err := Client(a, "sb", false, 0); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"client id     agent-sb", "sub    agent-sb", "aud    https://gw.example.com:8443/mcp", "scope  mcp",
		"(listed for agents)", "no claim preferred_username"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := Client(a, "sb", true, 0); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	i := strings.Index(s, "<<'EOF'\n") + len("<<'EOF'\n")
	j := strings.Index(s[i:], "\nEOF\n") + i
	var client map[string]any
	if err := json.Unmarshal([]byte(s[i:j]), &client); err != nil {
		t.Fatalf("the client's JSON: %v\n%s", err, s)
	}
	mappers, _ := json.Marshal(client["protocolMappers"])
	if client["clientId"] != "agent-sb" || client["serviceAccountsEnabled"] != true || client["standardFlowEnabled"] != false ||
		len(client["defaultClientScopes"].([]any)) != 0 ||
		!strings.Contains(string(mappers), `"claim.name":"sub","claim.value":"agent-sb"`) ||
		!strings.Contains(string(mappers), `"included.custom.audience":"https://gw.example.com:8443/mcp"`) {
		t.Errorf("client %s", s[i:j])
	}
	if !strings.Contains(s, "realm=mcp") || !strings.Contains(s, `create client-scopes -r "$realm" -s name="mcp"`) ||
		!strings.Contains(s, "client-secret") {
		t.Errorf("script:\n%s", s)
	}
	// A gateway without its HTTPS listener: nothing to say.
	os.WriteFile(a.Path(gatewayconf.Config), []byte("listen: /run/mcp-gateway/mcp.sock\n"), 0o644)
	if err := Client(a, "sb", false, 0); err == nil || !strings.Contains(err.Error(), "http.issuer and http.audience are not set") {
		t.Errorf("no listener: %v", err)
	}
}

// newAdmin is agent-admin on a test host.
func newAdmin(t *testing.T) (*admin.Admin, *hosttest.Rec) {
	h, r := hosttest.New(t)
	return &admin.Admin{Host: h}, r
}

// oshOAuthYml is an OpenShell agent with an OAuth client.
const oshOAuthYml = `version: 1
name: sb
model: {provider: openai, name: m, endpoint: "http://127.0.0.1:8008/v1"}
gateway:
  url: "https://gw.example.com:8443/mcp"
  servers: [zypp]
  oauth: {token_url: "https://idp.example.com/token", client_id: agent-sb, scopes: [mcp]}
sandbox: {kind: openshell}
`

// --token-lifetime: whole seconds, at most an hour, and a minute longer
// than the agent's renewal window (a drafted one's, 120 s, without an
// agent); for --keycloak only, in its script.
func TestTokenLifetime(t *testing.T) {
	for _, c := range []struct {
		o    *config.OAuth
		d    time.Duration
		fail string
	}{
		{nil, 30 * time.Minute, ""},
		{nil, 4 * time.Minute, ""},
		{nil, 3 * time.Minute, "renews it every minute"},
		{&config.OAuth{}, 5 * time.Minute, "renews it every minute"}, // OpenShell's 300 s
		{&config.OAuth{RefreshBefore: 120}, time.Hour, ""},
		{nil, 2 * time.Hour, "at most 1h0m0s"},
		{nil, 1500 * time.Millisecond, "whole seconds"},
		{nil, -time.Minute, "whole seconds"},
	} {
		err := tokenLifetime(c.o, c.d)
		if (c.fail == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.fail)) {
			t.Errorf("%v %s: %v, want %q", c.o, c.d, err, c.fail)
		}
	}
	a, _, _ := idpHost(t, goodClaims())
	var out strings.Builder
	a.Out = &out
	if err := Client(a, "sb", true, 30*time.Minute); err != nil || !strings.Contains(out.String(), `"access.token.lifespan": "1800"`) {
		t.Errorf("script: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := Client(a, "sb", true, 0); err != nil || !strings.Contains(out.String(), `"access.token.lifespan": "300"`) {
		t.Errorf("script, default: %v\n%s", err, out.String())
	}
	if err := Client(a, "sb", false, 30*time.Minute); err == nil || !strings.Contains(err.Error(), "for --keycloak") {
		t.Errorf("without --keycloak: %v", err)
	}
}
