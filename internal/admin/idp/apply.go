package idp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gatewayconf"
)

// Apply are idp-client --keycloak --apply's settings.
type Apply struct {
	// Admin is a Keycloak user that may manage the realm's clients, and
	// AdminRealm the realm it logs in to (master).
	Admin, AdminRealm string
	// URL is Keycloak's address as this host reaches it; "": the gateway
	// issuer's (…/realms/<realm> without the realm).
	URL string
}

// KeycloakApply makes agent NAME's OAuth client in the Keycloak realm of
// the gateway's issuer, through Keycloak's admin API, as the script of
// idp-client --keycloak does, logged in as an administrator with the
// password from password. A client of that id already there is replaced
// (a new secret). It then lists the issuer for agents if it is not,
// checks a token with the new secret (check-token) and sets it as the
// agent's gateway-client-secret, which makes the sandbox. The secret
// stays in memory; the administrator's password is used for this run
// only and kept nowhere.
func KeycloakApply(a *admin.Admin, name string, o Apply, password io.Reader) error {
	cfg, err := config.Load(a.AgentDir(name))
	if err != nil {
		return fmt.Errorf("agent %s: %w (create it first)", name, err)
	}
	if cfg.Sandbox.Kind != "openshell" || cfg.Gateway.OAuth == nil {
		return fmt.Errorf("agent %s has no OAuth client (gateway.oauth in an OpenShell sandbox): nothing to make", name)
	}
	h, err := gatewayconf.ReadHTTP(a.Root, a.GatewayConfig)
	if err != nil {
		return err
	}
	base, realm, ok := keycloakRealm(h.Issuer)
	if !ok {
		return fmt.Errorf("the gateway's issuer %s is no Keycloak realm (…/realms/<realm>): make the client as agent-admin idp-client %s says", h.Issuer, name)
	}
	if o.URL != "" {
		base = strings.TrimRight(o.URL, "/")
	}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "https" && !(plainHTTPLoopback && u.Scheme == "http" && loopback(u.Hostname()))) {
		return fmt.Errorf("Keycloak at %s: the administrator's password goes over https only (security review 0.9, T1: on plain http, whoever holds the port gets it)", base)
	}
	if o.AdminRealm == "" {
		o.AdminRealm = "master"
	}
	if o.Admin == "" {
		return errors.New("--admin: a Keycloak user that may manage the realm's clients")
	}
	b, err := io.ReadAll(io.LimitReader(password, 4<<10))
	if err != nil {
		return err
	}
	pw := strings.TrimRight(string(b), "\r\n")
	if pw == "" {
		return fmt.Errorf("no password for Keycloak user %s on standard input", o.Admin)
	}
	// No redirect is followed: a 307 or 308 would send the password's
	// form on, to wherever it points (security review 0.9, T3).
	hc := &http.Client{Timeout: 30 * time.Second}
	if a.HTTP != nil {
		*hc = *a.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	kc := &keycloakAPI{http: hc, base: base, realm: realm}
	if err := kc.login(o.AdminRealm, o.Admin, pw); err != nil {
		return err
	}
	clientID := cfg.Gateway.OAuth.ClientID
	// --apply replaces the client of that id: only the agent's own
	// (security review 0.9, T2), never another agent's or an application's.
	if clientID != "agent-"+name {
		return fmt.Errorf("agent %s's gateway.oauth.client_id is %s: --apply makes and replaces only agent-%s; make another client as agent-admin idp-client %s says", name, clientID, name, name)
	}
	secret, err := kc.makeClient(keycloakClient(clientID, "agent-"+name, h), clientID, h.Scopes)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "set  Keycloak client %s in realm %s (%s)\n", clientID, realm, base)
	listed := false
	if trusted, untrusted := a.TrustedIssuers(); untrusted != "" {
		return fmt.Errorf("the issuers for agents: %s", untrusted)
	} else if !slices.Contains(trusted, h.Issuer) {
		if err := a.Issuers([]string{h.Issuer}, nil); err != nil {
			return err
		}
		listed = true
		fmt.Fprintf(a.Out, "set  %s listed for agents (%s)\n", h.Issuer, admin.GatewayHTTPIssuers)
	}
	if err := CheckToken(a, name, strings.NewReader(secret)); err != nil {
		// The issuer listed here goes again when its token is not what the
		// gateway expects (security review 0.9, T4).
		if listed {
			if uerr := a.Issuers(nil, []string{h.Issuer}); uerr == nil {
				fmt.Fprintf(a.Out, "set  %s no longer listed for agents: the token is not right yet\n", h.Issuer)
			}
		}
		return err
	}
	return a.SetCredential(name, "gateway-client-secret", strings.NewReader(secret))
}

