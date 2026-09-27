package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
)

const stopUsage = "usage: a1s stop <id>"

// stopContainer handles `a1s stop <id>`: the desired-state transition to
// stopped (docs/api.md).
func stopContainer(args []string) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, stopUsage)
		return 2
	}

	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintln(os.Stderr, stopUsage)
		return 2
	}

	var stopped struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}

	client := NewClient()
	if err := client.do("POST", fmt.Sprintf("/api/v1/containers/%d/stop", id), nil, &stopped); err != nil {
		return client.fail(err)
	}

	fmt.Printf("stopped %s (id %d, %s)\n", stopped.Name, id, stopped.Status)
	return 0
}
