// Package chat is agent-admin chat: a conversation with an agent on the
// terminal, over the agent's AG-UI endpoint on its socket (CONTRACT.md
// §11), as Cockpit's chat has it. Each line typed is a message of one
// thread; the agent keeps the thread's history, so a message carries only
// itself. The answer streams back as it comes: the agent's text, its tool
// calls, the gateway's approvals it waits for, and why a run failed.
package chat

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/host"
)

// Options of a chat.
type Options struct {
	Name string
	// Thread continues one of the caller's threads (the agent keeps them
	// per caller, CONTRACT.md §11); empty: a new one.
	Thread string
	// Message, when set, is the only message: it is sent, its answer
	// printed, and the chat ends.
	Message string
}

// Run chats with agent o.Name: one message per line of a.In until its
// end, or o.Message alone.
func Run(a *admin.Admin, o Options) error {
	if err := host.CheckName(o.Name); err != nil {
		return err
	}
	sock := a.Path(filepath.Join(admin.SocketDir, o.Name, "api.sock"))
	if _, err := os.Stat(sock); err != nil {
		return fmt.Errorf("agent %s: no socket %s: is it there, and started? (agent-admin list)", o.Name, sock)
	}
	c := &client{out: a.Out, sock: sock, thread: o.Thread}
	if c.thread == "" {
		c.thread = "t-cli-" + id()
	}
	c.http = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	if o.Message != "" {
		return c.send(o.Message)
	}
	interactive := terminal(a.In)
	if interactive {
		fmt.Fprintf(a.Out, "Chat with %s, thread %s. An empty line is skipped; end with Ctrl-D.\n", o.Name, c.thread)
	}
	in := bufio.NewScanner(a.In)
	in.Buffer(make([]byte, 64*1024), 1024*1024)
	var failed error
	for {
		if interactive {
			fmt.Fprint(a.Out, "> ")
		}
		if !in.Scan() {
			break
		}
		text := strings.TrimSpace(in.Text())
		if text == "" {
			continue
		}
		if err := c.send(text); err != nil {
			// The chat goes on; the exit status says that a message failed.
			fmt.Fprintln(a.Out, "error:", err)
			failed = err
		}
	}
	if interactive {
		fmt.Fprintln(a.Out)
	}
	if err := in.Err(); err != nil {
		return err
	}
	if failed != nil {
		return errors.New("a message was not answered")
	}
	return nil
}

type client struct {
	out    io.Writer
	sock   string
	thread string
	http   *http.Client
	// midLine: the agent's text left the cursor inside a line.
	midLine bool
}

// send posts one message of the thread and prints its answer as it
// streams. A run the agent ends with RUN_ERROR is an error.
func (c *client) send(text string) error {
	body, _ := json.Marshal(map[string]any{
		"threadId": c.thread, "runId": "r-" + id(), "state": map[string]any{}, "tools": []any{},
		"context": []any{}, "forwardedProps": map[string]any{},
		"messages": []any{map[string]any{"id": "m-" + id(), "role": "user", "content": text}},
	})
	req, _ := http.NewRequest(http.MethodPost, "http://agent/agui", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("the agent did not answer: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusConflict:
		return errors.New("the agent is still working on the previous message of this thread (its answer is in the agent's activities): send again when it has finished")
	case resp.StatusCode != http.StatusOK:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("the agent answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	runErr, err := c.events(resp.Body)
	c.endLine()
	if err != nil {
		return fmt.Errorf("the answer broke off: %w (the run goes on: see the agent's activities)", err)
	}
	return runErr
}

// events prints the stream's events until its end: server-sent events,
// one JSON object per "data:" line.
func (c *client) events(r io.Reader) (runErr error, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	tools := map[string]string{} // toolCallId -> name
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var e map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &e) != nil {
			continue
		}
		switch str(e, "type") {
		case "TEXT_MESSAGE_CONTENT":
			d := str(e, "delta")
			fmt.Fprint(c.out, d)
			if d != "" {
				c.midLine = !strings.HasSuffix(d, "\n")
			}
		case "TEXT_MESSAGE_END":
			c.endLine()
		case "TOOL_CALL_START":
			tools[str(e, "toolCallId")] = str(e, "toolCallName")
			c.note("[tool] %s", str(e, "toolCallName"))
		case "TOOL_CALL_RESULT":
			if name := tools[str(e, "toolCallId")]; name != "" {
				c.note("[tool] %s: done", name)
			}
		case "CUSTOM":
			v, _ := e["value"].(map[string]any)
			switch str(e, "name") {
			case "agent.approval.pending":
				line := fmt.Sprintf("[approval] waiting for approval %s of %s", str(v, "approval_id"), str(v, "tool"))
				if u := str(v, "approval_url"); u != "" {
					line += ": " + u
				}
				c.note("%s", line)
			case "agent.approval.resolved":
				c.note("[approval] %s of %s: %s", str(v, "approval_id"), str(v, "tool"), str(v, "outcome"))
			case "agent.limit.exceeded":
				c.note("[limit] stopped at the limit %s (%v)", str(v, "limit"), v["value"])
			}
		case "RUN_ERROR":
			msg := str(e, "message")
			if msg == "" {
				msg = "the run failed"
			}
			runErr = errors.New(msg)
		}
	}
	return runErr, sc.Err()
}

// note prints a line of its own about the run, not the agent's text.
func (c *client) note(format string, args ...any) {
	c.endLine()
	fmt.Fprintf(c.out, "  "+format+"\n", args...)
}

func (c *client) endLine() {
	if c.midLine {
		fmt.Fprintln(c.out)
		c.midLine = false
	}
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func id() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// terminal: input typed by a person, who gets a prompt and a greeting.
func terminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
