package scheduler

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/daqing/airway/lib/repo"
)

// TestObserveFindsPendingContainers seeds a pending container and asserts
// the observation sees it; it skips unless A1S_TEST_DSN is set.
func TestObserveFindsPendingContainers(t *testing.T) {
	dsn := os.Getenv("A1S_TEST_DSN")
	if dsn == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(dsn); err != nil {
		t.Fatalf("setup test database: %v", err)
	}

	name := fmt.Sprintf("t41-%d", time.Now().UnixNano())
	var containerID int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO containers (name, image, status) VALUES ($1, 'nginx', 'pending') RETURNING id`,
		name).Scan(&containerID); err != nil {
		t.Fatalf("seed container: %v", err)
	}

	t.Cleanup(func() {
		repo.CurrentDB().Conn().Exec("DELETE FROM containers WHERE id = $1", containerID)
	})

	pending, err := observe()
	if err != nil {
		t.Fatalf("observe: %v", err)
	}

	found := false
	for _, c := range pending {
		if c.ID == containerID {
			found = true
		}
	}

	if !found {
		t.Fatalf("expected the seeded pending container among candidates, got %d candidates", len(pending))
	}
}

// TestObserveToleratesEmptyState runs the observation against an empty
// schedule (no pending containers): it must not error.
func TestObserveToleratesEmptyState(t *testing.T) {
	dsn := os.Getenv("A1S_TEST_DSN")
	if dsn == "" {
		t.Skip("A1S_TEST_DSN not set; skipping database-backed test")
	}

	if _, err := repo.SetupDB(dsn); err != nil {
		t.Fatalf("setup test database: %v", err)
	}

	// pending containers may exist from other tests; the observation must
	// simply succeed
	if _, err := observe(); err != nil {
		t.Fatalf("observe: %v", err)
	}
}
