package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/daqing/airway/lib/repo"
	"github.com/daqing/airway/lib/utils"
)

// statsMain handles `a1s stats`: a cluster summary read straight from the
// database. The database is the single source of truth, so a CLI beats a
// scrape endpoint for spare-time operations — no extra HTTP surface, and
// the numbers are exactly what the control plane itself sees.
func statsMain(args []string) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: a1s stats")
		return 2
	}

	dsn := utils.GetEnvMulti("A1S_DSN", "AIRWAY_DSN", "DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "a1s: A1S_DSN is not set; stats reads the database directly")
		return 1
	}

	if _, err := repo.SetupDB(dsn); err != nil {
		fmt.Fprintf(os.Stderr, "a1s: database setup failed: %v\n", err)
		return 1
	}

	workers, err := statusCounts("workers")
	if err != nil {
		return statsFail("worker stats", err)
	}

	containers, err := statusCounts("containers")
	if err != nil {
		return statsFail("container stats", err)
	}

	var inFlight int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT count(*) FROM commands WHERE status IN ('queued', 'delivered')`).Scan(&inFlight); err != nil {
		return statsFail("command stats", err)
	}

	var activeWorkers int64
	var heartbeatAge *float64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT count(*), EXTRACT(EPOCH FROM (now() - max(last_heartbeat_at)))
		 FROM workers WHERE status = 'active'`).Scan(&activeWorkers, &heartbeatAge); err != nil {
		return statsFail("heartbeat stats", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "WORKERS")
	printCounts(w, workers)
	fmt.Fprintf(w, "  active total\t%d\n", activeWorkers)
	if heartbeatAge != nil {
		fmt.Fprintf(w, "  newest heartbeat age\t%.0fs\n", *heartbeatAge)
	}

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "CONTAINERS")
	printCounts(w, containers)

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "COMMANDS")
	fmt.Fprintf(w, "  in flight (queued+delivered)\t%d\n", inFlight)

	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "a1s: write stats: %v\n", err)
		return 1
	}

	return 0
}

// statsFail prints a database-side stats failure and returns exit code 1.
func statsFail(stage string, err error) int {
	fmt.Fprintf(os.Stderr, "a1s: %s failed: %v\n", stage, err)
	return 1
}

// statusCounts counts rows per status for one table.
func statusCounts(table string) (map[string]int64, error) {
	rows, err := repo.CurrentDB().Conn().Query(
		fmt.Sprintf(`SELECT status, count(*) FROM %s GROUP BY status`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int64{}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}

	return counts, rows.Err()
}

func printCounts(w *tabwriter.Writer, counts map[string]int64) {
	for _, status := range []string{"pending", "scheduled", "running", "stopped", "failed", "lost"} {
		fmt.Fprintf(w, "  %s\t%d\n", status, counts[status])
	}
}
