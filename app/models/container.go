package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Container mirrors the containers table
// (db/migrate/20260926113000_create_containers.up.sql).
type Container struct {
	ID            int64      `db:"id"`
	Name          string     `db:"name"`
	Image         string     `db:"image"`
	Command       string     `db:"command"`
	Args          Args       `db:"args"`
	Env           Env        `db:"env"`
	Status        string     `db:"status"`
	WorkerID      *int64     `db:"worker_id"`
	RestartPolicy string     `db:"restart_policy"`
	Version       int64      `db:"version"`
	ScheduledAt   *time.Time `db:"scheduled_at"`
	CreatedAt     time.Time  `db:"created_at"`
	UpdatedAt     time.Time  `db:"updated_at"`
}

const (
	ContainerPending   = "pending"
	ContainerScheduled = "scheduled"
	ContainerRunning   = "running"
	ContainerStopped   = "stopped"
	ContainerFailed    = "failed"
	ContainerLost      = "lost"
)

func (Container) TableName() string {
	return "containers"
}

func init() {
	registerREPLModel("Container", Container{})
}

// Env is the container environment map, persisted as JSONB.
type Env map[string]string

func (e Env) Value() (driver.Value, error) {
	if e == nil {
		return "{}", nil
	}

	return marshalJSONB(map[string]string(e))
}

func (e *Env) Scan(src any) error {
	return unmarshalJSONB(src, e)
}

// Args is the container command argument list, persisted as JSONB.
type Args []string

func (a Args) Value() (driver.Value, error) {
	if a == nil {
		return "[]", nil
	}

	return marshalJSONB([]string(a))
}

func (a *Args) Scan(src any) error {
	return unmarshalJSONB(src, a)
}

func marshalJSONB(v any) (driver.Value, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	return string(b), nil
}

func unmarshalJSONB(src any, dst any) error {
	if src == nil {
		return nil
	}

	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("unsupported jsonb source type %T", src)
	}

	return json.Unmarshal(b, dst)
}
