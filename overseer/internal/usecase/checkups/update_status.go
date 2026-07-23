package checkups

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// ErrCheckupNotFound reports an id that doesn't exist or belongs to another
// user — the controller maps it to 404.
var ErrCheckupNotFound = errors.New("checkup not found")

// ErrIllegalTransition reports a PATCH against a terminal or mid-send row —
// the controller maps it to 409.
var ErrIllegalTransition = errors.New("illegal checkup status transition")

// UpdateStatus serves PATCH /checkups/{checkupId}: this one endpoint covers
// pause, resume, and cancel. sent/cancelled are terminal; sending is the
// scheduler's.
type UpdateStatus struct {
	Repo CheckupRepo
}

func (uc *UpdateStatus) Execute(ctx context.Context, userID, id uuid.UUID, to entity.CheckupStatus) (entity.CheckupMessage, error) {
	current, found, err := uc.Repo.GetByID(ctx, userID, id)
	if err != nil {
		return entity.CheckupMessage{}, fmt.Errorf("get checkup: %w", err)
	}
	if !found {
		return entity.CheckupMessage{}, ErrCheckupNotFound
	}
	if !entity.CanTransitionCheckup(current.Status, to) {
		return entity.CheckupMessage{}, fmt.Errorf("%w: %s → %s", ErrIllegalTransition, current.Status, to)
	}

	updated, found, err := uc.Repo.UpdateStatus(ctx, userID, id, to)
	if err != nil {
		return entity.CheckupMessage{}, fmt.Errorf("update checkup status: %w", err)
	}
	if !found {
		return entity.CheckupMessage{}, ErrCheckupNotFound
	}
	return updated, nil
}
