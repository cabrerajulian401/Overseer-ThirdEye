package checkups

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// CreateCheckupMessages serves POST /checkups: expand the flow's cadence into
// one row per message (spec: every row is a MESSAGE, not a flow), all sharing
// one server-minted flow id.
type CreateCheckupMessages struct {
	Repo CheckupRepo
}

type CreateInput struct {
	AccountID      uuid.UUID
	UserID         uuid.UUID
	ConversationID string
	CompanyName    string
	EmailAddress   string
	OrderID        int
	Subject        string
	Body           string
	SendDate       time.Time // first message; later ones step by RepeatEvery
	CurrentEstDate *time.Time
	CancelIfReply  bool
	// RepeatFlow is the total number of messages in the flow (< 1 means 1);
	// RepeatEvery is the gap between consecutive sends.
	RepeatFlow  int
	RepeatEvery time.Duration
}

// Execute mints the flow id, computes each message's send date off the
// cadence, and stores the rows. Returns the stored rows with server-minted
// ids in send-date order.
func (uc *CreateCheckupMessages) Execute(ctx context.Context, in CreateInput) ([]entity.CheckupMessage, error) {
	count := in.RepeatFlow
	if count < 1 {
		count = 1
	}
	if count > 1 && in.RepeatEvery <= 0 {
		return nil, fmt.Errorf("repeat interval must be positive for a %d-message flow", count)
	}

	flowID := uuid.New()
	msgs := make([]entity.CheckupMessage, 0, count)
	for i := 0; i < count; i++ {
		msgs = append(msgs, entity.CheckupMessage{
			AccountID:      in.AccountID,
			UserID:         in.UserID,
			FlowID:         flowID,
			ConversationID: in.ConversationID,
			CompanyName:    in.CompanyName,
			EmailAddress:   in.EmailAddress,
			OrderID:        in.OrderID,
			Status:         entity.CheckupPending,
			Subject:        in.Subject,
			Body:           in.Body,
			SendDate:       in.SendDate.Add(time.Duration(i) * in.RepeatEvery),
			CurrentEstDate: in.CurrentEstDate,
			CancelIfReply:  in.CancelIfReply,
			RepeatFlow:     count,
		})
	}

	stored, err := uc.Repo.InsertFlow(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("insert checkup flow: %w", err)
	}
	return stored, nil
}
