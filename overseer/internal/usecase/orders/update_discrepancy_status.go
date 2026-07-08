package orders

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// ErrDiscrepancyNotFound reports an id that doesn't exist or belongs to
// another user — the controller maps it to 404.
var ErrDiscrepancyNotFound = errors.New("discrepancy not found")

// UpdateDiscrepancyStatus serves PATCH /discrepancies/{discrepancyId}: the
// UI clears findings by resolving or dismissing them (reopening is allowed).
type UpdateDiscrepancyStatus struct {
	Repo DiscrepancyRepo
}

func (uc *UpdateDiscrepancyStatus) Execute(ctx context.Context, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, error) {
	if !entity.ValidDiscrepancyStatus(s) {
		return entity.Discrepancy{}, fmt.Errorf("invalid status %q", s)
	}
	d, found, err := uc.Repo.UpdateStatus(ctx, userID, id, s)
	if err != nil {
		return entity.Discrepancy{}, err
	}
	if !found {
		return entity.Discrepancy{}, ErrDiscrepancyNotFound
	}
	return d, nil
}
