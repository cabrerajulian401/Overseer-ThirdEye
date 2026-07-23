// Package checkups holds the Checkups usecases: create a follow-up flow,
// send due messages on the scheduler's tick, pause/resume/cancel, and list.
// Ports live where they are consumed.
package checkups

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// MailSender is the slice of gateway/graph the sender needs. The usecase (not
// the gateway) marks the row sent after the send succeeds.
type MailSender interface {
	SendMail(ctx context.Context, accessToken, to, subject, body string) error
}

// TokenSource hands out a user's most recent Graph access token — the
// scheduler runs without a request context, so tokens come from the cache.
type TokenSource interface {
	GraphToken(userID uuid.UUID) (string, bool)
}

// CheckupRepo is the checkup_messages-table port. Every query filters by
// owner except the scheduler's claim path, which spans users by design.
type CheckupRepo interface {
	// InsertFlow stores one flow's messages atomically and returns the stored
	// rows (server-minted ids, timestamps).
	InsertFlow(ctx context.Context, msgs []entity.CheckupMessage) ([]entity.CheckupMessage, error)
	// ClaimDue atomically flips pending rows with send_date <= now to
	// 'sending' and returns them; two workers can't claim the same row.
	ClaimDue(ctx context.Context, now time.Time) ([]entity.CheckupMessage, error)
	// MarkSent finishes a claimed row.
	MarkSent(ctx context.Context, id uuid.UUID) error
	// Release returns a claimed row to 'pending' so the next tick retries it.
	Release(ctx context.Context, id uuid.UUID) error
	// GetByID returns the user's row; found=false when no such row belongs
	// to the user.
	GetByID(ctx context.Context, userID, id uuid.UUID) (entity.CheckupMessage, bool, error)
	// UpdateStatus sets the row's status; found=false when no owned row
	// matched. Transition legality is the usecase's decision.
	UpdateStatus(ctx context.Context, userID, id uuid.UUID, s entity.CheckupStatus) (entity.CheckupMessage, bool, error)
	// ListByUser returns the user's rows, soonest send_date first; nil = all.
	ListByUser(ctx context.Context, userID uuid.UUID, status *entity.CheckupStatus) ([]entity.CheckupMessage, error)
}
