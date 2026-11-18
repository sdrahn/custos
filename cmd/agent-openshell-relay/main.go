// agent-openshell-relay is the host side of an agent in an OpenShell
// sandbox (CONTRACT.md §3a, §16a), run by agent-openshell@<name>.service:
// it serves /run/agents/<name>/api.sock (from its socket unit), relays
// to the agent's control API through OpenShell's port forward with the
// caller it saw by SO_PEERCRED, and writes the agent's records from
// OpenShell's log stream to the journal.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sdrahn/custos/internal/journal"
	"github.com/sdrahn/custos/internal/peercred"
	"github.com/sdrahn/custos/internal/relay"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "agent-openshell-relay:", err)
		os.Exit(1)
	}
}

func run() error {
	name := flag.String("name", "", "the agent")
	upstream := flag.String("upstream", "", "the forwarded control API, e.g. http://127.0.0.1:47123")
	secretFile := flag.String("secret-file", "", "the secret shared with the agent")
	logs := flag.String("logs", "", "command that streams the agent's output, e.g. '/usr/libexec/custos/agent-openshell-logs agent-NAME'")
	flag.Parse()
	if *name == "" || *upstream == "" || *secretFile == "" {
		return errors.New("--name, --upstream and --secret-file are needed")
	}
	u, err := url.Parse(*upstream)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return fmt.Errorf("--upstream %q: want http://<loopback>:<port>", *upstream)
	}
	b, err := os.ReadFile(*secretFile)
	if err != nil {
		return err
	}
	secret := strings.TrimSpace(string(b))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if *logs != "" {
		go bridge(ctx, *name, *logs)
	}
	ln, err := socket()
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: relay.Handler(u, secret), ConnContext: peercred.ConnContext, ReadHeaderTimeout: 30 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// socket takes the socket systemd passed (agent-openshell@.socket).
func socket() (net.Listener, error) {
	if pid, _ := strconv.Atoi(os.Getenv("LISTEN_PID")); pid != os.Getpid() || os.Getenv("LISTEN_FDS") != "1" {
		return nil, errors.New("no socket from systemd; start agent-openshell@<name>.socket")
	}
	return net.FileListener(os.NewFile(3, "api.sock"))
}

// bridge runs the log command and writes the agent's records to the
// journal, restarting the command if it ends.
func bridge(ctx context.Context, name, command string) {
	log := journal.New(name, os.Stderr)
	// The command is told where the last stream ended (AGENT_LOGS_SINCE),
	// so that a restart neither repeats records nor loses the gap.
	since := time.Now()
	for ctx.Err() == nil {
		cmd := logCommand(ctx, command, since)
		// Its complaints (no container, a runtime error) go to the unit's journal.
		cmd.Stderr = os.Stderr
		out, err := cmd.StdoutPipe()
		if err == nil && cmd.Start() == nil {
			relay.Bridge(bufio.NewReader(out), name, log)
			since = time.Now()
			cmd.Wait()
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

// logCommand is the log command, told where the last stream ended
// (AGENT_LOGS_SINCE). The relay does not signal it when it stops: the
// unit's stop ends it with the rest of the unit's processes (systemd
// signals the unit's whole control group). The relay may not: as rootless
// Podman, the command runs as the OpenShell user in that user's user
// namespace, and signalling it would need CAP_KILL there, which SELinux
// refuses the relay's domain (cap_userns kill, #221).
func logCommand(ctx context.Context, command string, since time.Time) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "AGENT_LOGS_SINCE="+since.UTC().Format(time.RFC3339Nano))
	cmd.Cancel = func() error { return nil }
	return cmd
}
