package checkups

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SendDueCheckups is the scheduler's usecase: claim rows that are due
// (send_date <= now — never ==, the ticker won't land exactly on SendDate),
// send each via Graph, and mark them sent. One failed send never stops the
// batch: the row is released back to pending and the next tick retries it.
type SendDueCheckups struct {
	Repo   CheckupRepo
	Mail   MailSender
	Tokens TokenSource
	Now    func() time.Time
	Log    interface {
		Error(msg string, args ...any)
	}
}

// Execute returns how many messages were sent this tick.
func (uc *SendDueCheckups) Execute(ctx context.Context) (int, error) {
	due, err := uc.Repo.ClaimDue(ctx, uc.Now())
	if err != nil {
		return 0, fmt.Errorf("claim due checkups: %w", err)
	}

	sent := 0
	for _, m := range due {
		token, ok := uc.Tokens.GraphToken(m.UserID)
		if !ok {
			uc.Log.Error("checkup send skipped: no cached graph token", "checkupId", m.ID, "userId", m.UserID)
			uc.release(ctx, m.ID)
			continue
		}
		if err := uc.Mail.SendMail(ctx, token, m.EmailAddress, m.Subject, m.Body); err != nil {
			uc.Log.Error("checkup send failed", "checkupId", m.ID, "to", m.EmailAddress, "error", err)
			uc.release(ctx, m.ID)
			continue
		}
		// The usecase, not the gateway, marks the row sent.
		if err := uc.Repo.MarkSent(ctx, m.ID); err != nil {
			return sent, fmt.Errorf("mark checkup %s sent: %w", m.ID, err)
		}
		sent++
	}
	return sent, nil
}

func (uc *SendDueCheckups) release(ctx context.Context, id uuid.UUID) {
	if err := uc.Repo.Release(ctx, id); err != nil {
		uc.Log.Error("release claimed checkup failed", "checkupId", id, "error", err)
	}
}
