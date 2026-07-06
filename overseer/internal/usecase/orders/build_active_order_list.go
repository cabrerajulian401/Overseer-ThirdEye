package orders

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// BuildActiveOrderList is the one-time onboarding sync (POST /sync):
// Graph ListMessages → FormatInitialOrderData (LLM filter + extract) →
// upsert into orders → push sync.completed on the realtime channel.
//
// It runs as an async job — the controller returns 202 {syncId} and invokes
// Execute in the background.
type BuildActiveOrderList struct {
	Graph  MessageLister
	Format *FormatInitialOrderData
	Repo   OrderRepo
	Events EventPublisher
}

type BuildInput struct {
	SyncID     uuid.UUID
	AccountID  uuid.UUID
	UserID     uuid.UUID
	GraphToken string
	Since      time.Time
}

func (uc *BuildActiveOrderList) Execute(ctx context.Context, in BuildInput) ([]entity.Order, error) {
	msgs, err := uc.Graph.ListMessages(ctx, in.GraphToken, in.Since)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}

	// All LLM work happens before any write, so an LLM failure leaves the
	// orders table untouched (no partial seed).
	orders, err := uc.Format.Execute(ctx, msgs)
	if err != nil {
		return nil, err
	}

	for i := range orders {
		orders[i].AccountID = in.AccountID
		orders[i].UserID = in.UserID
		if err := uc.Repo.Upsert(ctx, orders[i]); err != nil {
			return nil, fmt.Errorf("upsert order (conversation %s): %w", orders[i].ConversationID, err)
		}
	}

	uc.Events.Publish(entity.Event{
		Type: entity.EventSyncCompleted,
		Data: entity.SyncCompleted{SyncID: in.SyncID, OrdersUpserted: len(orders)},
	})
	return orders, nil
}
