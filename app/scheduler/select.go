package scheduler

import (
	"github.com/daqing/a1s/app/models"
	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
)

// selectWorker picks the active worker with the fewest containers in
// scheduled or running state (least-loaded), tie-broken by id for
// determinism. Future policies (resources, labels, spread/packing) replace
// this function's body; the signature and the "returns a ready worker or
// nil" contract stay.
func selectWorker() (*models.Worker, error) {
	loadExpr := `(SELECT count(*) FROM containers
		WHERE containers.worker_id = workers.id
		  AND containers.status IN ('scheduled', 'running'))`

	b := buildingsql.SelectColumns("id").
		From("workers").
		Where(buildingsql.FieldEq(buildingsql.FieldFor(models.Worker{}, "status"), models.WorkerActive)).
		OrderBy(loadExpr + " ASC, id ASC").
		Limit(1)

	// FindOne yields (nil, nil) when no active worker exists.
	candidate, err := repo.FindOne[workerCandidate](repo.CurrentDB(), b)
	if err != nil {
		return nil, err
	}

	if candidate == nil {
		return nil, nil
	}

	return repo.FindByID[models.Worker](buildingsql.IdType(candidate.ID))
}

// workerCandidate is the slim projection the selection query scans.
type workerCandidate struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}
