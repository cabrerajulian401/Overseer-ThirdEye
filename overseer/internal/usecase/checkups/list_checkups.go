package checkups

import (
	"context"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// ListCheckups serves GET /checkups: the add-in repopulates its checkup list
// from the DB on refresh (spec: "immediately populate them on refresh").
type ListCheckups struct {
	Repo CheckupRepo
}

type ListInput struct {
	UserID uuid.UUID
	Status *entity.CheckupStatus // nil = all
}

func (uc *ListCheckups) Execute(ctx context.Context, in ListInput) ([]entity.CheckupMessage, error) {
	return uc.Repo.ListByUser(ctx, in.UserID, in.Status)
}
