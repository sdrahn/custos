// Package journal writes the agents' audit records (CONTRACT.md §9) to
// journald's native protocol, or as JSON lines when there is no journal.
package journal

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
)

// Event names and their MESSAGE_IDs (CONTRACT.md §9).
const (
	AgentStart       = "agent.start"
	AgentStop        = "agent.stop"
	ConfigInvalid    = "config.invalid"
	InvocationStart  = "invocation.start"
	InvocationEnd    = "invocation.end"
	ModelCall        = "model.call"
	ToolCall         = "tool.call"
	ToolResult       = "tool.result"
	ToolDeniedLocal  = "tool.denied.local"
	ApprovalPending  = "approval.pending"
	ApprovalResolved = "approval.resolved"
	LimitExceeded    = "limit.exceeded"
	AgentPaused      = "agent.paused"
	AgentResumed     = "agent.resumed"
	ActivityCancel   = "activity.cancel"

	// Promotion records (§6a, §10), written by agent-admin.
	CategoryPromoted = "category.promoted"
	CategoryDemoted  = "category.demoted"
	CategoryReview   = "category.review"

	// Root's records the readiness measures need (O11): a change of an
	// agent's permissions, the emergency stop and its release.
	PermissionsChanged = "permissions.changed"
	AgentsStopped      = "agents.stopped"
	AgentsReleased     = "agents.released"

	// The bundled diagnostics install (§4a, §6b, §10; draft, O16): the
	// IDs are reserved by the contract.
	DiagnosisReport   = "diagnosis.report"
	DiagnosisFeedback = "diagnosis.feedback"
	CategoriesEnabled = "categories.enabled"
	CategoryRefused   = "category.refused"
	// CategoriesDisabled: the way back from categories.enabled (#110).
	CategoriesDisabled = "categories.disabled"
	// GatewayData: agent-admin signed an agent's gateway data or the
	// agents' token issuers with the seal (§6b).
	GatewayData = "gateway.data"
	// EventSkipped: an event (§14) started no activity (damped, or the
	// agent refused).
	EventSkipped = "event.skipped"
)

var MessageIDs = map[string]string{
	AgentStart:       "6f3c0e1a9b2d4c58a7e1f2d3c4b5a601",
	AgentStop:        "6f3c0e1a9b2d4c58a7e1f2d3c4b5a602",
	ConfigInvalid:    "6f3c0e1a9b2d4c58a7e1f2d3c4b5a603",
	InvocationStart:  "6f3c0e1a9b2d4c58a7e1f2d3c4b5a610",
	InvocationEnd:    "6f3c0e1a9b2d4c58a7e1f2d3c4b5a611",
	ModelCall:        "6f3c0e1a9b2d4c58a7e1f2d3c4b5a620",
	ToolCall:         "6f3c0e1a9b2d4c58a7e1f2d3c4b5a630",
	ToolResult:       "6f3c0e1a9b2d4c58a7e1f2d3c4b5a631",
	ToolDeniedLocal:  "6f3c0e1a9b2d4c58a7e1f2d3c4b5a632",
	ApprovalPending:  "6f3c0e1a9b2d4c58a7e1f2d3c4b5a640",
	ApprovalResolved: "6f3c0e1a9b2d4c58a7e1f2d3c4b5a641",
	LimitExceeded:    "6f3c0e1a9b2d4c58a7e1f2d3c4b5a650",
	AgentPaused:      "6f3c0e1a9b2d4c58a7e1f2d3c4b5a604",
	AgentResumed:     "6f3c0e1a9b2d4c58a7e1f2d3c4b5a605",
	ActivityCancel:   "6f3c0e1a9b2d4c58a7e1f2d3c4b5a612",
	CategoryPromoted: "6f3c0e1a9b2d4c58a7e1f2d3c4b5a660",
	CategoryDemoted:  "6f3c0e1a9b2d4c58a7e1f2d3c4b5a661",
	CategoryReview:   "6f3c0e1a9b2d4c58a7e1f2d3c4b5a662",

	PermissionsChanged: "6f3c0e1a9b2d4c58a7e1f2d3c4b5a663",
	AgentsStopped:      "6f3c0e1a9b2d4c58a7e1f2d3c4b5a664",
	AgentsReleased:     "6f3c0e1a9b2d4c58a7e1f2d3c4b5a665",

	DiagnosisReport:    "6f3c0e1a9b2d4c58a7e1f2d3c4b5a670",
	DiagnosisFeedback:  "6f3c0e1a9b2d4c58a7e1f2d3c4b5a671",
	CategoriesEnabled:  "6f3c0e1a9b2d4c58a7e1f2d3c4b5a672",
	CategoryRefused:    "6f3c0e1a9b2d4c58a7e1f2d3c4b5a673",
	EventSkipped:       "6f3c0e1a9b2d4c58a7e1f2d3c4b5a674",
	CategoriesDisabled: "6f3c0e1a9b2d4c58a7e1f2d3c4b5a675",
	GatewayData:        "6f3c0e1a9b2d4c58a7e1f2d3c4b5a676",
}

