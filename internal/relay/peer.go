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
// must belong to root. The kernel's socket table says whose it is: the
// entry whose local address is the port and whose remote address is the
// relay's end of this connection.

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

// peerUID is the owner of the socket at the other end of a TCP
// connection on this host.
func peerUID(c net.Conn) (int, error) {
	local, ok1 := c.LocalAddr().(*net.TCPAddr)
	remote, ok2 := c.RemoteAddr().(*net.TCPAddr)
	if !ok1 || !ok2 {
		return 0, fmt.Errorf("not a TCP connection")
	}
	// The other end's entry: its local address is our remote one.
	want := procAddr(remote) + " " + procAddr(local)
	for _, p := range ProcNetTCP {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 8 || fs[1]+" "+fs[2] != want {
				continue
			}
			f.Close()
			return strconv.Atoi(fs[7])
		}
		f.Close()
	}
	return 0, fmt.Errorf("the socket at %s is not in the kernel's tables", remote)
}

// procAddr is an address as /proc/net/tcp{,6} writes it: the IP in hex,
// 32-bit words in host byte order (little endian), and the port in hex.
func procAddr(a *net.TCPAddr) string {
	ip := a.IP.To4()
	if ip == nil {
		ip = a.IP.To16()
	}
	b := make([]byte, len(ip))
	for i := 0; i < len(ip); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = ip[i+3], ip[i+2], ip[i+1], ip[i]
	}
	return strings.ToUpper(hex.EncodeToString(b)) + ":" + fmt.Sprintf("%04X", a.Port)
}
