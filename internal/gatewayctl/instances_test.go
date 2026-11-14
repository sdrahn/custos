package gatewayctl

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// The exec server's instances are stopped through the gateway's control
// API; another server's are left.
func TestStopInstances(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "control.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var stopped []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/servers":
			json.NewEncoder(w).Encode([]map[string]any{
				{"name": "exec", "instances": []map[string]any{{"id": "i-1"}, {"id": "i-2"}}},
				{"name": "fs", "instances": []map[string]any{{"id": "i-3"}}},
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/instances/"):
			stopped = append(stopped, strings.TrimPrefix(r.URL.Path, "/v1/instances/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(l)
	defer srv.Close()
	n, err := StopInstancesOn(sock, "exec")
	if err != nil || n != 2 || strings.Join(stopped, ",") != "i-1,i-2" {
		t.Errorf("stopped %d %v: %v", n, stopped, err)
	}
	if _, err := StopInstancesOn(filepath.Join(t.TempDir(), "none.sock"), "exec"); err != ErrNoControl {
		t.Errorf("without the gateway: %v", err)
	}
}
