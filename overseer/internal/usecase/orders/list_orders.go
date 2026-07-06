package orders

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// ListOrders serves GET /orders: read-only, owner-scoped, cursor-paginated.
type ListOrders struct {
	Repo OrderRepo
}

type ListInput struct {
	UserID uuid.UUID
	Status *entity.OrderStatus
	Cursor *time.Time // created_at of the last row on the previous page
	Limit  int
}

type ListOutput struct {
	Orders     []entity.Order
	NextCursor *time.Time // nil when there are no more pages
}

func (uc *ListOrders) Execute(ctx context.Context, in ListInput) (ListOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}

	// Fetch one extra row to know whether another page exists.
	rows, err := uc.Repo.List(ctx, in.UserID, in.Status, in.Cursor, limit+1)
	if err != nil {
		return ListOutput{}, err
	}

	out := ListOutput{Orders: rows}
	if len(rows) > limit {
		out.Orders = rows[:limit]
		cursor := out.Orders[limit-1].CreatedAt
		out.NextCursor = &cursor
	}
	return out, nil
}
