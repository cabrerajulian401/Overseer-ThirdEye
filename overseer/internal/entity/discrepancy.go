package entity

import (
	"time"

	"github.com/google/uuid"
)

type DiscrepancyStatus string

const (
	DiscrepancyOpen      DiscrepancyStatus = "open"
	DiscrepancyResolved  DiscrepancyStatus = "resolved"
	DiscrepancyDismissed DiscrepancyStatus = "dismissed"
)

// ValidDiscrepancyStatus reports whether s is one of the known statuses,
// matching the DB CHECK vocabulary.
func ValidDiscrepancyStatus(s DiscrepancyStatus) bool {
	switch s {
	case DiscrepancyOpen, DiscrepancyResolved, DiscrepancyDismissed:
		return true
	}
	return false
}

type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

func ValidPriority(p Priority) bool {
	switch p {
	case PriorityHigh, PriorityMedium, PriorityLow:
		return true
	}
	return false
}

// MetricType names which agreed-upon metric a discrepancy is about.
type MetricType string

const (
	MetricPrice MetricType = "price"
	MetricDate  MetricType = "date"
	MetricUnit  MetricType = "unit"
)

func ValidMetricType(m MetricType) bool {
	switch m {
	case MetricPrice, MetricDate, MetricUnit:
		return true
	}
	return false
}

// DiscrepancyDetail is the previously-agreed vs newly-stated value pair.
type DiscrepancyDetail struct {
	PreviouslyAgreed string
	NewMetric        string
	Type             MetricType
}

// DiscrepancyQuote evidences a finding: a verbatim quote from the history and
// a deeplink to the message it came from.
type DiscrepancyQuote struct {
	Quote       string
	MessageLink string
}

// DiscrepancyFinding is what gateway/llm.FindDiscrepancies returns: a finding
// with no ownership attached yet. CatchDiscrepancies stamps account/user/
// conversation/order fields before persisting.
type DiscrepancyFinding struct {
	Title    string
	Priority Priority
	Detail   DiscrepancyDetail
	Quotes   []DiscrepancyQuote
}

type Discrepancy struct {
	ID               uuid.UUID
	AccountID        uuid.UUID
	UserID           uuid.UUID
	ConversationID   string // Graph conversation id — same lookup key as orders
	Status           DiscrepancyStatus
	CompanyName      string
	EmailAddress     string
	EmailDeeplinkURL string // so the UI can open a draft to the supplier
	OrderID          int    // supplier's PO number, copied from the order row
	Priority         Priority
	Title            string
	Detail           DiscrepancyDetail
	Quotes           []DiscrepancyQuote
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
