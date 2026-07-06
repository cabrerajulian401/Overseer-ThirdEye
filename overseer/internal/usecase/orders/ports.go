// Package orders holds the Order List usecases. Ports live where they are
// consumed: this file declares the small interfaces the usecases need;
// gateways/repos satisfy them structurally and never import this package.
package orders

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// MessageLister is the slice of gateway/graph the sync needs.
type MessageLister interface {
	// ListMessages returns the user's inbox messages received since the given
	// time, authenticated with the caller's Graph access token.
	ListMessages(ctx context.Context, accessToken string, since time.Time) ([]entity.EmailMessage, error)
}

// OrderLLM is the slice of gateway/llm the Order List feature needs.
type OrderLLM interface {
	// FilterOrderMessages returns the conversation ids of the order-related
	// threads among msgs — non-order threads are dropped.
	FilterOrderMessages(ctx context.Context, msgs []entity.EmailMessage) ([]string, error)
	// ExtractOrderFields returns structured order fields for one thread.
	ExtractOrderFields(ctx context.Context, thread entity.Thread) (entity.OrderExtraction, error)
}

// OrderRepo is the orders-table port. Every query filters by owner. The
// signatures use only entity/stdlib types so the repo adapter satisfies this
// interface structurally without importing this package.
type OrderRepo interface {
	// Upsert inserts or updates by (user_id, conversation_id) — idempotent,
	// because both the initial sync and Graph webhook re-deliveries hit it.
	Upsert(ctx context.Context, o entity.Order) error
	// List returns up to limit orders for the user, newest first, optionally
	// filtered by status and created strictly before the cursor time.
	List(ctx context.Context, userID uuid.UUID, status *entity.OrderStatus, before *time.Time, limit int) ([]entity.Order, error)
}

// EventPublisher pushes typed events over the shared realtime channel.
type EventPublisher interface {
	Publish(e entity.Event)
}
