package containers_api

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/daqing/a1s/app/api/respond"
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
	"github.com/daqing/airway/lib/validation"
	"github.com/gin-gonic/gin"
)

type createRequest struct {
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	RestartPolicy string            `json:"restart_policy"`
}

var containerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// CreateAction handles POST /api/v1/containers: validates the request and
// inserts a pending container with no worker assigned (docs/api.md).
func CreateAction(c *gin.Context) {
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respond.Validation(c, err.Error())
		return
	}

	vals, err := normalizeCreateRequest(&req)
	if err != nil {
		respond.Validation(c, err.Error())
		return
	}

	row, err := repo.CreateFrom[models.Container](vals)
	if err != nil {
		// The unique constraint on name is the race-safe backstop for the
		// cheaper existence pre-check; map both paths to a conflict.
		if nameTaken(vals["name"].(string)) {
			respond.Conflict(c, fmt.Sprintf("container name %q already exists", vals["name"]))
			return
		}

		respond.Internal(c, err)
		return
	}

	c.JSON(201, toContainerJSON(row))
}

func normalizeCreateRequest(req *createRequest) (buildingsql.H, error) {
	if err := validation.Do("image", req.Image, "required"); err != nil {
		return nil, fmt.Errorf("image is required")
	}

	if req.Name != "" && !containerNamePattern.MatchString(req.Name) {
		return nil, fmt.Errorf("invalid name %q", req.Name)
	}

	restartPolicy, err := normalizeRestartPolicy(req.RestartPolicy)
	if err != nil {
		return nil, err
	}

	name := req.Name
	if name == "" {
		var err error
		name, err = generateName(req.Image)
		if err != nil {
			return nil, err
		}
	}

	vals := buildingsql.H{
		"name":           name,
		"image":          req.Image,
		"command":        req.Command,
		"status":         models.ContainerPending,
		"restart_policy": restartPolicy,
	}

	if req.Args != nil {
		vals["args"] = models.Args(req.Args)
	}
	if req.Env != nil {
		vals["env"] = models.Env(req.Env)
	}

	return vals, nil
}

// normalizeRestartPolicy accepts Docker-style policies with optional
// numeric suffixes (on-failure:3), defaulting to "no".
func normalizeRestartPolicy(policy string) (string, error) {
	if policy == "" {
		return "no", nil
	}

	base, suffix, hasSuffix := strings.Cut(policy, ":")

	switch base {
	case "no", "always", "unless-stopped":
		if hasSuffix {
			return "", fmt.Errorf("restart_policy %q takes no suffix", policy)
		}
	case "on-failure":
		if hasSuffix {
			if _, err := parseRetries(suffix); err != nil {
				return "", fmt.Errorf("invalid restart_policy %q", policy)
			}
		}
	default:
		return "", fmt.Errorf("invalid restart_policy %q", policy)
	}

	return policy, nil
}

func parseRetries(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid retry count %q", s)
	}

	return n, nil
}

// generateName builds a unique container name from the image slug plus a
// random suffix, e.g. "nginx-k3m9x2".
func generateName(image string) (string, error) {
	slug := image
	if idx := strings.LastIndexAny(slug, "/"); idx >= 0 {
		slug = slug[idx+1:]
	}
	if idx := strings.IndexAny(slug, ":@"); idx >= 0 {
		slug = slug[:idx]
	}

	var b strings.Builder
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}

	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "container"
	}

	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	suffix := make([]byte, 6)
	for i := range suffix {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("generate container name: %w", err)
		}
		suffix[i] = alphabet[n.Int64()]
	}

	return name + "-" + string(suffix), nil
}

func nameTaken(name string) bool {
	exists, err := repo.ExistsWhere[models.Container](buildingsql.H{"name": name})
	return err == nil && exists
}

// containerJSON is the wire representation of a container (docs/api.md).
type containerJSON struct {
	ID            int64       `json:"id"`
	Name          string      `json:"name"`
	Image         string      `json:"image"`
	Command       string      `json:"command"`
	Args          models.Args `json:"args"`
	Env           models.Env  `json:"env"`
	Status        string      `json:"status"`
	WorkerID      *int64      `json:"worker_id"`
	RestartPolicy string      `json:"restart_policy"`
	Version       int64       `json:"version"`
	ScheduledAt   *time.Time  `json:"scheduled_at"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

func toContainerJSON(c *models.Container) containerJSON {
	return containerJSON{
		ID:            c.ID,
		Name:          c.Name,
		Image:         c.Image,
		Command:       c.Command,
		Args:          c.Args,
		Env:           c.Env,
		Status:        c.Status,
		WorkerID:      c.WorkerID,
		RestartPolicy: c.RestartPolicy,
		Version:       c.Version,
		ScheduledAt:   utcPtr(c.ScheduledAt),
		CreatedAt:     c.CreatedAt.UTC(),
		UpdatedAt:     c.UpdatedAt.UTC(),
	}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	u := t.UTC()
	return &u
}
