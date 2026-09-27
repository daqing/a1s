// Package cli implements the stateless client subcommands (`run`, `ps`, ...)
// that talk to the control plane API over HTTP (see docs/api.md). Commands
// return the process exit code: 0 on success, 1 on API/server errors, 2 on
// usage errors.
package cli

import (
	"fmt"
	"os"
)

// Main dispatches a client subcommand and returns the exit code.
func Main(command string, args []string) int {
	switch command {
	case "run":
		return runContainers(args)
	case "ps":
		return psContainers(args)
	default:
		fmt.Fprintf(os.Stderr, "a1s: unknown client command %q\n", command)
		return 2
	}
}
