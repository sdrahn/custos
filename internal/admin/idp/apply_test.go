package idp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/host"
)

// The fake Keycloaks speak plain http on loopback, which agent-admin
// itself never sends a password over (T1).
func init() { plainHTTPLoopback = true }

// oldLifespan is the access.token.lifespan of the client fakeKeycloak has
// already ("": none set).
var oldLifespan string

// fakeKeycloak answers the admin API calls of KeycloakApply for realm mcp
// (a client agent-sb there already, the realm's scope basic, no scope
// mcp), logged in as admin/pw in master; calls records what was asked.
func fakeKeycloak(t *testing.T, forbid bool) (*httptest.Server, *[]string, *map[string]any) {
	t.Helper()
	var mu sync.Mutex
	calls := []string{}
	created := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := r.URL.Path
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		calls = append(calls, r.Method+" "+path)
		reply := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
		if path == "/realms/master/protocol/openid-connect/token" {
			r.ParseForm()
			if r.Form.Get("username") != "admin" || r.Form.Get("password") != "pw" || r.Form.Get("client_id") != "admin-cli" {
				w.WriteHeader(401)
				reply(map[string]string{"error": "invalid_grant", "error_description": "Invalid user credentials"})
				return
			}
			reply(map[string]string{"access_token": "adm"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer adm" {
			w.WriteHeader(401)
			return
		}
		if forbid {
			w.WriteHeader(403)
			return
		}
		const p = "/admin/realms/mcp"
		switch r.Method + " " + path {
		case "GET " + p + "/client-scopes":
			reply([]map[string]string{{"id": "s1", "name": "basic"}})
		case "POST " + p + "/client-scopes":
			w.Header().Set("Location", "http://kc"+p+"/client-scopes/s2")
			w.WriteHeader(201)
		case "GET " + p + "/clients?clientId=agent-sb":
			reply([]map[string]any{{"id": "old", "attributes": map[string]string{"access.token.lifespan": oldLifespan}}})
		case "DELETE " + p + "/clients/old", "DELETE " + p + "/clients/c1/default-client-scopes/s1",
			"PUT " + p + "/clients/c1/default-client-scopes/s2":
			w.WriteHeader(204)
		case "POST " + p + "/clients":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &created)
			w.Header().Set("Location", "http://kc"+p+"/clients/c1")
			w.WriteHeader(201)
		case "GET " + p + "/clients/c1/default-client-scopes":
			reply([]map[string]string{{"id": "s1", "name": "basic"}})
		case "GET " + p + "/clients/c1/client-secret":
			reply(map[string]string{"value": "s3cret"})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &created
}

// --apply: the client replaced, with its subject and audience, the
// gateway's scope made and its only default, the token checked with the
// new secret and the secret set as the agent's credential.
func TestKeycloakApply(t *testing.T) {
	a, r, _ := idpHost(t, goodClaims())
	os.WriteFile(filepath.Join(a.AgentDir("sb"), "relay.secret"), []byte("relay\n"), 0o600) // agent-admin create's
	kc, calls, created := fakeKeycloak(t, false)
	var out strings.Builder
	a.Out = &out
	if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: kc.URL}, strings.NewReader("pw\n")); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"POST /admin/realms/mcp/client-scopes", "DELETE /admin/realms/mcp/clients/old", "POST /admin/realms/mcp/clients",
		"DELETE /admin/realms/mcp/clients/c1/default-client-scopes/s1", "PUT /admin/realms/mcp/clients/c1/default-client-scopes/s2"} {
		if !strings.Contains(strings.Join(*calls, "\n"), want) {
			t.Errorf("not asked: %s\n%s", want, strings.Join(*calls, "\n"))
		}
	}
	if (*created)["clientId"] != "agent-sb" || (*created)["serviceAccountsEnabled"] != true || (*created)["standardFlowEnabled"] != false {
		t.Errorf("client %v", *created)
	}
	if b, _ := json.Marshal((*created)["protocolMappers"]); !strings.Contains(string(b), `"claim.value":"agent-sb"`) || !strings.Contains(string(b), "https://gw.example.com:8443/mcp") {
		t.Errorf("mappers %s", b)
	}
	if !strings.Contains(out.String(), "set  Keycloak client agent-sb in realm mcp") || !strings.Contains(out.String(), "token is what the gateway expects") {
		t.Errorf("output:\n%s", out.String())
	}
	if !strings.Contains(strings.Join(r.Cmds, "\n"), "provider") {
		t.Errorf("the credential was not set:\n%s", strings.Join(r.Cmds, "\n"))
	}
	// The secret reaches OpenShell through the environment (the record's
	// VAR=value prefix), never as an argument.
	for _, c := range r.Cmds {
		if strings.Contains(strings.ReplaceAll(c, "CUSTOS_GATEWAY_CLIENT_SECRET=s3cret ", ""), "s3cret") {
			t.Errorf("the secret is on a command line: %s", c)
		}
	}
}

