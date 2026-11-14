package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"gopkg.in/yaml.v3"

	"github.com/sdrahn/custos/internal/config"
	"github.com/sdrahn/custos/internal/gatewayconf"
	"github.com/sdrahn/custos/internal/host"
)

// The site's identity provider issues an OpenShell agent's tokens
// (CONTRACT.md §16a, #111): one OAuth client per agent, client
// credentials grant. idp-client says what that client must be, from the
// gateway's own settings, with a Keycloak script to create it; check-token
// mints a token with the agent's client and its secret (on standard input,
// before set-credential hands it to OpenShell, which keeps it) and checks
// it as the gateway will.

// GatewayHTTPIssuers lists the issuers the gateway accepts agent-*
// subjects from (§16a).
const GatewayHTTPIssuers = "/etc/mcp-gateway/policy/agents/_http.yaml"

// MaxTokenLifetime is the longest token lifetime check-token accepts
// without a warning: OpenShell renews tokens, so they can be short.
const MaxTokenLifetime = time.Hour

// gatewayHTTP is what the gateway checks in a token.
func (a *Admin) gatewayHTTP() (*gatewayconf.HTTP, error) {
	return gatewayconf.ReadHTTP(a.Root, a.GatewayConfig)
}

func (a *Admin) agentIssuers() ([]string, error) {
	b, err := os.ReadFile(a.Path(GatewayHTTPIssuers))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		HTTP struct {
			Issuers []string `yaml:"issuers"`
		} `yaml:"_http"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", GatewayHTTPIssuers, err)
	}
	return doc.HTTP.Issuers, nil
}

// trustedIssuers are the issuers the gateway trusts for agents: _http.yaml's,
// unless the seal covers it and it does not match (then none, and why;
// security review 0.6, N6).
func (a *Admin) trustedIssuers() ([]string, string) {
	issuers, err := a.agentIssuers()
	if err != nil {
		return nil, err.Error()
	}
	if pub, agents, err := a.sealInfo(); err == nil && agents && !a.entryVerified(pub, HTTPIssuersName) {
		return nil, GatewayHTTPIssuers + " does not match its seal, so the gateway trusts no issuer: check it, then accept it " +
			"(agent-admin seal --agents --accept _http)"
	}
	return issuers, ""
}

// keycloakRealm is the realm of a Keycloak issuer URL (…/realms/<realm>).
func keycloakRealm(issuer string) (base, realm string, ok bool) {
	i := strings.Index(issuer, "/realms/")
	if i < 0 {
		return "", "", false
	}
	return issuer[:i], strings.Trim(issuer[i+len("/realms/"):], "/"), true
}

// IDPClient prints what the agent's OAuth client must be, from the
// gateway's settings; keycloak prints a script that creates it in the
// gateway's Keycloak realm instead.
func (a *Admin) IDPClient(name string, keycloak bool) error {
	if err := host.CheckName(name); err != nil {
		return err
	}
	h, err := a.gatewayHTTP()
	if err != nil {
		return err
	}
	clientID := "agent-" + name
	if cfg, err := config.Load(a.agentDir(name)); err == nil && cfg.Gateway.OAuth != nil && cfg.Gateway.OAuth.ClientID != "" {
		clientID = cfg.Gateway.OAuth.ClientID
	}
	if keycloak {
		base, realm, ok := keycloakRealm(h.Issuer)
		if !ok {
			return fmt.Errorf("the gateway's issuer %s is no Keycloak realm (…/realms/<realm>)", h.Issuer)
		}
		fmt.Fprint(a.Out, keycloakScript(base, realm, clientID, "agent-"+name, h))
		return nil
	}
	issuers, untrusted := a.trustedIssuers()
	listed := "listed"
	switch {
	case untrusted != "":
		listed = "NOT trusted: " + untrusted
	case !slices.Contains(issuers, h.Issuer):
		listed = "NOT listed yet: agent-admin issuers --add " + h.Issuer
	}
	fmt.Fprintf(a.Out, `The OAuth client of agent %[1]s at the gateway's identity provider:

  client id     %[2]s
  grant         client credentials only (no standard flow, no direct access grants)
  issuer        %[3]s (%[4]s for agents)

Its access tokens must have:

  iss    %[3]s
  sub    agent-%[1]s   (the gateway's principal; a provider's own id, such as a service account's, will not do)
  aud    %[5]s
  scope  %[6]s
  exp    a short lifetime: OpenShell renews the token (5 minutes, say; at most %[7]s)
%[8]s
Keycloak: agent-admin idp-client %[1]s --keycloak prints a script that creates it.
Then: agent-admin check-token %[1]s < secret, and agent-admin set-credential %[1]s gateway-client-secret < secret.
`, name, clientID, h.Issuer, listed, h.Audience, orNone(strings.Join(h.Scopes, " ")), MaxTokenLifetime,
		localUserNote(h.LocalUserClaim))
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(none required)"
	}
	return s
}

func localUserNote(claim string) string {
	if claim == "" {
		return ""
	}
	return "\nand no claim " + claim + " (the gateway's local_user_claim): an agent is never a person's local account.\n"
}

// keycloakScript creates the agent's client in a Keycloak realm with
// kcadm.sh (KCADM overrides the command; it must be logged in): a
// confidential client with the service account only, the subject agent-<name>
// by a hardcoded claim, the gateway's audience, its scopes, a 5-minute
// token lifetime. The realm's default client scopes are left out, "basic"
// with its own sub mapper among them, so that the hardcoded sub is the only
// one.
func keycloakScript(base, realm, clientID, sub string, h *gatewayconf.HTTP) string {
	client := map[string]any{
		"clientId": clientID, "enabled": true, "protocol": "openid-connect",
		"publicClient": false, "clientAuthenticatorType": "client-secret",
		"serviceAccountsEnabled": true, "standardFlowEnabled": false, "directAccessGrantsEnabled": false,
		"implicitFlowEnabled": false, "fullScopeAllowed": false,
		"defaultClientScopes": []string{}, "optionalClientScopes": []string{},
		"attributes": map[string]string{"access.token.lifespan": "300"},
		"protocolMappers": []map[string]any{
			{"name": "agent subject", "protocol": "openid-connect", "protocolMapper": "oidc-hardcoded-claim-mapper",
				"config": map[string]string{"claim.name": "sub", "claim.value": sub, "jsonType.label": "String",
					"access.token.claim": "true", "id.token.claim": "false", "userinfo.token.claim": "false", "introspection.token.claim": "true"}},
			{"name": "gateway audience", "protocol": "openid-connect", "protocolMapper": "oidc-audience-mapper",
				"config": map[string]string{"included.custom.audience": h.Audience, "access.token.claim": "true",
					"id.token.claim": "false", "introspection.token.claim": "true"}},
		},
	}
	cj, _ := json.MarshalIndent(client, "", "  ")
	var b strings.Builder
	fmt.Fprintf(&b, `#!/bin/bash
# The OAuth client of %[1]s in Keycloak realm %[2]s (custos #111; user
# guide, chapter 11). Log kcadm.sh in first, for example:
#   kcadm.sh config credentials --server %[3]s --realm master --user admin
# KCADM overrides the command (KCADM="podman exec -i keycloak /opt/keycloak/bin/kcadm.sh").
set -euo pipefail
kcadm=${KCADM:-kcadm.sh}
realm=%[2]s
`, clientID, realm, base)
	for _, sc := range h.Scopes {
		fmt.Fprintf(&b, `# The gateway requires the scope %[1]s: a client scope of that name, in the token's scope.
if ! $kcadm get client-scopes -r "$realm" --fields name --format csv --noquotes | grep -qx %[1]q; then
	$kcadm create client-scopes -r "$realm" -s name=%[1]q -s protocol=openid-connect -s 'attributes."include.in.token.scope"=true' >/dev/null
fi
`, sc)
	}
	fmt.Fprintf(&b, `$kcadm create clients -r "$realm" -f - >/dev/null <<'EOF'
%s
EOF
id=$($kcadm get clients -r "$realm" -q clientId=%s --fields id --format csv --noquotes)
# The realm's default client scopes, which Keycloak may add all the same, go:
# "basic" among them maps a sub of its own (the service account's id).
$kcadm get "clients/$id/default-client-scopes" -r "$realm" --fields id,name --format csv --noquotes |
while IFS=, read -r sid name; do
	case " %s " in *" $name "*) ;; *) $kcadm delete "clients/$id/default-client-scopes/$sid" -r "$realm" ;; esac
