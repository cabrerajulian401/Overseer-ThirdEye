package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thirdeye/overseer/internal/entity"
)

const upsertSubscriptionSQL = `
	INSERT INTO graph_subscriptions (user_id, graph_subscription_id, expires_at)
	VALUES ($1, $2, $3)
	ON CONFLICT (user_id) DO UPDATE SET
		graph_subscription_id = EXCLUDED.graph_subscription_id,
		expires_at = EXCLUDED.expires_at` // one live subscription per user (MVP)

const resolveSubscriptionSQL = `
	SELECT u.id, u.account_id, u.email
	FROM graph_subscriptions gs
	JOIN users u ON u.id = gs.user_id
	WHERE gs.graph_subscription_id = $1` // the webhook's notification → user lookup

const listExpiringSQL = `
	SELECT id, user_id, graph_subscription_id, expires_at, created_at
	FROM graph_subscriptions
	WHERE expires_at < $1`

const updateExpirySQL = `
	UPDATE graph_subscriptions SET expires_at = $2 WHERE graph_subscription_id = $1`

type Subscriptions struct {
	db *pgxpool.Pool
}

func NewSubscriptions(db *pgxpool.Pool) *Subscriptions { return &Subscriptions{db: db} }

func (r *Subscriptions) UpsertForUser(ctx context.Context, userID uuid.UUID, graphSubscriptionID string, expiresAt time.Time) error {
	if _, err := r.db.Exec(ctx, upsertSubscriptionSQL, userID, graphSubscriptionID, expiresAt); err != nil {
		return fmt.Errorf("upsert subscription: %w", err)
	}
	return nil
}

func (r *Subscriptions) ResolveSubscription(ctx context.Context, graphSubscriptionID string) (entity.User, bool, error) {
	var u entity.User
	rows, err := r.db.Query(ctx, resolveSubscriptionSQL, graphSubscriptionID)
	if err != nil {
		return entity.User{}, false, fmt.Errorf("resolve subscription: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return entity.User{}, false, rows.Err()
	}
	if err := rows.Scan(&u.ID, &u.AccountID, &u.Email); err != nil {
		return entity.User{}, false, fmt.Errorf("scan subscription user: %w", err)
	}
	return u, true, nil
}

func (r *Subscriptions) ListExpiring(ctx context.Context, before time.Time) ([]entity.GraphSubscription, error) {
	rows, err := r.db.Query(ctx, listExpiringSQL, before)
	if err != nil {
		return nil, fmt.Errorf("list expiring subscriptions: %w", err)
	}
	defer rows.Close()

	var out []entity.GraphSubscription
	for rows.Next() {
		var s entity.GraphSubscription
		if err := rows.Scan(&s.ID, &s.UserID, &s.GraphSubscriptionID, &s.ExpiresAt, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subscriptions: %w", err)
	}
	return out, nil
}

func (r *Subscriptions) UpdateExpiry(ctx context.Context, graphSubscriptionID string, expiresAt time.Time) error {
	if _, err := r.db.Exec(ctx, updateExpirySQL, graphSubscriptionID, expiresAt); err != nil {
		return fmt.Errorf("update subscription expiry: %w", err)
	}
	return nil
}
