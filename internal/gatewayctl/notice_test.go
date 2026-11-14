package gatewayctl

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PostNoticeTo: the gateway's answers.
func TestPostNotice(t *testing.T) {
	dir, err := os.MkdirTemp("", "ctl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "control.sock")
	var got Notice
	status := http.StatusCreated
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/notices" {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		if status == http.StatusCreated {
			w.Write([]byte(`{"id":"n-7","title":"x"}`))
		} else {
			w.Write([]byte(`{"error":"not allowed to post notices"}`))
		}
	}))
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	n := Notice{Principal: "agent-pp", Servers: []string{"zypp"}, Severity: "warning", Title: "t", Text: "x"}
	if id, err := PostNoticeTo(sock, n); err != nil || id != "n-7" || got.Principal != "agent-pp" || got.Servers[0] != "zypp" {
		t.Errorf("%q %v %+v", id, err, got)
	}
	status = http.StatusForbidden
	if _, err := PostNoticeTo(sock, n); err == nil || !strings.Contains(err.Error(), "(403): not allowed to post notices") {
		t.Errorf("403: %v", err)
	}
	status = http.StatusNotFound
	if _, err := PostNoticeTo(sock, n); !errors.Is(err, ErrNoNotices) {
		t.Errorf("404: %v", err)
	}
	if _, err := PostNoticeTo(filepath.Join(dir, "none.sock"), n); !errors.Is(err, ErrNoNotices) {
		t.Errorf("no socket: %v", err)
	}
}
