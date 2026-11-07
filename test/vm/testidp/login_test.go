package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const redirect = "https://127.0.0.1:18443/approvals/callback"

func loginIdP(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(nil)
	t.Cleanup(srv.Close)
	idp, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	idp.Logins = map[string]string{"approvals": redirect}
	srv.Config.Handler = idp
	// The redirects to the client are looked at, not followed.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return srv, c
}

func authorizeQuery(challenge string) url.Values {
	return url.Values{"response_type": {"code"}, "client_id": {"approvals"}, "redirect_uri": {redirect},
		"state": {"st-1"}, "nonce": {"n-1"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "scope": {"openid"}}
}

// signIn posts the sign-in form and returns the redirect's query.
func signInAs(t *testing.T, srv *httptest.Server, c *http.Client, q url.Values, user string) url.Values {
	t.Helper()
	f := url.Values{}
	for k, v := range q {
		f[k] = v
	}
	f.Set("username", user)
	f.Set("password", "anything")
	res, err := c.PostForm(srv.URL+"/authorize", f)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, err := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || err != nil || !strings.HasPrefix(loc.String(), redirect+"?") {
		t.Fatalf("sign-in: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	return loc.Query()
}

func exchange(t *testing.T, srv *httptest.Server, code, verifier string) (int, map[string]any) {
	t.Helper()
	res, err := http.PostForm(srv.URL+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"code_verifier": {verifier}, "client_id": {"approvals"}, "redirect_uri": {redirect}})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}

// The authorization code flow with PKCE, as mcp-gateway's approval page
// runs it: discovery names the endpoints, the form signs in whoever it
// is given, the code becomes an ID token for the client with the nonce
// and the name as its subject, once.
func TestLoginCodeFlow(t *testing.T) {
	srv, c := loginIdP(t)
	var disc map[string]any
	json.Unmarshal(get(t, srv.URL+"/.well-known/openid-configuration"), &disc)
	if disc["authorization_endpoint"] != srv.URL+"/authorize" || disc["token_endpoint"] != srv.URL+"/token" ||
		disc["end_session_endpoint"] != srv.URL+"/logout" {
		t.Fatalf("discovery: %v", disc)
	}
	verifier := "a-verifier-of-at-least-43-characters-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	q := authorizeQuery(base64.RawURLEncoding.EncodeToString(sum[:]))

	form := string(get(t, srv.URL+"/authorize?"+q.Encode()))
	if !strings.Contains(form, `name="username"`) || !strings.Contains(form, `value="n-1"`) || !strings.Contains(form, "approvals") {
		t.Fatalf("the sign-in form: %s", form)
	}
	back := signInAs(t, srv, c, q, "carol")
	if back.Get("state") != "st-1" || back.Get("code") == "" {
		t.Fatalf("redirect: %v", back)
	}
	if code, m := exchange(t, srv, back.Get("code"), "the wrong verifier, which is long enough anyway"); code != 400 || m["error"] != "invalid_grant" {
		t.Errorf("a wrong verifier: %d %v", code, m)
	}
	// The failed exchange used the code up.
	back = signInAs(t, srv, c, q, "carol")
	code, m := exchange(t, srv, back.Get("code"), verifier)
	if code != 200 {
		t.Fatalf("exchange: %d %v", code, m)
	}
	parts := strings.Split(m["id_token"].(string), ".")
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	json.Unmarshal(b, &claims)
	if claims["iss"] != srv.URL || claims["aud"] != "approvals" || claims["sub"] != "carol" || claims["nonce"] != "n-1" {
		t.Errorf("claims: %v", claims)
	}
	if code, _ := exchange(t, srv, back.Get("code"), verifier); code != 400 {
		t.Error("a code was accepted twice")
	}
}

// Requests for another client or redirect URI are refused here and never
// sent anywhere; a request without PKCE goes back to the client with an
// error; signing out returns only to the client's origin.
func TestLoginRefusals(t *testing.T) {
	srv, c := loginIdP(t)
	q := authorizeQuery("challenge")
	q.Set("redirect_uri", "https://evil.example/cb")
	res, err := c.Get(srv.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 || res.Header.Get("Location") != "" {
		t.Errorf("another redirect URI: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	q = authorizeQuery("")
	q.Del("code_challenge")
	res, err = c.Get(srv.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if loc := res.Header.Get("Location"); res.StatusCode != 302 || !strings.Contains(loc, "error=invalid_request") {
		t.Errorf("without PKCE: %d %q", res.StatusCode, loc)
	}
	res, err = c.Get(srv.URL + "/logout?post_logout_redirect_uri=" + url.QueryEscape("https://127.0.0.1:18443/approvals/signed-out"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 302 {
		t.Errorf("sign-out to the client: %d", res.StatusCode)
	}
	res, err = c.Get(srv.URL + "/logout?post_logout_redirect_uri=" + url.QueryEscape("https://evil.example/"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Location") != "" || !strings.Contains(string(b), "Signed out") {
		t.Errorf("sign-out elsewhere: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestLoginFlag(t *testing.T) {
	var l loginFlags
	if err := l.Set("approvals=" + redirect); err != nil || l["approvals"] != redirect {
		t.Fatalf("%v %v", err, l)
	}
	if err := l.Set("approvals"); err == nil {
		t.Error("accepted a login without a redirect URI")
	}
}
