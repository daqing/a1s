package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
)

const rmUsage = "usage: a1s rm <id>"

// removeContainer handles `a1s rm <id>`: deletes the container row.
func removeContainer(args []string) int {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, rmUsage)
		return 2
	}

	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil || id <= 0 {
		fmt.Fprintln(os.Stderr, rmUsage)
		return 2
	}

	client := NewClient()
	if err := client.do("DELETE", fmt.Sprintf("/api/v1/containers/%d", id), nil, nil); err != nil {
		return client.fail(err)
	}

	fmt.Printf("removed %d\n", id)
	return 0
}
