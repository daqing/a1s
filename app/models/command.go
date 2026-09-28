package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
)

// Command mirrors the commands table
// (db/migrate/20260927100000_create_commands.up.sql).
type Command struct {
	ID          int64     `db:"id"`
	WorkerID    int64     `db:"worker_id"`
	ContainerID *int64    `db:"container_id"`
	Action      string    `db:"action"`
	Payload     JSONB     `db:"payload"`
	Status      string    `db:"status"`
	Result      JSONB     `db:"result"`
	Version     int64     `db:"version"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
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

// QueueStopCommand enqueues a stop command for the worker owning the
// container, unless one was already created recently or is still in flight.
// It reports whether a new command was created.
func QueueStopCommand(containerID, workerID int64) (bool, error) {
	// bound the re-issue rate: a stop that keeps failing (unkillable
	// runtime) would otherwise be re-issued on every reconcile pass
	var recent int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT count(*) FROM commands
		 WHERE container_id = $1 AND action = $2 AND created_at > now() - interval '60 seconds'`,
		containerID, CommandStop).Scan(&recent); err != nil {
		return false, err
	}

	if recent > 0 {
		return false, nil
	}

	// the worker's stop executor needs the container name in the payload
	container, err := repo.FindByID[Container](buildingsql.IdType(containerID))
	if err != nil {
		return false, err
	}

	if container == nil {
		return false, nil
	}

	payload, err := json.Marshal(map[string]string{"name": container.Name})
	if err != nil {
		return false, err
	}

	_, err = repo.CreateFrom[Command](buildingsql.H{
		"worker_id":    workerID,
		"container_id": containerID,
		"action":       CommandStop,
		"payload":      JSONB(payload),
	})

	return err == nil, err
}

// HasInFlightStart reports whether a start command for the container is
// still queued or delivered — the runtime may legitimately not exist yet,
// so a reconcile pass must not treat the container as missing.
func HasInFlightStart(containerID int64) (bool, error) {
	inFlight, err := repo.Find[Command](repo.CurrentDB(),
		buildingsql.SelectColumns("id").
			From("commands").
			Where(buildingsql.AllOf(
				buildingsql.FieldEq(buildingsql.FieldFor(Command{}, "container_id"), containerID),
				buildingsql.FieldEq(buildingsql.FieldFor(Command{}, "action"), CommandStart),
				buildingsql.In("status", []string{CommandQueued, CommandDelivered}),
			)).
			Limit(1))
	if err != nil {
		return false, err
	}

	return len(inFlight) > 0, nil
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
