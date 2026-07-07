package subscriptions

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// RenewDueSubscriptions is the renewal pass the scheduler worker runs:
// message subscriptions live ~3 days max, so anything expiring within
// RenewAhead gets pushed forward by TTL. Per-subscription failures are
// logged and skipped — one dead token must not stall the rest.
type RenewDueSubscriptions struct {
	Graph  GraphSubscriber
	Repo   SubscriptionRepo
	Tokens TokenSource

	TTL        time.Duration
	RenewAhead time.Duration
	Now        func() time.Time
	Log        *slog.Logger
}

func (uc *RenewDueSubscriptions) Execute(ctx context.Context) (int, error) {
	due, err := uc.Repo.ListExpiring(ctx, uc.Now().Add(uc.RenewAhead))
	if err != nil {
		return 0, fmt.Errorf("list expiring subscriptions: %w", err)
	}

	renewed := 0
	for _, s := range due {
		token, ok := uc.Tokens.GraphToken(s.UserID)
		if !ok {
			uc.Log.Warn("subscription renewal skipped: no cached token",
				"subscriptionId", s.GraphSubscriptionID, "userId", s.UserID)
			continue
		}
		newExpiry := uc.Now().Add(uc.TTL)
		if err := uc.Graph.RenewSubscription(ctx, token, s.GraphSubscriptionID, newExpiry); err != nil {
			uc.Log.Error("subscription renewal failed",
				"subscriptionId", s.GraphSubscriptionID, "error", err)
			continue
		}
		if err := uc.Repo.UpdateExpiry(ctx, s.GraphSubscriptionID, newExpiry); err != nil {
			uc.Log.Error("subscription expiry update failed",
				"subscriptionId", s.GraphSubscriptionID, "error", err)
			continue
		}
		renewed++
	}
	return renewed, nil
}
