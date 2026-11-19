package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/sdrahn/custos/internal/admin"
	"github.com/sdrahn/custos/internal/admin/idp"
)

// The site's identity provider, which issues an OpenShell agent's tokens
// (CONTRACT.md §16a).
var idpCommands = []command{
	{"idp-client", func(a *admin.Admin, args []string) error {
		fs := flags("idp-client")
		gw := fs.String("gateway-config", "", "")
		keycloak := fs.Bool("keycloak", false, "")
		apply := fs.Bool("apply", false, "")
		var o idp.Apply
		fs.StringVar(&o.Admin, "admin", "", "")
		fs.StringVar(&o.AdminRealm, "admin-realm", "master", "")
		fs.StringVar(&o.URL, "keycloak-url", "", "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		a.GatewayConfig = *gw
		if !*apply {
			return idp.Client(a, pos[0], *keycloak)
		}
		if !*keycloak {
			return errors.New("idp-client: --apply is for --keycloak")
		}
		if o.Admin == "" {
			return errors.New("idp-client --apply: --admin USER, a Keycloak user that may manage the realm's clients")
		}
		pw, err := password(o.Admin)
		if err != nil {
			return err
		}
		return idp.KeycloakApply(a, pos[0], o, pw)
	}},
	{"check-token", func(a *admin.Admin, args []string) error {
		fs := flags("check-token")
		gw := fs.String("gateway-config", "", "")
		pos, err := parse(fs, args, 1)
		if err != nil {
			return err
		}
		a.GatewayConfig = *gw
		return idp.CheckToken(a, pos[0], os.Stdin)
	}},
}

// password is the Keycloak administrator's password, from standard input:
// asked for, without echo, when that is a terminal; else its first line
// (a pipe, or Cockpit).
func password(user string) (*bytes.Reader, error) {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return nil, err
	}
	tty := fi.Mode()&os.ModeCharDevice != 0
	if tty {
		stty := func(arg string) error { c := exec.Command("stty", arg); c.Stdin = os.Stdin; return c.Run() }
		// Not without echo off: the password would show (security review
		// 0.9, T5); and echo comes back on after Ctrl-C too.
		if err := stty("-echo"); err != nil {
			return nil, fmt.Errorf("the terminal's echo could not be turned off (stty: %v): give the password on standard input instead", err)
		}
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		done := make(chan struct{})
		go func() {
			select {
			case <-sig:
				stty("echo")
				fmt.Fprintln(os.Stderr)
				os.Exit(130)
			case <-done:
			}
		}()
		defer func() { signal.Stop(sig); close(done); stty("echo"); fmt.Fprintln(os.Stderr) }()
		fmt.Fprintf(os.Stderr, "Keycloak password of %s: ", user)
	}
	var line []byte
	b := make([]byte, 1)
	for len(line) < 4096 {
		n, err := os.Stdin.Read(b)
		if n == 0 || err != nil || b[0] == '\n' {
			break
		}
		line = append(line, b[0])
	}
	return bytes.NewReader(line), nil
}
