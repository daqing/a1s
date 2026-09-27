package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Command mirrors the commands table
// (db/migrate/20260927100000_create_commands.up.sql).
type Command struct {
	ID          int64      `db:"id"`
	WorkerID    int64      `db:"worker_id"`
	ContainerID *int64     `db:"container_id"`
	Action      string     `db:"action"`
	Payload     JSONB      `db:"payload"`
	Status      string     `db:"status"`
	Result      JSONB      `db:"result"`
	Version     int64      `db:"version"`
	CreatedAt   time.Time  `db:"created_at"`
	UpdatedAt   time.Time  `db:"updated_at"`
}

const (
	CommandQueued    = "queued"
	CommandDelivered = "delivered"
	CommandDone      = "done"

	CommandStart   = "start"
	CommandStop    = "stop"
	CommandRemove  = "remove"
	CommandInspect = "inspect"
)

func (Command) TableName() string {
	return "commands"
}

func init() {
	registerREPLModel("Command", Command{})
}

// JSONB wraps raw JSON for jsonb columns: it stores verbatim and scans back
// without re-marshaling.
type JSONB json.RawMessage

func (j JSONB) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "{}", nil
	}

	return string(j), nil
}

func (j *JSONB) Scan(src any) error {
	if src == nil {
		*j = nil
		return nil
	}

	switch v := src.(type) {
	case []byte:
		*j = append((*j)[:0], v...)
	case string:
		*j = []byte(v)
	default:
		return fmt.Errorf("unsupported jsonb source type %T", src)
	}

	return nil
}

func (j JSONB) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("{}"), nil
	}

	return j, nil
}

func (j *JSONB) UnmarshalJSON(data []byte) error {
	*j = append((*j)[:0], data...)
	return nil
}
