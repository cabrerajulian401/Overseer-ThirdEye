package entity

import (
	"time"

	"github.com/google/uuid"
)

// CheckupStatus is the one status vocabulary for checkup messages — the same
// enum from the Postgres CHECK constraint to the wire.
type CheckupStatus string

const (
	CheckupPending   CheckupStatus = "pending"   // scheduled, will send
	CheckupPaused    CheckupStatus = "paused"    // toggled off; resumable
	CheckupSending   CheckupStatus = "sending"   // transient: claimed by the scheduler mid-send
	CheckupSent      CheckupStatus = "sent"      // terminal, set by the sender
	CheckupCancelled CheckupStatus = "cancelled" // terminal (user, or CancelIfReply fired)
)

// ValidCheckupStatus reports whether s is one of the stored statuses.
func ValidCheckupStatus(s CheckupStatus) bool {
	switch s {
	case CheckupPending, CheckupPaused, CheckupSending, CheckupSent, CheckupCancelled:
		return true
	}
	return false
}

// CanTransitionCheckup encodes PATCH /checkups/{checkupId}'s legal moves:
// paused ↔ pending, either may be cancelled, and a same-status PATCH is an
// allowed no-op. sent and cancelled are terminal; sending is owned by the
// scheduler and can't be patched mid-send, nor set by a client.
func CanTransitionCheckup(from, to CheckupStatus) bool {
	switch from {
	case CheckupPending, CheckupPaused:
		return to == CheckupPending || to == CheckupPaused || to == CheckupCancelled
	}
	return false
}

// CheckupMessage is one scheduled follow-up email. Every row is a MESSAGE,
// not a flow; FlowID groups the messages minted by one POST /checkups.
type CheckupMessage struct {
	ID             uuid.UUID // this IS the checkupId PATCH /checkups/{checkupId} targets
	AccountID      uuid.UUID
	UserID         uuid.UUID
	FlowID         uuid.UUID
	ConversationID string // lets HandleChangeMessage find pending rows to cancel on reply
	CompanyName    string
	EmailAddress   string
	OrderID        int
	Status         CheckupStatus
	Subject        string
	Body           string
	SendDate       time.Time
	CurrentEstDate *time.Time // pointer = nullable column
	CancelIfReply  bool
	RepeatFlow     int // total messages in the flow, denormalized onto each row
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
