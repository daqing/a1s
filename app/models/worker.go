package models

import "time"

// Worker mirrors the workers table
// (db/migrate/20260926112500_create_workers.up.sql).
type Worker struct {
	ID              int64      `db:"id"`
	Name            string     `db:"name"`
	Address         string     `db:"address"`
	Status          string     `db:"status"`
	LastHeartbeatAt *time.Time `db:"last_heartbeat_at"`
	Version         int64      `db:"version"`
	CreatedAt       time.Time  `db:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at"`
}

const (
	WorkerActive = "active"
	WorkerLost   = "lost"
)

func (Worker) TableName() string {
	return "workers"
}

func init() {
	registerREPLModel("Worker", Worker{})
}