// A wrong password, and a user who may not manage clients, say so; and
// nothing is made without a password.
func TestKeycloakApplyRefused(t *testing.T) {
	a, _, _ := idpHost(t, goodClaims())
	kc, _, _ := fakeKeycloak(t, false)
	if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: kc.URL}, strings.NewReader("wrong\n")); err == nil || !strings.Contains(err.Error(), "Invalid user credentials") {
		t.Errorf("wrong password: %v", err)
	}
	if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: kc.URL}, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "no password") {
		t.Errorf("no password: %v", err)
	}
	if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: "http://kc.example.com:8080"}, strings.NewReader("pw\n")); err == nil || !strings.Contains(err.Error(), "https only") {
		t.Errorf("plain http: %v", err)
	}
	kc2, _, _ := fakeKeycloak(t, true)
	if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: kc2.URL}, strings.NewReader("pw\n")); err == nil || !strings.Contains(err.Error(), "manage-clients") {
		t.Errorf("forbidden: %v", err)
	}
}

// Security review 0.9: T1, plain http only in this package's tests; T2,
// only the agent's own client is replaced; T3, a redirect is not followed
// with the password; T4, an issuer listed for a token that fails goes again.
func TestKeycloakApplyHardening(t *testing.T) {
	a, _, _ := idpHost(t, goodClaims())
	os.WriteFile(filepath.Join(a.AgentDir("sb"), "relay.secret"), []byte("relay\n"), 0o600)
	kc, calls, _ := fakeKeycloak(t, false)

	plainHTTPLoopback = false
	err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: kc.URL}, strings.NewReader("pw\n"))
	plainHTTPLoopback = true
	if err == nil || !strings.Contains(err.Error(), "https only") || len(*calls) != 0 {
		t.Errorf("T1: plain http to loopback: %v, %v", err, *calls)
	}

	cfg := filepath.Join(a.AgentDir("sb"), "agent.yaml")
	orig, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(orig), "client_id: agent-sb", "client_id: account", 1)), 0o640)
	if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: kc.URL}, strings.NewReader("pw\n")); err == nil || !strings.Contains(err.Error(), "only agent-sb") {
		t.Errorf("T2: another client id: %v", err)
	}
	if strings.Contains(strings.Join(*calls, "\n"), "DELETE") {
		t.Errorf("T2: something was deleted: %v", *calls)
	}
	os.WriteFile(cfg, orig, 0o640)

	var got string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		got = r.Form.Get("password")
	}))
	t.Cleanup(elsewhere.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: redirect.URL}, strings.NewReader("pw\n")); err == nil || got != "" {
		t.Errorf("T3: redirected: %v, the password reached %q", err, got)
	}
}

func TestKeycloakApplyUnlistsOnFailure(t *testing.T) {
	bad := goodClaims()
	bad["sub"] = "someone-else"
	a, _, _ := idpHost(t, bad)
	os.WriteFile(filepath.Join(a.AgentDir("sb"), "relay.secret"), []byte("relay\n"), 0o600)
	host.WriteFile(a.Path(admin.GatewayHTTPIssuers), []byte("_http:\n  issuers: []\n"), 0o644)
	kc, _, _ := fakeKeycloak(t, false)
	var out strings.Builder
	a.Out = &out
	if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: kc.URL}, strings.NewReader("pw\n")); err == nil {
		t.Fatal("a token with another sub was taken")
	}
	if issuers, _ := a.AgentIssuers(); len(issuers) != 0 {
		t.Errorf("T4: the issuer stayed listed: %v\n%s", issuers, out.String())
	}
	if !strings.Contains(out.String(), "no longer listed") {
		t.Errorf("output:\n%s", out.String())
	}
}

// The token lifetime: --token-lifetime's; without it, the replaced
// client's (set in Keycloak's admin console, say); without either, 5
// minutes.
func TestKeycloakApplyTokenLifetime(t *testing.T) {
	t.Cleanup(func() { oldLifespan = "" })
	for _, c := range []struct {
		old  string
		set  time.Duration
		want string
		says string
	}{
		{"", 0, "300", "tokens valid for 5m0s"},
		{"1800", 0, "1800", "tokens valid for 30m0s"},
		{"1800", 10 * time.Minute, "600", "tokens valid for 10m0s"},
		{"junk", 0, "300", "tokens valid for 5m0s"},
	} {
		oldLifespan = c.old
		a, _, _ := idpHost(t, goodClaims())
		os.WriteFile(filepath.Join(a.AgentDir("sb"), "relay.secret"), []byte("relay\n"), 0o600)
		kc, _, created := fakeKeycloak(t, false)
		var out strings.Builder
		a.Out = &out
		if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: kc.URL, TokenLifetime: c.set}, strings.NewReader("pw\n")); err != nil {
			t.Fatalf("%+v: %v\n%s", c, err, out.String())
		}
		attrs, _ := (*created)["attributes"].(map[string]any)
		if attrs["access.token.lifespan"] != c.want || !strings.Contains(out.String(), c.says) {
			t.Errorf("%+v: lifespan %v\n%s", c, attrs["access.token.lifespan"], out.String())
		}
	}
	// One that does not suit the agent's renewal window is refused before
	// Keycloak is asked.
	a, _, _ := idpHost(t, goodClaims())
	kc, calls, _ := fakeKeycloak(t, false)
	if err := KeycloakApply(a, "sb", Apply{Admin: "admin", URL: kc.URL, TokenLifetime: 2 * time.Hour}, strings.NewReader("pw\n")); err == nil || !strings.Contains(err.Error(), "at most 1h0m0s") {
		t.Errorf("2h: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("Keycloak was asked: %v", *calls)
	}
}
