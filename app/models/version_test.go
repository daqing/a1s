package models

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
)

// versionTestRow mirrors the scratch table used by the optimistic-lock
// tests, created and dropped per run.
type versionTestRow struct {
	ID        int64     `db:"id"`
	Value     string    `db:"value"`
	Version   int64     `db:"version"`
	UpdatedAt time.Time `db:"updated_at"`
}

func (versionTestRow) TableName() string { return "version_lock_test_rows" }

// setupVersionTestDB skips the test unless A1S_TEST_DSN points at a test
// PostgreSQL, then installs the scratch table for the run.
func setupVersionTestDB(t *testing.T) {
	t.Helper()

	dsn := os.Getenv("A1S_TEST_DSN")
	if dsn == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(dsn); err != nil {
		t.Fatalf("setup test database: %v", err)
	}

	db := repo.CurrentDB().Conn()
	if _, err := db.Exec(`DROP TABLE IF EXISTS version_lock_test_rows`); err != nil {
		t.Fatalf("drop scratch table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE version_lock_test_rows (
		id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		value TEXT NOT NULL,
		version BIGINT NOT NULL DEFAULT 0,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		t.Fatalf("create scratch table: %v", err)
	}

	t.Cleanup(func() {
		if _, err := db.Exec(`DROP TABLE IF EXISTS version_lock_test_rows`); err != nil {
			t.Logf("drop scratch table: %v", err)
		}
	})
}

func TestUpdateWhereVersionMatchUpdatesAndBumps(t *testing.T) {
	setupVersionTestDB(t)

	created, err := repo.CreateFrom[versionTestRow](buildingsql.H{"value": "v1"})
	if err != nil {
		t.Fatalf("create row: %v", err)
	}

	affected, err := UpdateWhereVersion[versionTestRow](created.ID, created.Version, buildingsql.H{"value": "v2"})
	if err != nil {
		t.Fatalf("update with matching version: %v", err)
	}
	if affected != 1 {
		t.Fatalf("expected 1 affected row, got %d", affected)
	}

	row, err := repo.FindByID[versionTestRow](buildingsql.IdType(created.ID))
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}

	if row.Value != "v2" {
		t.Fatalf("expected value v2, got %q", row.Value)
	}
	if row.Version != created.Version+1 {
		t.Fatalf("expected version %d, got %d", created.Version+1, row.Version)
	}
}

func TestUpdateWhereVersionStaleConflicts(t *testing.T) {
	setupVersionTestDB(t)

	created, err := repo.CreateFrom[versionTestRow](buildingsql.H{"value": "v1"})
	if err != nil {
		t.Fatalf("create row: %v", err)
	}

	if _, err := UpdateWhereVersion[versionTestRow](created.ID, created.Version, buildingsql.H{"value": "v2"}); err != nil {
		t.Fatalf("first update: %v", err)
	}

	_, err = UpdateWhereVersion[versionTestRow](created.ID, created.Version, buildingsql.H{"value": "v3"})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected ErrVersionConflict, got %v", err)
	}

	row, err := repo.FindByID[versionTestRow](buildingsql.IdType(created.ID))
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}

	if row.Value != "v2" || row.Version != created.Version+1 {
		t.Fatalf("stale update must not touch the row, got value=%q version=%d", row.Value, row.Version)
	}
}
