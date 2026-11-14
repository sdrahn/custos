// testidp is the token issuer of the OpenShell tests (test/openshell):
// it issues the agents' access tokens for mcp-gateway's HTTPS listener,
// as a site's IdP would with a client per agent (CONTRACT.md §16a). The
// tokens are ES256 JWTs with subject agent-<name> and no local-user
// claim, so the gateway keeps the subject as the agent's principal.
//
// It serves OIDC discovery and the key set, which the gateway fetches
// from the issuer (http is accepted on the loopback), and the token
// endpoint OpenShell's gateway uses to mint agents' tokens with the
// client credentials grant (RFC 6749 §4.4). For tests only: GET /token
// hands out a token to whoever asks.
//
// With --login, it is also the sign-in of people for mcp-gateway's own
// approval page (the demonstration videos, test/video): the
// authorization code flow with PKCE (OpenID Connect Core 3.1), whose
// sign-in form takes any name and password and makes the name the ID
// token's subject. For tests only.
//
//	GET  /.well-known/openid-configuration
//	GET  /jwks
//	GET  /token?sub=agent-sb&aud=https://...&exp=3600   → the JWT
//	POST /token  grant_type=client_credentials&client_id=…&client_secret=…
//	             → {"access_token", "token_type": "Bearer", "expires_in"}
//	GET  /authorize?response_type=code&client_id=…&redirect_uri=…&code_challenge=…&code_challenge_method=S256&state=…&nonce=…
//	             → the sign-in form; POST /authorize → redirect_uri?code=…&state=…
//	POST /token  grant_type=authorization_code&code=…&code_verifier=…&client_id=…&redirect_uri=…
//	             → {"id_token", "access_token", "token_type": "Bearer", "expires_in"}
//	GET  /logout?post_logout_redirect_uri=…   → back to the client
//
// Usage: testidp [--listen 127.0.0.1:18445] [--issuer URL] [--client ID:SECRET:SUBJECT:AUDIENCE]...
//
//	[--login CLIENT_ID=REDIRECT_URI]... [--ttl 3600]
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18445", "address (the issuer is http://ADDRESS)")
	issuer := flag.String("issuer", "", "the issuer, when clients reach it by another address than --listen")
	ttl := flag.Int("ttl", 3600, "lifetime of client credentials tokens, seconds")
	var clients clientFlags
	flag.Var(&clients, "client", "an OAuth client, ID:SECRET:SUBJECT:AUDIENCE (repeatable)")
	var logins loginFlags
	flag.Var(&logins, "login", "a client people sign in to, CLIENT_ID=REDIRECT_URI (repeatable)")
	flag.Parse()
	if *issuer == "" {
		*issuer = "http://" + *listen
	}
	idp, err := New(*issuer)
	if err != nil {
		log.Fatal(err)
	}
	idp.Clients = clients
	idp.Logins = logins
	idp.TTL = time.Duration(*ttl) * time.Second
	log.Fatal(http.ListenAndServe(*listen, idp))
}

// IDP issues tokens signed with one ephemeral P-256 key (kid k1).
type IDP struct {
	Issuer string
	// Clients for the client credentials grant, by id; TTL their tokens'
	// lifetime. Minted counts the tokens issued to each.
	Clients map[string]Client
	TTL     time.Duration
	// Logins are the clients people sign in to (authorization code flow),
	// by id: their redirect URI.
	Logins map[string]string
	key    *ecdsa.PrivateKey
	now    func() time.Time
	mu     sync.Mutex
	minted map[string]int
	codes  map[string]grant
}

// grant is an authorization code's: who signed in, for which client and
// redirect URI, with which PKCE challenge and nonce, until when.
type grant struct {
	user, client, redirect, challenge, nonce string
	expires                                  time.Time
}

type loginFlags map[string]string

func (l *loginFlags) String() string { return fmt.Sprint(len(*l)) }
func (l *loginFlags) Set(v string) error {
	id, uri, ok := strings.Cut(v, "=")
	if !ok || id == "" || !strings.HasPrefix(uri, "http") {
		return fmt.Errorf("want CLIENT_ID=REDIRECT_URI")
	}
	if *l == nil {
		*l = loginFlags{}
	}
	(*l)[id] = uri
	return nil
}

// Client is an OAuth client: its secret, and its tokens' subject and
// audience (what an IdP's client configuration and mappers decide).
type Client struct{ Secret, Subject, Audience string }

