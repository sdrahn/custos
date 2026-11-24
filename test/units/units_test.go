// Package units checks the shipped systemd units against what the rest of
// Custos relies on.
package units

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// settings reads a unit file's assignments, in order (key → values).
func settings(t *testing.T, path string) map[string][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string][]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			out[k] = append(out[k], v)
		}
	}
	return out
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// An agent's credentials, sealed to the TPM on a host that has one, are
// decrypted under the unit's device policy: PrivateDevices= needs the TPM
// allowed, or the unit fails at step CREDENTIALS ("Protocol error").
func TestAgentCredentialsReachTheTPM(t *testing.T) {
	u := settings(t, "../../systemd/agent@.service")
	if !has(u["ImportCredential"], "agent-%i.*") {
		t.Fatalf("ImportCredential: %v", u["ImportCredential"])
	}
	if has(u["PrivateDevices"], "yes") && !has(u["DeviceAllow"], "/dev/tpmrm0 rw") {
		t.Error("PrivateDevices=yes without DeviceAllow=/dev/tpmrm0 rw: TPM-sealed credentials cannot be decrypted")
	}
}

// agent-admin creates /etc/custos/agents/<name> as 0750; the unit says the
// same, or systemd warns at every start.
func TestAgentConfigurationDirectoryMode(t *testing.T) {
	u := settings(t, "../../systemd/agent@.service")
	if !has(u["ConfigurationDirectoryMode"], "0750") {
		t.Errorf("ConfigurationDirectoryMode: %v, want 0750 (agent-admin create)", u["ConfigurationDirectoryMode"])
	}
}

// An OpenShell agent's sandbox is agent-%i by default and the name
// openshell.env gives otherwise (config.OpenShellSandbox): every command
// of the unit takes it from OPENSHELL_SANDBOX, and the default is an
// Environment=, which the EnvironmentFile= overrides. An openshell.env
// written before it named the sandbox works unchanged.
func TestOpenShellSandboxName(t *testing.T) {
	u := settings(t, "../../systemd/agent-openshell@.service")
	if !has(u["Environment"], "OPENSHELL_SANDBOX=agent-%i") {
		t.Errorf("Environment: %v", u["Environment"])
	}
	if !has(u["EnvironmentFile"], "/etc/custos/agents/%i/openshell.env") {
		t.Errorf("EnvironmentFile: %v", u["EnvironmentFile"])
	}
	for _, k := range []string{"ExecStartPre", "ExecStart", "ExecStopPost"} {
		for _, v := range u[k] {
			if strings.Contains(v, "agent-%i") {
				t.Errorf("%s names the sandbox agent-%%i, not ${OPENSHELL_SANDBOX}: %s", k, v)
			}
		}
	}
	n := 0
	for _, k := range []string{"ExecStartPre", "ExecStart", "ExecStopPost"} {
		for _, v := range u[k] {
			n += strings.Count(v, "${OPENSHELL_SANDBOX}")
		}
	}
	if n != 6 {
		t.Errorf("${OPENSHELL_SANDBOX} in %d commands, want 6 (start, the forward stopped and started, the logs, the forward and the sandbox stopped)", n)
	}
}
