package monitor

import (
	"fmt"
	"sync"
	"testing"

	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
)

func TestShouldRestart(t *testing.T) {
	cases := []struct {
		policy string
		count  int64
		want   bool
	}{
		{"no", 0, false},
		{"no", 5, false},
		{"on-failure", 0, true},
		{"on-failure", 99, true},
		{"on-failure:3", 0, true},
		{"on-failure:3", 2, true},
		{"on-failure:3", 3, false},
		{"on-failure:0", 0, false},
		{"always", 0, true},
		{"always", 10, true},
		{"unless-stopped", 4, true},
	}

	for _, tc := range cases {
		if got := shouldRestart(tc.policy, tc.count); got != tc.want {
			t.Fatalf("shouldRestart(%q, %d) = %v, want %v", tc.policy, tc.count, got, tc.want)
		}
	}
}

// seedFailedContainer inserts a failed container with the given policy and
// restart count.
func seedFailedContainer(t *testing.T, name, policy string, count int64) int64 {
	t.Helper()

	var id int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`INSERT INTO containers (name, image, status, restart_policy, restart_count)
		 VALUES ($1, 'nginx', 'failed', $2, $3) RETURNING id`,
		name, policy, count).Scan(&id); err != nil {
		t.Fatalf("seed failed container: %v", err)
	}

	t.Cleanup(func() {
		repo.CurrentDB().Conn().Exec(`DELETE FROM containers WHERE id = $1`, id)
	})

	return id
}

// containerRestart reads status, restart policy and count of one row.
func containerRestart(t *testing.T, id int64) (string, int64) {
	t.Helper()

	var status string
	var count int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT status, restart_count FROM containers WHERE id = $1`, id).Scan(&status, &count); err != nil {
		t.Fatalf("query container: %v", err)
	}

	return status, count
}

func TestRestartFailedContainersPerPolicy(t *testing.T) {
	setupMonitorDB(t)

	suffix := fmt.Sprintf("%d", timeNowUnix())

	policies := map[string]bool{
		"no":             false,
		"on-failure":     true,
		"always":         true,
		"unless-stopped": true,
	}

	ids := map[string]int64{}
	for policy := range policies {
		ids[policy] = seedFailedContainer(t, "t54-"+policy+"-"+suffix, policy, 0)
	}

	exhausted := seedFailedContainer(t, "t54-exhausted-"+suffix, "on-failure:2", 2)

	restartFailedContainers()

	for policy, wantRestart := range policies {
		status, count := containerRestart(t, ids[policy])
		if wantRestart {
			if status != models.ContainerPending || count != 1 {
				t.Fatalf("expected %s container requeued (pending, count 1), got %q count %d", policy, status, count)
			}
			continue
		}

		if status != models.ContainerFailed {
			t.Fatalf("expected the %s container to stay failed, got %q", policy, status)
		}
	}

	status, count := containerRestart(t, exhausted)
	if status != models.ContainerFailed || count != 2 {
		t.Fatalf("expected the exhausted container to stay failed at count 2, got %q count %d", status, count)
	}
}

func TestRestartConcurrentSingleRequeue(t *testing.T) {
	setupMonitorDB(t)

	suffix := fmt.Sprintf("%d", timeNowUnix())
	id := seedFailedContainer(t, "t54-race-"+suffix, "on-failure", 0)

	var versionBefore int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT version FROM containers WHERE id = $1`, id).Scan(&versionBefore); err != nil {
		t.Fatalf("query version: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			restartFailedContainers()
		}()
	}
	wg.Wait()

	status, count := containerRestart(t, id)
	if status != models.ContainerPending {
		t.Fatalf("expected the container pending, got %q", status)
	}
	if count != 1 {
		t.Fatalf("expected restart_count 1 after a single requeue, got %d", count)
	}

	var versionAfter int64
	if err := repo.CurrentDB().Conn().QueryRow(
		`SELECT version FROM containers WHERE id = $1`, id).Scan(&versionAfter); err != nil {
		t.Fatalf("query version: %v", err)
	}
	if versionAfter != versionBefore+1 {
		t.Fatalf("expected exactly one requeue (version %d), got %d", versionBefore+1, versionAfter)
	}
}
