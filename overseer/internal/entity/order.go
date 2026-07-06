package entity

import (
	"time"

	"github.com/google/uuid"
)

type OrderStatus string

const (
	OrderStatusInTransit OrderStatus = "in_transit"
	OrderStatusConfirmed OrderStatus = "confirmed"
	OrderStatusDelivered OrderStatus = "delivered"
	OrderStatusIssue     OrderStatus = "issue"
)

// ValidOrderStatus reports whether s is one of the known statuses; the LLM
// gateway validates model output against this before it ever reaches a usecase.
func ValidOrderStatus(s OrderStatus) bool {
	switch s {
	case OrderStatusInTransit, OrderStatusConfirmed, OrderStatusDelivered, OrderStatusIssue:
		return true
	}
	return false
}

type Order struct {
	ID              uuid.UUID
	AccountID       uuid.UUID
	UserID          uuid.UUID
	ConversationID  string // Graph conversation id — webhook/audit lookup key
	CompanyName     string
	EmailAddress    string
	OrderID         int // supplier's PO number (business id), NOT the row PK
	EmailURL        string
	Status          OrderStatus
	OriginalEstDate *time.Time // pointer = nullable column
	CurrentEstDate  *time.Time
	LastMessenger   string
	SupportingQuote string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// OrderExtraction is what gateway/llm.ExtractOrderFields returns for a thread:
// the structured order fields with no ownership attached yet. Usecases stamp
// account/user/conversation before persisting.
type OrderExtraction struct {
	CompanyName     string
	EmailAddress    string
	OrderID         int
	Status          OrderStatus
	OriginalEstDate *time.Time
	CurrentEstDate  *time.Time
	LastMessenger   string
	SupportingQuote string
}
