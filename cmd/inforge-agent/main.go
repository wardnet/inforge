// Command inforge-agent is the systemd ExecStart for every inforge-managed
// service. It runs as root, fetches the service's secrets from the configured
// provider at start time, injects them as environment variables, drops privilege
// to the service user, and execs the real service binary — so secrets are
// delivered at runtime (rotatable, no redeploy) and never written to disk, the
// journal, or argv. It is downloaded onto each host by `inforge deploy`, pinned
// to the deploying inforge version; users never invoke it directly.
package main

import (
	"fmt"
	"os"

	"github.com/wardnet/inforge/internal/agent"
)

// version is the build version, overridden at release time via -ldflags
// "-X main.version=<tag>". It defaults to "dev" for local builds, mirroring
// cmd/inforge.
var version = "dev"

func main() {
	if err := agent.Run(os.Args, version); err != nil {
		fmt.Fprintf(os.Stderr, "inforge-agent: %v\n", err)
		os.Exit(1)
	}
}
