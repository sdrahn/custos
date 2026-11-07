package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// signIn is the sign-in form of the authorization code flow: any name
// and password are accepted (for tests only); the name is the subject.
var signIn = template.Must(template.New("sign-in").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Sign in · Example Corp</title>
<style>
body { font-family: "SUSE", "Open Sans", sans-serif; background: #0c322c; margin: 0; display: grid; place-items: center; min-height: 100vh; }
main { background: #fff; border-radius: 8px; padding: 2rem 2.5rem; width: 22rem; box-shadow: 0 8px 24px rgba(0,0,0,.25); }
h1 { font-size: 1.4rem; color: #0c322c; margin: 0 0 .3rem; }
p { color: #525252; margin: 0 0 1.4rem; font-size: .95rem; }
label { display: block; font-size: .85rem; color: #0c322c; margin: .8rem 0 .3rem; }
input { width: 100%; box-sizing: border-box; padding: .55rem .6rem; border: 1px solid #b0b0b0; border-radius: 4px; font: inherit; }
button { margin-top: 1.4rem; width: 100%; padding: .6rem; border: 0; border-radius: 4px; background: #30ba78; color: #fff; font: inherit; font-weight: 600; cursor: pointer; }
</style></head>
<body><main>
<h1>Example Corp</h1>
<p>Sign in to continue to {{.Client}}.</p>
<form method="post" action="/authorize">
{{range $k, $v := .Query}}<input type="hidden" name="{{$k}}" value="{{$v}}">
{{end}}<label for="username">User name</label><input id="username" name="username" autocomplete="username" required>
<label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required>
<button type="submit">Sign in</button>
</form>
</main></body></html>
`))

// authorize is the authorization endpoint: GET shows the sign-in form
// for a valid request, POST signs in and redirects with a code.
func (i *IDP) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	q := r.Form
	client, redirect := q.Get("client_id"), q.Get("redirect_uri")
	want, known := i.Logins[client]
	// Errors before the redirect URI is known to be the client's are
	// shown here, never sent to an unknown URI (RFC 6749 §4.1.2.1).
	if !known || redirect != want {
		http.Error(w, "unknown client or redirect URI", http.StatusBadRequest)
		return
	}
	back := func(v url.Values) {
		v.Set("state", q.Get("state"))
		http.Redirect(w, r, redirect+"?"+v.Encode(), http.StatusFound)
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		back(url.Values{"error": {"invalid_request"}})
		return
	}
	if r.Method != http.MethodPost {
		keep := map[string]string{}
		for _, k := range []string{"response_type", "client_id", "redirect_uri", "scope", "state", "nonce", "code_challenge", "code_challenge_method"} {
			keep[k] = q.Get(k)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		signIn.Execute(w, map[string]any{"Client": client, "Query": keep})
		return
	}
	user := strings.TrimSpace(r.PostForm.Get("username"))
	if user == "" || r.PostForm.Get("password") == "" {
		http.Error(w, "a user name and a password are needed", http.StatusBadRequest)
		return
	}
	b := make([]byte, 16)
	rand.Read(b)
	code := hex.EncodeToString(b)
	i.mu.Lock()
	i.codes[code] = grant{user: user, client: client, redirect: redirect, challenge: q.Get("code_challenge"),
		nonce: q.Get("nonce"), expires: i.now().Add(time.Minute)}
	i.mu.Unlock()
	back(url.Values{"code": {code}})
}

// authorizationCode is the token endpoint's authorization code grant:
// the code, once, for the client and redirect URI it was issued to, with
// the PKCE verifier of its challenge (RFC 7636); it returns an ID token
// for the client with the nonce.
func (i *IDP) authorizationCode(w http.ResponseWriter, r *http.Request) {
	fail := func(e string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": e})
	}
	code := r.PostForm.Get("code")
	i.mu.Lock()
	g, ok := i.codes[code]
	delete(i.codes, code)
	i.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || i.now().After(g.expires) || r.PostForm.Get("client_id") != g.client ||
		r.PostForm.Get("redirect_uri") != g.redirect ||
		subtle.ConstantTimeCompare([]byte(b64(sum[:])), []byte(g.challenge)) != 1 {
		fail("invalid_grant")
		return
	}
	extra := map[string]any{"preferred_username": g.user, "azp": g.client}
	if g.nonce != "" {
		extra["nonce"] = g.nonce
	}
	id, err := i.sign(g.user, g.client, 5*time.Minute, extra)
	if err != nil {
		fail("server_error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"id_token": id, "access_token": id, "token_type": "Bearer", "expires_in": 300})
}

// logout is the end-session endpoint: back to post_logout_redirect_uri
// when it is on the origin of a client's redirect URI.
func (i *IDP) logout(w http.ResponseWriter, r *http.Request) {
	after := r.URL.Query().Get("post_logout_redirect_uri")
	if u, err := url.Parse(after); err == nil && after != "" {
		for _, redirect := range i.Logins {
			if c, err := url.Parse(redirect); err == nil && c.Scheme == u.Scheme && c.Host == u.Host {
				http.Redirect(w, r, after, http.StatusFound)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("Signed out.\n"))
}
