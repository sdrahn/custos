package relay

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// The relay sends the shared secret with every request, so it talks only
// to OpenShell's port forward, which root runs (agent-openshell@.service).
// A local user who bound the forward's port first, while no forward
// existed, would get the secret and every prompt (security review H3):
// after each connection, before anything is sent, the socket it reached
// must belong to root. The kernel's socket tables say whose it is, but not
// in the entry of the accepted connection: before the listener accepts it,
// that entry has no socket file, and older kernels (SLE 16's 6.12) list
// it with uid 0 (sock_i_uid read the file's owner). Any listener that waits before
// accept() would pass as root's (security review 0.6, P10), and the check
// failed where the listener is not root's and slow to accept (OBS's
// aarch64 builds of 0.6.1 on SLE 16). The owner is therefore the listening
// socket's, which always has its file: the entry in state LISTEN on the
// connection's address and port, or on the wildcard address and that
// port. Another listener on the same port is refused by the kernel unless
// both use SO_REUSEPORT, which it allows only to the same user. The
// connection's own entry, in a connected state (not an earlier
// connection's TIME_WAIT, listed with uid 0 too), must be there as well:
// the other end is on this host.

// ProcNetTCP are the kernel's TCP socket tables the owner is read from.
var ProcNetTCP = []string{"/proc/net/tcp", "/proc/net/tcp6"}

// ForwardUID is the owner the forward's socket must have.
var ForwardUID = 0

// transport dials the upstream and checks the socket's owner.
func transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		uid, err := peerUID(c)
		if err == nil && uid != ForwardUID {
			err = fmt.Errorf("%s is held by uid %d, not by OpenShell's forward (uid %d): not sending the agent's secret there", addr, uid, ForwardUID)
		}
		if err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	}
	return t
}

// peerUID is the owner of the listening socket at the other end of a TCP
// connection on this host.
func peerUID(c net.Conn) (int, error) {
	local, ok1 := c.LocalAddr().(*net.TCPAddr)
	remote, ok2 := c.RemoteAddr().(*net.TCPAddr)
	if !ok1 || !ok2 {
		return 0, fmt.Errorf("not a TCP connection")
	}
	// The other end's entry: its local address is our remote one; a
	// v4 connection to a dual-stack listener is listed with v4-mapped
	// addresses in tcp6.
	conn := map[string]bool{}
	exact := map[string]bool{}
	for _, f := range []func(*net.TCPAddr) string{procAddr, procAddr6} {
		conn[f(remote)+" "+f(local)] = true
		exact[f(remote)] = true
	}
	port := fmt.Sprintf(":%04X", remote.Port)
	wild := map[string]bool{"00000000" + port: true, strings.Repeat("0", 32) + port: true}
	connected, uid, wuid := false, -1, -1
	for _, p := range ProcNetTCP {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 8 {
				continue
			}
			switch {
			case connectedStates[fs[3]] && conn[fs[1]+" "+fs[2]]:
				connected = true
			case fs[3] == listening && exact[fs[1]]:
				if n, err := strconv.Atoi(fs[7]); err == nil {
					uid = n
				}
			case fs[3] == listening && wild[fs[1]]:
				if n, err := strconv.Atoi(fs[7]); err == nil {
					wuid = n
				}
			}
		}
		f.Close()
	}
	if !connected {
		return 0, fmt.Errorf("the socket at %s is not in the kernel's tables", remote)
	}
	// The kernel delivers to the listener on the address before the
	// wildcard's.
	if uid < 0 {
		uid = wuid
	}
	if uid < 0 {
		return 0, fmt.Errorf("no listener at %s in the kernel's tables", remote)
	}
	return uid, nil
}

// connectedStates are the states (the table's st, in hex) of the other
// end of a connection just made: ESTABLISHED, or SYN_RECV until the
// handshake's last ACK arrives. Not TIME_WAIT (06) or CLOSE (07), which
// are an earlier connection's.
var connectedStates = map[string]bool{"01": true, "03": true}

// listening is LISTEN's st.
const listening = "0A"

// procAddr is an address as /proc/net/tcp{,6} writes it: the IP in hex,
// 32-bit words in host byte order (little endian), and the port in hex.
func procAddr(a *net.TCPAddr) string {
	ip := a.IP.To4()
	if ip == nil {
		ip = a.IP.To16()
	}
	return procIP(ip, a.Port)
}

// procAddr6 is the address as tcp6 lists it: an IPv4 one v4-mapped.
func procAddr6(a *net.TCPAddr) string { return procIP(a.IP.To16(), a.Port) }

func procIP(ip net.IP, port int) string {
	b := make([]byte, len(ip))
	for i := 0; i+3 < len(ip); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = ip[i+3], ip[i+2], ip[i+1], ip[i]
	}
	return strings.ToUpper(hex.EncodeToString(b)) + ":" + fmt.Sprintf("%04X", port)
}