// HostEvents are the events an agent host writes itself (CONTRACT.md
// §10); the others are agent-admin's, as root. The OpenShell relay
// passes only these on from the sandbox (security review H1).
var HostEvents = map[string]bool{
	AgentStart: true, AgentStop: true, ConfigInvalid: true,
	InvocationStart: true, InvocationEnd: true, ModelCall: true,
	ToolCall: true, ToolResult: true, ToolDeniedLocal: true,
	ApprovalPending: true, ApprovalResolved: true, LimitExceeded: true,
	AgentPaused: true, AgentResumed: true, ActivityCancel: true,
	DiagnosisReport: true,
}

// ValidField reports whether k is a field name a record may carry,
// without the AGENT_ prefix: upper-case letters, digits and "_", at most
// 64 (journald's field names; a newline in one would add fields).
func ValidField(k string) bool {
	if k == "" || len(k) > 64 || k[0] == '_' {
		return false
	}
	for _, c := range k {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// Fields of a record, without the AGENT_ prefix: "SESSION", "TOOL", ….
type Fields map[string]string

// Logger writes records for one agent.
type Logger interface {
	Log(event, message string, f Fields)
}

// SocketPath is journald's native socket.
const SocketPath = "/run/systemd/journal/socket"

// New returns a logger for agent name: journald if its socket is there,
// else JSON lines on w.
func New(name string, w io.Writer) Logger {
	return NewAs("agent-"+name, name, w)
}

// NewAs is New with another SYSLOG_IDENTIFIER: agent-admin's promotion
// records are custos-promotion's (§10).
func NewAs(ident, name string, w io.Writer) Logger {
	if c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: SocketPath, Net: "unixgram"}); err == nil {
		return &native{ident: ident, name: name, conn: c, fallback: &JSON{Name: name, W: w}}
	}
	return &JSON{Name: name, W: w}
}

type native struct {
	ident    string
	name     string
	mu       sync.Mutex
	conn     *net.UnixConn
	fallback *JSON
}

func (n *native) Log(event, message string, f Fields) {
	var b bytes.Buffer
	put := func(k, v string) {
		if strings.ContainsRune(v, '\n') {
			b.WriteString(k)
			b.WriteByte('\n')
			binary.Write(&b, binary.LittleEndian, uint64(len(v)))
			b.WriteString(v)
			b.WriteByte('\n')
			return
		}
		b.WriteString(k + "=" + v + "\n")
	}
	put("MESSAGE", message)
	put("MESSAGE_ID", MessageIDs[event])
	put("PRIORITY", priority(event))
	put("SYSLOG_IDENTIFIER", n.ident)
	put("AGENT_CONTRACT", "1")
	put("AGENT_NAME", n.name)
	put("AGENT_EVENT", event)
	for _, k := range sortedKeys(f) {
		// A name journald would read otherwise (a newline, "=") never
		// becomes fields of the caller's choosing.
		if f[k] != "" && ValidField(k) {
			put("AGENT_"+k, f[k])
		}
	}
	n.mu.Lock()
	_, err := n.conn.Write(b.Bytes())
	n.mu.Unlock()
	if err != nil {
		// Too large for a datagram (or journald gone): keep the record.
		n.fallback.Log(event, message, f)
	}
}

func priority(event string) string {
	switch event {
	case ConfigInvalid:
		return "3"
	case ToolDeniedLocal, LimitExceeded, ApprovalPending:
		return "5"
	}
	return "6"
}

// JSON writes records as JSON lines (lower-case keys, no AGENT_ prefix).
type JSON struct {
	Name string
	W    io.Writer
	mu   sync.Mutex
}

func (j *JSON) Log(event, message string, f Fields) {
	m := map[string]string{"message": message, "message_id": MessageIDs[event], "contract": "1", "name": j.Name, "event": event}
	for k, v := range f {
		if v != "" {
			m[strings.ToLower(k)] = v
		}
	}
	b, _ := json.Marshal(m)
	j.mu.Lock()
	defer j.mu.Unlock()
	w := j.W
	if w == nil {
		w = os.Stderr
	}
	w.Write(append(b, '\n'))
}

func sortedKeys(f Fields) []string {
	ks := make([]string, 0, len(f))
	for k := range f {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Memory keeps records, for tests.
type Memory struct {
	mu      sync.Mutex
	Records []Record
}

type Record struct {
	Event, Message string
	Fields         Fields
}

func (m *Memory) Log(event, message string, f Fields) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := Fields{}
	for k, v := range f {
		cp[k] = v
	}
	m.Records = append(m.Records, Record{event, message, cp})
}

// Events returns the records of the given event.
func (m *Memory) Events(event string) []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, r := range m.Records {
		if r.Event == event {
			out = append(out, r)
		}
	}
	return out
}
