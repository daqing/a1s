package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/daqing/a1s/app/models"
)

// command is one queued work item as delivered by the API.
type command struct {
	ID          int64           `json:"id"`
	Action      string          `json:"action"`
	ContainerID *int64          `json:"container_id"`
	Payload     json.RawMessage `json:"payload"`
}

// commandResult is what the executor reports back for a command.
type commandResult struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// fetchCommands gets the worker's queued commands, marking them delivered
// API-side.
func (c *apiClient) fetchCommands(ctx context.Context, workerID int64) ([]command, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/internal/workers/%d/commands", c.baseURL, workerID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, apiErrorFromBody(resp.StatusCode, payload)
	}

	var out struct {
		Commands []command `json:"commands"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}

	return out.Commands, nil
}

// reportStatus puts one observed container status; the API applies it
// under the version lock when the transition is legal.
func (c *apiClient) reportStatus(ctx context.Context, containerID int64, status string) error {
	body, err := json.Marshal(map[string]string{"status": status})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		fmt.Sprintf("%s/api/v1/internal/containers/%d/status", c.baseURL, containerID), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return apiErrorFromBody(resp.StatusCode, payload)
	}

	return nil
}

// reportResult posts one command result; the API marks the command done.
func (c *apiClient) reportResult(ctx context.Context, commandID int64, result commandResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/v1/internal/commands/%d/result", c.baseURL, commandID), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return apiErrorFromBody(resp.StatusCode, payload)
	}

	return nil
}

func apiErrorFromBody(status int, payload []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &envelope)

	if envelope.Error.Message != "" {
		return fmt.Errorf("api %d: %s", status, envelope.Error.Message)
	}

	return fmt.Errorf("api %d", status)
}

// pollOnce fetches and executes the worker's queued commands, reporting
// each result. Failures are logged and survived; the next poll retries.
func pollOnce(ctx context.Context, client *apiClient, rt *containerRuntime, workerID int64) {
	cmds, err := client.fetchCommands(ctx, workerID)
	if err != nil {
		log.Printf("poll commands failed: %v", err)
		return
	}

	for _, cmd := range cmds {
		result := rt.execute(ctx, cmd)
		if err := client.reportResult(ctx, cmd.ID, result); err != nil {
			log.Printf("report result for command %d failed: %v", cmd.ID, err)
			continue
		}

		log.Printf("command %d (%s) reported done", cmd.ID, cmd.Action)

		// a failed start leaves no containerd container behind, so the
		// status report loop would never see it; report the failure
		// directly so the row does not stay scheduled forever
		if cmd.Action == "start" && !result.OK && cmd.ContainerID != nil {
			if err := client.reportStatus(ctx, *cmd.ContainerID, models.ContainerFailed); err != nil {
				log.Printf("report failed status for container %d: %v", *cmd.ContainerID, err)
			}
		}
	}
}
