// Package messages holds the post-sync message usecases: the webhook job that
// keeps the order list live. Ports live where they are consumed.
package messages

import (
	"context"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// MessageGetter is the slice of gateway/graph the webhook job needs.
type MessageGetter interface {
	GetMessage(ctx context.Context, accessToken, messageID string) (entity.EmailMessage, error)
}

// OrderLLM matches usecase/orders' port: filter decides order-relevance for
// unseen conversations, extract structures the fields.
type OrderLLM interface {
	FilterOrderMessages(ctx context.Context, msgs []entity.EmailMessage) ([]string, error)
	ExtractOrderFields(ctx context.Context, thread entity.Thread) (entity.OrderExtraction, error)
}

// OrderRepo is the orders-table slice this job needs.
type OrderRepo interface {
	Upsert(ctx context.Context, o entity.Order) error
	GetByConversation(ctx context.Context, userID uuid.UUID, conversationID string) (entity.Order, bool, error)
}

// SubscriptionResolver maps a Graph subscription id to its owning user.
type SubscriptionResolver interface {
	ResolveSubscription(ctx context.Context, graphSubscriptionID string) (entity.User, bool, error)
}

// TokenSource hands out the user's most recent Graph access token.
type TokenSource interface {
	GraphToken(userID uuid.UUID) (string, bool)
}

// EventPublisher pushes typed events over the shared realtime channel.
type EventPublisher interface {
	Publish(e entity.Event)
}

// DiscrepancyCatcher is the Audit hook — usecase/orders.CatchDiscrepancies
// satisfies it structurally. It audits the new message against its
// conversation history and returns whatever it stored.
type DiscrepancyCatcher interface {
	Execute(ctx context.Context, accessToken string, order entity.Order, newMsg entity.EmailMessage) ([]entity.Discrepancy, error)
}

// CheckupCanceller is the Checkups hook — this job is the only place in the
// system that sees supplier replies, so it is what wires cancel_if_reply to
// reality. repo.Checkups satisfies it structurally.
type CheckupCanceller interface {
	CancelPendingByConversation(ctx context.Context, userID uuid.UUID, conversationID string) (int, error)
}
