// Package entity is the innermost ring: domain types imported by every layer,
// importing nothing but stdlib and uuid. Entities carry no JSON/DB tags —
// controllers map to DTOs (camelCase), repos map to columns (snake_case).
package entity

import (
	"time"

	"github.com/google/uuid"
)

type Account struct {
	ID          uuid.UUID
	CompanyName string
	CreatedAt   time.Time
}

type User struct {
	ID                   uuid.UUID
	AccountID            uuid.UUID
	Email                string
	GraphRefreshTokenEnc string // encrypted at rest
	CreatedAt            time.Time
}

type GraphSubscription struct {
	ID                  uuid.UUID
	UserID              uuid.UUID
	GraphSubscriptionID string
	ExpiresAt           time.Time // renewal job reads this (~3-day max lifetime)
	CreatedAt           time.Time
}
