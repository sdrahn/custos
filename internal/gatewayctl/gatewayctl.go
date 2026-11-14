// Package gatewayctl talks to mcp-gateway's control API on its control
// socket, as root: notices to approvers (the gateway's POST /v1/notices,
// Custos's requirement R3) and a server's running instances.
package gatewayctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// client reaches the control API on sock.
func client(sock string) *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
}

// ControlSocket is mcp-gateway's control socket, where root posts notices
// to approvers (its requirement R3).
const ControlSocket = "/run/mcp-gateway/control.sock"

// Notice is a notice to the approvers of an agent's calls: mcp-gateway's
// POST /v1/notices. The gateway decides its audience as for a pending
// call of the principal on those servers (the servers' approvers and the
// agent's owners) and delivers it by mail, on its event stream and its
// approval page.
type Notice struct {
	Principal string   `json:"principal"`
	Servers   []string `json:"servers,omitempty"`
	Severity  string   `json:"severity"`
	Title     string   `json:"title"`
	Text      string   `json:"text"`
}

// ErrNoNotices: the gateway takes no notices (it has no POST /v1/notices,
// or no control socket).
var ErrNoNotices = errors.New("this mcp-gateway takes no notices")

// PostNotice posts n on the gateway's control socket, as the caller (root
// for the demotion evaluator).
func PostNotice(n Notice) (string, error) { return PostNoticeTo(ControlSocket, n) }

func PostNoticeTo(sock string, n Notice) (string, error) {
	if _, err := os.Stat(sock); err != nil {
		return "", ErrNoNotices
	}
	c := client(sock)
	b, _ := json.Marshal(n)
	res, err := c.Post("http://localhost/v1/notices", "application/json", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed:
		return "", ErrNoNotices
	case res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK:
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(body))
		}
		return "", fmt.Errorf("the gateway refused the notice (%d): %s", res.StatusCode, e.Error)
	}
	var out struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &out)
	return out.ID, nil
}

// ErrNoControl: the gateway's control socket is not there (the gateway
// does not run).
var ErrNoControl = errors.New("mcp-gateway's control socket is not there")

// StopInstances stops the running instances of a server through the
// gateway's control API (GET /v1/servers, DELETE /v1/instances/{id}), as
// Cockpit's Servers tab does: the exec server reads its commands when an
// instance starts, so new commands are offered only by a new instance.
// It returns how many it stopped.
func StopInstances(server string) (int, error) { return StopInstancesOn(ControlSocket, server) }

func StopInstancesOn(sock, server string) (int, error) {
	if _, err := os.Stat(sock); err != nil {
		return 0, ErrNoControl
	}
	c := client(sock)
	res, err := c.Get("http://localhost/v1/servers")
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return 0, fmt.Errorf("the gateway's servers (%d): %s", res.StatusCode, strings.TrimSpace(string(b)))
	}
	var servers []struct {
		Name      string `json:"name"`
		Instances []struct {
			ID string `json:"id"`
		} `json:"instances"`
	}
	if err := json.NewDecoder(res.Body).Decode(&servers); err != nil {
		return 0, fmt.Errorf("the gateway's servers: %w", err)
	}
	n := 0
	var errs []string
	for _, s := range servers {
		if s.Name != server {
			continue
		}
		for _, in := range s.Instances {
			req, _ := http.NewRequest(http.MethodDelete, "http://localhost/v1/instances/"+in.ID, nil)
			r, err := c.Do(req)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			r.Body.Close()
			if r.StatusCode != http.StatusNoContent && r.StatusCode != http.StatusNotFound {
				errs = append(errs, fmt.Sprintf("instance %s: %d", in.ID, r.StatusCode))
				continue
			}
			n++
		}
	}
	if len(errs) > 0 {
		return n, errors.New(strings.Join(errs, "; "))
	}
	return n, nil
}