// keycloakAPI is the part of Keycloak's admin REST API that makes a
// client, logged in as an administrator (admin-cli, password grant).
type keycloakAPI struct {
	http        *http.Client
	base, realm string
	token       string
}

func (k *keycloakAPI) login(adminRealm, user, password string) error {
	form := url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {user}, "password": {password}}
	u := k.base + "/realms/" + url.PathEscape(adminRealm) + "/protocol/openid-connect/token"
	resp, err := k.http.PostForm(u, form)
	if err != nil {
		return fmt.Errorf("Keycloak at %s: %w", k.base, err)
	}
	defer resp.Body.Close()
	var t struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error_description"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t)
	if resp.StatusCode != http.StatusOK || t.AccessToken == "" {
		return fmt.Errorf("Keycloak refused %s in realm %s (%d): %s", user, adminRealm, resp.StatusCode, orNone(t.Error))
	}
	k.token = t.AccessToken
	return nil
}

// call is one admin API request on the realm; out, if not nil, takes the
// answer's JSON. It returns the answer's Location (where POST made it).
func (k *keycloakAPI) call(method, path string, in, out any) (string, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return "", err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, k.base+"/admin/realms/"+url.PathEscape(k.realm)+path, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("Keycloak %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		why := strings.TrimSpace(string(b))
		if resp.StatusCode == http.StatusForbidden {
			why = "the user may not manage this realm's clients (realm-management: manage-clients)"
		}
		return "", fmt.Errorf("Keycloak %s %s (%d): %s", method, path, resp.StatusCode, why)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return "", fmt.Errorf("Keycloak %s %s: %w", method, path, err)
		}
	}
	return resp.Header.Get("Location"), nil
}

type named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// makeClient makes the client (replacing one of that client id), its
// scopes as the script does, and returns its secret.
func (k *keycloakAPI) makeClient(client map[string]any, clientID string, scopes []string) (string, error) {
	var have []named
	if _, err := k.call("GET", "/client-scopes", nil, &have); err != nil {
		return "", err
	}
	scopeID := map[string]string{}
	for _, s := range have {
		scopeID[s.Name] = url.PathEscape(s.ID)
	}
	// The gateway's scopes, made where the realm lacks them, in the token's scope.
	for _, sc := range scopes {
		if scopeID[sc] != "" {
			continue
		}
		loc, err := k.call("POST", "/client-scopes", map[string]any{"name": sc, "protocol": "openid-connect",
			"attributes": map[string]string{"include.in.token.scope": "true"}}, nil)
		if err != nil {
			return "", err
		}
		scopeID[sc] = url.PathEscape(loc[strings.LastIndex(loc, "/")+1:])
	}
	var old []struct {
		ID string `json:"id"`
	}
	if _, err := k.call("GET", "/clients?clientId="+url.QueryEscape(clientID), nil, &old); err != nil {
		return "", err
	}
	for _, c := range old {
		if _, err := k.call("DELETE", "/clients/"+url.PathEscape(c.ID), nil, nil); err != nil {
			return "", err
		}
	}
	loc, err := k.call("POST", "/clients", client, nil)
	if err != nil {
		return "", err
	}
	id := url.PathEscape(loc[strings.LastIndex(loc, "/")+1:])
	if id == "" {
		return "", errors.New("Keycloak made the client but did not say where")
	}
	// The realm's default client scopes go ("basic" maps a sub of its
	// own, the service account's id); the gateway's scopes are the defaults.
	var defaults []named
	if _, err := k.call("GET", "/clients/"+id+"/default-client-scopes", nil, &defaults); err != nil {
		return "", err
	}
	for _, d := range defaults {
		if !slices.Contains(scopes, d.Name) {
			if _, err := k.call("DELETE", "/clients/"+id+"/default-client-scopes/"+url.PathEscape(d.ID), nil, nil); err != nil {
				return "", err
			}
		}
	}
	for _, sc := range scopes {
		if _, err := k.call("PUT", "/clients/"+id+"/default-client-scopes/"+scopeID[sc], nil, nil); err != nil {
			return "", err
		}
	}
	var secret struct {
		Value string `json:"value"`
	}
	if _, err := k.call("GET", "/clients/"+id+"/client-secret", nil, &secret); err != nil {
		return "", err
	}
	if secret.Value == "" {
		return "", errors.New("Keycloak gave the client no secret")
	}
	return secret.Value, nil
}

// plainHTTPLoopback lets the package's tests reach a Keycloak on this
// host's loopback over plain http; agent-admin never does (T1).
var plainHTTPLoopback = false

func loopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
