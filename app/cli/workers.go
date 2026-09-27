package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"
)

// listWorkers handles `a1s workers`: lists workers as a plain table.
func listWorkers(args []string) int {
	fs := flag.NewFlagSet("workers", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: a1s workers")
		return 2
	}

	var list struct {
		Workers []struct {
			ID              int64   `json:"id"`
			Name            string  `json:"name"`
			Address         string  `json:"address"`
			Status          string  `json:"status"`
			LastHeartbeatAt *string `json:"last_heartbeat_at"`
		} `json:"workers"`
	}

	client := NewClient()
	if err := client.do("GET", "/api/v1/workers", nil, &list); err != nil {
		return client.fail(err)
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tADDRESS\tSTATUS\tHEARTBEAT")
	for _, worker := range list.Workers {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", worker.ID, worker.Name, worker.Address, worker.Status, heartbeat(worker.LastHeartbeatAt))
	}

	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "a1s: write table: %v\n", err)
		return 1
	}

	return 0
}

// heartbeat renders the heartbeat timestamp compactly, or "-" when the
// worker never reported one.
func heartbeat(at *string) string {
	if at == nil {
		return "-"
	}

	parsed, err := time.Parse(time.RFC3339, *at)
	if err != nil {
		return *at
	}

	return parsed.UTC().Format("2006-01-02 15:04:05")
}
