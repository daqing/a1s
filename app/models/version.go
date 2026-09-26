package models

import (
	"errors"

	"github.com/daqing/airway/lib/repo"
	buildingsql "github.com/daqing/airway/lib/sql"
)

// ErrVersionConflict reports an optimistic-locking miss: the row is gone or
// its version moved on since it was read.
var ErrVersionConflict = errors.New("version_conflict")

// UpdateWhereVersion updates a row guarded by its version column: vals apply
// only while the row still carries the expected version, and version is
// bumped (updated_at refreshed) inside the same statement. Zero rows
// affected — row gone or stale version — yields ErrVersionConflict, so
// competing control-plane processes can never double-apply a transition.
// Every mutable table must carry id, version and updated_at columns.
func UpdateWhereVersion[T buildingsql.Table](id int64, version int64, vals buildingsql.H) (int64, error) {
	var t T

	set := make(buildingsql.H, len(vals)+2)
	for key, value := range vals {
		set[key] = value
	}
	set["version"] = buildingsql.Op(buildingsql.Column("version"), "+", 1)
	set["updated_at"] = buildingsql.Func("now")

	b := buildingsql.UpdateTable(buildingsql.TableFor(t)).Set(set).
		Where(buildingsql.AllOf(
			buildingsql.FieldEq(buildingsql.FieldFor(t, "id"), id),
			buildingsql.FieldEq(buildingsql.FieldFor(t, "version"), version),
		))

	affected, err := repo.UpdateAffected(repo.CurrentDB(), b)
	if err != nil {
		return 0, err
	}

	if affected == 0 {
		return 0, ErrVersionConflict
	}

	return affected, nil
}
