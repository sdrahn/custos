// custos-setup: the bundled diagnostics install's setup, for the
// installer (CONTRACT.md §17a). Run as root after the packages are
// installed; non-interactive and idempotent. One line per step on
// standard output; exit status 0 when it is set up, 1 when not, 2 for
// wrong arguments.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sdrahn/custos/internal/admin"
)

const usage = `usage: custos-setup --diagnostics-only --backend on-host|local-network|customer-cloud|vendor
                    --model NAME [--provider openai|vertex|gemini] [--endpoint URL] [--credential-file FILE]
                    [--restarts]

The bundled health-check and diagnostics install (CONTRACT.md §17a): makes
this freshly installed host diagnostics-only, sets the model defaults,
creates the agent health-check from its template, stores its model key
(read from FILE, never from the command line), and installs the
playbooks' commands for mcp-gateway's exec server. With --restarts it
also offers mcp-gateway's server restarts (package
mcp-gateway-restarts-server), which holds CAP_SYS_PTRACE, for the
services update-status finds waiting for a restart. Without
--diagnostics-only it does nothing: the full install needs no setup.

The backends, in the documented order:
  on-host         a model server on this host (provider openai; --endpoint, default ` + admin.DefaultOnHostEndpoint + `)
  local-network   a model server on the local network (provider openai, --endpoint)
  customer-cloud  your own cloud account (--provider vertex, or openai with --endpoint)
  vendor          a vendor-hosted service (--provider gemini by default, or openai with --endpoint; --credential-file)
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("custos-setup", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var o admin.SetupOptions
	var credFile string
	fs.BoolVar(&o.DiagnosticsOnly, "diagnostics-only", false, "")
	fs.StringVar(&o.Backend, "backend", "", "")
	fs.StringVar(&o.Provider, "provider", "", "")
	fs.StringVar(&o.Endpoint, "endpoint", "", "")
	fs.StringVar(&o.Model, "model", "", "")
	fs.StringVar(&credFile, "credential-file", "", "")
	fs.BoolVar(&o.Restarts, "restarts", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "custos-setup: unexpected %q\n%s", strings.Join(fs.Args(), " "), usage)
		return 2
	}
	if o.DiagnosticsOnly && o.Model == "" {
		fmt.Fprintf(os.Stderr, "custos-setup: --model is needed\n%s", usage)
		return 2
	}
	if credFile != "" {
		b, err := os.ReadFile(credFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "custos-setup:", err)
			return 2
		}
		o.Credential = b
	}
	a := admin.New()
	a.Out = os.Stderr // agent-admin's own messages; the steps go to stdout
	a.By = "custos-setup"
	steps, err := a.Setup(o)
	for _, s := range steps {
		fmt.Println("custos-setup:", s)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "custos-setup:", err)
		return 1
	}
	for _, s := range steps {
		if s.Result == "failed" {
			return 1
		}
	}
	return 0
}