type clientFlags map[string]Client

func (c *clientFlags) String() string { return fmt.Sprint(len(*c)) }
func (c *clientFlags) Set(v string) error {
	// The audience is a URL with colons: ID:SECRET:SUBJECT:AUDIENCE.
	p := strings.SplitN(v, ":", 4)
	if len(p) != 4 || p[0] == "" || p[1] == "" || p[2] == "" || p[3] == "" {
		return fmt.Errorf("want ID:SECRET:SUBJECT:AUDIENCE")
	}
	if *c == nil {
		*c = clientFlags{}
	}
	(*c)[p[0]] = Client{Secret: p[1], Subject: p[2], Audience: p[3]}
	return nil
}

func New(issuer string) (*IDP, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &IDP{Issuer: issuer, key: k, now: time.Now, TTL: time.Hour, minted: map[string]int{}, codes: map[string]grant{}}, nil
}

func (i *IDP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		d := map[string]any{
			"issuer": i.Issuer, "jwks_uri": i.Issuer + "/jwks", "token_endpoint": i.Issuer + "/token",
			"id_token_signing_alg_values_supported": []string{"ES256"},
		}
		if len(i.Logins) > 0 {
			d["authorization_endpoint"] = i.Issuer + "/authorize"
			d["end_session_endpoint"] = i.Issuer + "/logout"
			d["response_types_supported"] = []string{"code"}
			d["code_challenge_methods_supported"] = []string{"S256"}
		}
		writeJSON(w, d)
	case "/authorize":
		i.authorize(w, r)
	case "/logout":
		i.logout(w, r)
	case "/jwks":
		writeJSON(w, map[string]any{"keys": []any{i.JWK()}})
	case "/token":
		if r.Method == http.MethodPost {
			if r.PostFormValue("grant_type") == "authorization_code" {
				i.authorizationCode(w, r)
				return
			}
			i.clientCredentials(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("sub") == "" || q.Get("aud") == "" {
			http.Error(w, "want sub and aud", http.StatusBadRequest)
			return
		}
		exp, _ := strconv.Atoi(q.Get("exp"))
		if exp <= 0 {
			exp = 3600
		}
		tok, err := i.Token(q.Get("sub"), q.Get("aud"), time.Duration(exp)*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write([]byte(tok))
	case "/minted":
		// How many tokens each client got (the tests see renewals).
		i.mu.Lock()
		defer i.mu.Unlock()
		writeJSON(w, i.minted)
	default:
		http.NotFound(w, r)
	}
}

// clientCredentials is the client credentials grant: the client by its
// id and secret in the form (client_secret_post) or basic auth.
func (i *IDP) clientCredentials(w http.ResponseWriter, r *http.Request) {
	oauthErr := func(code int, e string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": e})
	}
	if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "client_credentials" {
		oauthErr(http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	c, known := i.Clients[id]
	if !known || subtle.ConstantTimeCompare([]byte(secret), []byte(c.Secret)) != 1 {
		oauthErr(http.StatusUnauthorized, "invalid_client")
		return
	}
	tok, err := i.Token(c.Subject, c.Audience, i.TTL)
	if err != nil {
		oauthErr(http.StatusInternalServerError, "server_error")
		return
	}
	i.mu.Lock()
	i.minted[id]++
	i.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": int(i.TTL.Seconds())})
}

// JWK is the public key as a JSON Web Key.
func (i *IDP) JWK() map[string]any {
	b, _ := i.key.PublicKey.Bytes() // 0x04 || X || Y
	return map[string]any{"kty": "EC", "crv": "P-256", "kid": "k1", "use": "sig", "alg": "ES256",
		"x": b64(b[1:33]), "y": b64(b[33:65])}
}

// Token signs a JWT for sub and aud, valid for ttl.
func (i *IDP) Token(sub, aud string, ttl time.Duration) (string, error) {
	return i.sign(sub, aud, ttl, nil)
}

// sign signs a JWT for sub and aud, valid for ttl, with extra claims.
func (i *IDP) sign(sub, aud string, ttl time.Duration, extra map[string]any) (string, error) {
	now := i.now()
	claims := jwt.MapClaims{"iss": i.Issuer, "sub": sub, "aud": aud,
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(ttl).Unix()}
	for k, v := range extra {
		claims[k] = v
	}
	t := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	t.Header["kid"] = "k1"
	return t.SignedString(i.key)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
