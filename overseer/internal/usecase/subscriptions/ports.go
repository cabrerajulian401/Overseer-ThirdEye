// Package subscriptions manages Graph change-notification subscriptions:
// creation at onboarding and renewal before the ~3-day expiry.
package subscriptions

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// GraphSubscriber is the slice of gateway/graph these usecases need.
type GraphSubscriber interface {
	CreateSubscription(ctx context.Context, accessToken, notificationURL, clientState string, expires time.Time) (string, error)
	RenewSubscription(ctx context.Context, accessToken, subscriptionID string, expires time.Time) error
}

// SubscriptionRepo is the graph_subscriptions-table port.
type SubscriptionRepo interface {
	UpsertForUser(ctx context.Context, userID uuid.UUID, graphSubscriptionID string, expiresAt time.Time) error
	ListExpiring(ctx context.Context, before time.Time) ([]entity.GraphSubscription, error)
	UpdateExpiry(ctx context.Context, graphSubscriptionID string, expiresAt time.Time) error
}

// TokenSource hands out the user's most recent Graph access token.
type TokenSource interface {
	GraphToken(userID uuid.UUID) (string, bool)
}
