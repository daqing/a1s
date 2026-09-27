package worker

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// defaultInterval matches A1S_HEARTBEAT_INTERVAL in .env.example.
const defaultInterval = 5 * time.Second

// defaultPollInterval matches A1S_COMMAND_INTERVAL in .env.example.
const defaultPollInterval = 2 * time.Second

// Main runs the worker agent process: it registers via a first heartbeat,
// then keeps beating on the interval until the process is signaled. It
// returns the process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	name := fs.String("name", "", "unique worker name")

	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: a1s worker --name <name>")
		return 2
	}

	token := os.Getenv("A1S_INTERNAL_TOKEN")
	if token == "" {
		log.Println("A1S_INTERNAL_TOKEN is not set; the API rejects every internal request without it")
		return 1
	}

	interval := defaultInterval
	if raw := os.Getenv("A1S_HEARTBEAT_INTERVAL"); raw != "" {
		parsed, ok := parseInterval(raw)
		if !ok {
			log.Printf("invalid A1S_HEARTBEAT_INTERVAL %q, using %s", raw, defaultInterval)
		} else {
			interval = parsed
		}
	}

	pollInterval := defaultPollInterval
	if raw := os.Getenv("A1S_COMMAND_INTERVAL"); raw != "" {
		parsed, ok := parseInterval(raw)
		if !ok {
			log.Printf("invalid A1S_COMMAND_INTERVAL %q, using %s", raw, defaultPollInterval)
		} else {
			pollInterval = parsed
		}
	}

	apiURL := os.Getenv("A1S_API_URL")
	if apiURL == "" {
		apiURL = "http://127.0.0.1:1905"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &apiClient{baseURL: strings.TrimRight(apiURL, "/"), token: token}

	log.Printf("worker %s: api=%s heartbeat interval=%s command poll=%s", *name, apiURL, interval, pollInterval)

	runLoop(ctx, client, *name, interval, pollInterval)

	log.Printf("worker %s shutting down", *name)
	return 0
}

// runLoop registers with a first heartbeat, then keeps beating on the
// heartbeat interval and polling for commands on the poll interval until
// ctx is canceled. Transient API failures are logged and survived.
func runLoop(ctx context.Context, client *apiClient, name string, heartbeatInterval, pollInterval time.Duration) {
	var workerID int64

	beat := func() {
		id, status, err := client.heartbeat(ctx, name)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			log.Printf("heartbeat failed: %v", err)
		default:
			workerID = id
			log.Printf("heartbeat ok (id %d, %s)", id, status)
		}
	}

	beat() // the first beat is the registration

	poll := func() {
		if workerID == 0 {
			return // not registered yet
		}

		pollOnce(ctx, client, workerID)
	}

	poll() // in case commands are already queued from a previous run

	beatTicker := time.NewTicker(heartbeatInterval)
	defer beatTicker.Stop()

	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-beatTicker.C:
			beat()
		case <-pollTicker.C:
			poll()
		}
	}
}

// apiClient is the worker's minimal internal-API client.
type apiClient struct {
	baseURL string
	token   string
	http    http.Client
}

// heartbeat posts one beat; transient failures surface as errors for the
// loop to log and survive.
func (c *apiClient) heartbeat(ctx context.Context, name string) (int64, string, error) {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return 0, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/internal/heartbeat", strings.NewReader(string(body)))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, "", err
	}

	if resp.StatusCode != http.StatusOK {
		return 0, "", apiErrorFromBody(resp.StatusCode, payload)
	}

	var out struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return 0, "", err
	}

	return out.ID, out.Status, nil
}

// parseInterval validates the A1S_HEARTBEAT_INTERVAL value.
func parseInterval(raw string) (time.Duration, bool) {
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0, false
	}

	return parsed, true
}
