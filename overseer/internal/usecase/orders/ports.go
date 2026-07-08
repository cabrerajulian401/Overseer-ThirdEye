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

// ThreadFetcher is the slice of gateway/graph the Audit needs: conversation
// history is re-fetched from Graph at audit time (no messages table; Graph
// is the source of truth).
type ThreadFetcher interface {
	// ListConversationMessages returns every message in the conversation,
	// oldest → newest.
	ListConversationMessages(ctx context.Context, accessToken, conversationID string) ([]entity.EmailMessage, error)
}

// DiscrepancyLLM is the slice of gateway/llm the Audit needs.
type DiscrepancyLLM interface {
	// FindDiscrepancies compares the new message against the conversation
	// history and returns zero or more findings.
	FindDiscrepancies(ctx context.Context, newMsg entity.EmailMessage, history []entity.EmailMessage) ([]entity.DiscrepancyFinding, error)
}

// DiscrepancyRepo is the discrepancies-table port. Every query filters by owner.
type DiscrepancyRepo interface {
	// Insert stores a finding and returns the stored row (server-minted id,
	// timestamps) so events carry the authoritative row.
	Insert(ctx context.Context, d entity.Discrepancy) (entity.Discrepancy, error)
	// ListByUser returns the user's discrepancies, newest first, optionally
	// filtered by status (nil = all).
	ListByUser(ctx context.Context, userID uuid.UUID, status *entity.DiscrepancyStatus) ([]entity.Discrepancy, error)
	// UpdateStatus sets the status of the user's discrepancy; found=false
	// when no such row belongs to the user.
	UpdateStatus(ctx context.Context, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, bool, error)
}
