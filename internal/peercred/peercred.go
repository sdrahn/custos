// Package peercred identifies callers on the agent's unix socket by
// SO_PEERCRED and limits what they may reach (CONTRACT.md §10).
package peercred

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

type ctxKey struct{}

// Caller is the local account on the other end of the connection.
type Caller struct {
	UID  int
	Name string
}

// Root reports whether the caller is root (Cockpit's administrative access).
func (c Caller) Root() bool { return c.UID == 0 }

// Agent returns the agent name if the caller is an agent's user
// (agent-<name>, CONTRACT.md §2), or the delegation bridge acting for one
// (DelegateUser with HeaderDelegator, delegated).
func (c Caller) Agent() (string, bool) {
	if len(c.Name) > 6 && c.Name[:6] == "agent-" {
		return c.Name[6:], true
	}
	return "", false
}

// ConnContext is http.Server.ConnContext: it records the peer's uid.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return ctx
	}
	var cred *syscall.Ucred
	raw.Control(func(fd uintptr) {
		cred, err = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || cred == nil {
		return ctx
	}
	name := strconv.Itoa(int(cred.Uid))
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	return WithCaller(ctx, Caller{UID: int(cred.Uid), Name: name})
}

// WithCaller sets the caller (tests, and ConnContext).
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// From returns the caller of a request.
func From(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(ctxKey{}).(Caller)
	return c, ok
}

var (
	userPath = regexp.MustCompile(`^/apps/([^/]+)/users/([^/]+)/sessions(/[^/]+(/artifacts(/.*)?)?)?$`)
	open     = map[string]bool{"/health": true, "/version": true, "/list-apps": true}
	// The control API (CONTRACT.md §5): who may use it is checked by its
	// handlers, except that agents only delegate.
	control  = regexp.MustCompile(`^/v1/(status|playbooks|activities(/[^/]+(/cancel)?)?|pause|resume)$`)
	agentMay = regexp.MustCompile(`^/v1/activities(/[^/]+)?$`)
)

// The delegation bridge (agent-delegate, CONTRACT.md §9) runs as
// DelegateUser, whichever agent delegates, and names the delegating
// agent in HeaderDelegator: the gateway told it whom it serves.
const (
	DelegateUser    = "custos-delegate"
	HeaderDelegator = "X-Custos-Delegator"
)

// agentName is an agent's name (config.ValidName, which this package may
// not import).
var agentName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$`)

// delegated is the caller a request is from: the delegating agent's user
// for the bridge (DelegateUser) naming one in HeaderDelegator, the caller
// itself for anyone else, whose HeaderDelegator is ignored. The bridge
// without a delegator is refused (false).
func delegated(c Caller, r *http.Request) (Caller, bool) {
	d := r.Header.Get(HeaderDelegator)
	r.Header.Del(HeaderDelegator)
	if c.Name != DelegateUser {
		return c, true
	}
	if !agentName.MatchString(d) {
		return c, false
	}
	return Caller{UID: c.UID, Name: "agent-" + d}, true
}

// Guard allows only the paths of the contract, and only the caller's own
// sessions unless the caller is root. Everything else is 404, so the ADK
// server's builder and debug endpoints are not reachable. Agents (the
// delegation bridge) may only start and read activities.
func Guard(app string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := From(r.Context())
		if !ok {
			http.Error(w, "caller unknown", http.StatusForbidden)
			return
		}
		if c, ok = delegated(c, r); !ok {
			http.Error(w, "the delegation bridge names no delegating agent", http.StatusForbidden)
			return
		}
		r = r.WithContext(WithCaller(r.Context(), c))
		p := r.URL.Path
		if _, isAgent := c.Agent(); isAgent && !agentMay.MatchString(p) && !open[p] {
			http.Error(w, "agents may only delegate", http.StatusForbidden)
			return
		}
		switch {
		case open[p], control.MatchString(p), p == "/agui":
		case p == "/run" || p == "/run_sse":
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var req struct {
				AppName string `json:"appName"`
				UserID  string `json:"userId"`
			}
			if json.Unmarshal(body, &req) != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if req.AppName != app || (!c.Root() && req.UserID != c.Name) {
				http.Error(w, "not your session", http.StatusForbidden)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		default:
			m := userPath.FindStringSubmatch(p)
			if m == nil || m[1] != app {
				http.NotFound(w, r)
				return
			}
			if !c.Root() && m[2] != c.Name {
				http.Error(w, "not your session", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Relay headers (CONTRACT.md §3a): the host relay of a sandboxed agent
// passes the caller it saw by SO_PEERCRED, with the secret it shares
// with the agent.
const (
	HeaderCaller = "X-Custos-Caller" // "<uid>:<name>"
	HeaderSecret = "X-Custos-Relay-Secret"
)

// Relayed identifies callers by the relay headers, for an agent that
// listens on TCP inside its sandbox. Without the right secret, or
// without a caller, a request is refused.
func Relayed(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(HeaderSecret)
		if secret == "" || subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
			http.Error(w, "not from the relay", http.StatusForbidden)
			return
		}
		uid, name, ok := strings.Cut(r.Header.Get(HeaderCaller), ":")
		n, err := strconv.Atoi(uid)
		if !ok || err != nil || n < 0 || name == "" {
			http.Error(w, "caller unknown", http.StatusForbidden)
			return
		}
		r.Header.Del(HeaderSecret)
		next.ServeHTTP(w, r.WithContext(WithCaller(r.Context(), Caller{UID: n, Name: name})))
	})
}
