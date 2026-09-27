package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
)

// psContainers handles `a1s ps`: lists containers as a plain table.
func psContainers(args []string) int {
	fs := flag.NewFlagSet("ps", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: a1s ps")
		return 2
	}

	var list struct {
		Containers []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Image    string `json:"image"`
			Status   string `json:"status"`
			WorkerID *int64 `json:"worker_id"`
		} `json:"containers"`
	}
	client := NewClient()
	if err := client.do("GET", "/api/v1/containers", nil, &list); err != nil {
		return client.fail(err)
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tIMAGE\tSTATUS\tWORKER")
	for _, c := range list.Containers {
		worker := "-"
		if c.WorkerID != nil {
			worker = fmt.Sprintf("%d", *c.WorkerID)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", c.ID, c.Name, c.Image, c.Status, worker)
	}

	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "a1s: write table: %v\n", err)
		return 1
	}

	return 0
}
