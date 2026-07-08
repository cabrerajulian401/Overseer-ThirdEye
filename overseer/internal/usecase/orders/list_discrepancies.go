package orders

import (
	"context"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// ListDiscrepancies serves GET /discrepancies: discrepancy.found only
// reaches clients connected at the moment of the finding, so the DB is the
// source of truth for everyone who connects later.
type ListDiscrepancies struct {
	Repo DiscrepancyRepo
}

type ListDiscrepanciesInput struct {
	UserID uuid.UUID
	Status *entity.DiscrepancyStatus // nil = all
}

func (uc *ListDiscrepancies) Execute(ctx context.Context, in ListDiscrepanciesInput) ([]entity.Discrepancy, error) {
	return uc.Repo.ListByUser(ctx, in.UserID, in.Status)
}
