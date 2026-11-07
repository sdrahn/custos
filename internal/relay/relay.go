// Package relay is the host side of an agent in an OpenShell sandbox
// (CONTRACT.md §3a, §16a): it owns the agent's socket
// /run/agents/<name>/api.sock, identifies callers by SO_PEERCRED, and
// relays to the agent's control API through OpenShell's port forward,
// passing the caller with the shared secret. It also turns the agent's
// records from OpenShell's log stream into journal records on the host.
package relay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/peercred"
)

// Handler relays to upstream (http://127.0.0.1:<forwarded port>).
// Callers come from SO_PEERCRED (peercred.ConnContext on the server);
// any relay headers a caller sends are dropped.
func Handler(upstream *url.URL, secret string) http.Handler {
	p := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.Header.Del(peercred.HeaderCaller)
			pr.Out.Header.Del(peercred.HeaderSecret)
			c, _ := peercred.From(pr.In.Context())
			pr.Out.Header.Set(peercred.HeaderCaller, fmt.Sprintf("%d:%s", c.UID, c.Name))
			pr.Out.Header.Set(peercred.HeaderSecret, secret)
		},
		FlushInterval: -1, // AG-UI and run_sse stream
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := peercred.From(r.Context()); !ok {
			http.Error(w, "caller unknown", http.StatusForbidden)
			return
		}
		p.ServeHTTP(w, r)
	})
}

// Bridge reads the agent's log stream (JSON lines, CONTRACT.md §10)
// and writes each record of contract 1 to log. Other lines (OpenShell's
// own, the agent's plain output) are skipped. A stream may prefix lines
// (a timestamp, the sandbox name): the record is the JSON object from
// the first '{'. It returns the number of records written.
func Bridge(r io.Reader, name string, log journal.Logger) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		line := sc.Text()
		i := strings.IndexByte(line, '{')
		if i < 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line[i:]), &m) != nil {
			continue
		}
		if m["contract"] != "1" || m["name"] != name {
			continue
		}
		// Only what an agent host writes: the relay writes as root, and
		// root's records of agent-admin's events (reviews, feedback,
		// permissions, stops) are trusted (security review H1).
		event, _ := m["event"].(string)
		if !journal.HostEvents[event] {
			continue
		}
		msg, _ := m["message"].(string)
		f := journal.Fields{}
		for k, v := range m {
			switch k {
			case "contract", "name", "event", "message", "message_id":
				continue
			}
			s, ok := v.(string)
			if !ok || !journal.ValidField(strings.ToUpper(k)) || k != strings.ToLower(k) {
				continue
			}
			f[strings.ToUpper(k)] = s
		}
		log.Log(event, msg, f)
		n++
	}
	return n, sc.Err()
}