done
`, cj, clientID, strings.Join(h.Scopes, " "))
	for _, sc := range h.Scopes {
		fmt.Fprintf(&b, `sid=$($kcadm get client-scopes -r "$realm" --fields id,name --format csv --noquotes | awk -F, '$2 == %q { print $1 }')
$kcadm update "clients/$id/default-client-scopes/$sid" -r "$realm" >/dev/null
`, sc)
	}
	n := strings.TrimPrefix(sub, "agent-")
	fmt.Fprintf(&b, `# Its secret, the script's only output: agent-admin check-token %s < secret,
# then agent-admin set-credential %s gateway-client-secret < secret.
$kcadm get "clients/$id/client-secret" -r "$realm" --fields value --format csv --noquotes
`, n, n)
	return b.String()
}

// CheckToken mints a token with the agent's OAuth client (gateway.oauth,
// and the client's secret read from secret) and checks each claim the gateway
// relies on, printing one line per check. The token's signature is the
// gateway's to check (with the issuer's keys); this checks what the
// identity provider's client configuration decides.
func (a *Admin) CheckToken(name string, secretIn io.Reader) error {
	cfg, err := config.Load(a.agentDir(name))
	if err != nil {
		return err
	}
	o := cfg.Gateway.OAuth
	if o == nil {
		return fmt.Errorf("agent %s has no OAuth client (gateway.oauth): its token is static (gateway-token)", name)
	}
	h, err := a.gatewayHTTP()
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(secretIn, 64<<10))
	if err != nil {
		return err
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" {
		return fmt.Errorf("no client secret on standard input: agent-admin check-token %s < secret", name)
	}
	tok, err := a.mintToken(o, secret)
	if err != nil {
		return err
	}
	claims, err := jwtClaims(tok)
	if err != nil {
		return fmt.Errorf("the issuer's access token: %w", err)
	}
	if _, err := a.agentIssuers(); err != nil {
		return err
	}
	failed := 0
	say := func(ok bool, what, why string) {
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			failed++
		}
		if why != "" && !ok {
			what += ": " + why
		}
		fmt.Fprintf(a.Out, "%s %s\n", mark, what)
	}
	str := func(k string) string { s, _ := claims[k].(string); return s }
	iss := str("iss")
	say(iss == h.Issuer, "iss "+iss, "the gateway's http.issuer is "+h.Issuer)
	trusted, untrusted := a.trustedIssuers()
	why := "agent-admin issuers --add " + iss + ", or the gateway denies agent-* subjects from it everything"
	if untrusted != "" {
		why = untrusted
	}
	say(untrusted == "" && slices.Contains(trusted, iss), "the issuer is listed for agents ("+GatewayHTTPIssuers+")", why)
	sub := str("sub")
	say(sub == "agent-"+name, "sub "+sub, "want agent-"+name+" (in Keycloak: the client's hardcoded sub claim, and no default client scope that maps its own)")
	var auds []string
	switch v := claims["aud"].(type) {
	case string:
		auds = []string{v}
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				auds = append(auds, s)
			}
		}
	}
	say(slices.Contains(auds, h.Audience), "aud includes "+h.Audience, "aud is "+strings.Join(auds, ", "))
	have := strings.Fields(str("scope"))
	for _, sc := range h.Scopes {
		say(slices.Contains(have, sc), "scope "+sc, "the token's scope is "+orNone(str("scope")))
	}
	exp, _ := claims["exp"].(float64)
	iat, _ := claims["iat"].(float64)
	now := a.Now()
	switch {
	case exp == 0:
		say(false, "exp", "the token does not expire")
	case time.Unix(int64(exp), 0).Before(now):
		say(false, "exp", "the token has expired already (the host's clock?)")
	case iat > 0 && time.Duration(exp-iat)*time.Second > MaxTokenLifetime:
		say(false, fmt.Sprintf("lifetime %s", time.Duration(exp-iat)*time.Second), "longer than "+MaxTokenLifetime.String()+": OpenShell renews it, keep it short")
	default:
		say(true, fmt.Sprintf("exp in %s", time.Unix(int64(exp), 0).Sub(now).Round(time.Second)), "")
	}
	if c := h.LocalUserClaim; c != "" {
		_, has := claims[c]
		say(!has, "no "+c+" (the gateway's local_user_claim)", "an agent's token must not name a local account")
	}
	if failed > 0 {
		return fmt.Errorf("agent %s's token: %d check(s) failed", name, failed)
	}
	fmt.Fprintf(a.Out, "agent %s's token is what the gateway expects\n", name)
	return nil
}

// mintToken asks the issuer for a token with the client credentials grant.
func (a *Admin) mintToken(o *config.OAuth, secret string) (string, error) {
	c := a.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	cc := clientcredentials.Config{ClientID: o.ClientID, ClientSecret: secret, TokenURL: o.TokenURL,
		Scopes: o.Scopes, AuthStyle: oauth2.AuthStyleInHeader}
	tok, err := cc.Token(context.WithValue(context.Background(), oauth2.HTTPClient, c))
	var refused *oauth2.RetrieveError
	switch {
	case errors.As(err, &refused):
		why := strings.TrimSpace(refused.ErrorCode + " " + refused.ErrorDescription)
		if why == "" {
			why = strings.TrimSpace(string(refused.Body))
		}
		return "", fmt.Errorf("the issuer refused client %s (%d): %s", o.ClientID, refused.Response.StatusCode, why)
	case err != nil:
		return "", fmt.Errorf("the issuer (%s): %w", o.TokenURL, err)
	}
	return tok.AccessToken, nil
}

// jwtClaims are a JWT's claims, its signature unchecked: check-token
// compares them with what the gateway expects, and the gateway verifies.
func jwtClaims(tok string) (map[string]any, error) {
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(tok, claims); err != nil {
		return nil, errors.New("not a JWT")
	}
	return claims, nil
}
