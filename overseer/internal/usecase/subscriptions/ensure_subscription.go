package subscriptions

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// EnsureSubscription creates (or replaces) the user's Graph message
// subscription and persists the mapping the webhook needs. POST /subscriptions
// calls this after onboarding; re-running it simply replaces the row.
type EnsureSubscription struct {
	Graph GraphSubscriber
	Repo  SubscriptionRepo

	NotificationURL string // public URL of POST /webhooks/graph
	ClientState     string // shared secret echoed back in every notification
	TTL             time.Duration
	Now             func() time.Time
}

type EnsureInput struct {
	UserID     uuid.UUID
	GraphToken string
}

type EnsureOutput struct {
	GraphSubscriptionID string
	ExpiresAt           time.Time
}

func (uc *EnsureSubscription) Execute(ctx context.Context, in EnsureInput) (EnsureOutput, error) {
	expires := uc.Now().Add(uc.TTL)

	id, err := uc.Graph.CreateSubscription(ctx, in.GraphToken, uc.NotificationURL, uc.ClientState, expires)
	if err != nil {
		return EnsureOutput{}, fmt.Errorf("create graph subscription: %w", err)
	}
	if err := uc.Repo.UpsertForUser(ctx, in.UserID, id, expires); err != nil {
		return EnsureOutput{}, fmt.Errorf("persist subscription: %w", err)
	}
	return EnsureOutput{GraphSubscriptionID: id, ExpiresAt: expires}, nil
}
