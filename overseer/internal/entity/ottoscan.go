package entity

import (
	"time"

	"github.com/google/uuid"
)

type ScanStatus string

const (
	ScanScanned   ScanStatus = "scanned"   // extraction + destinations returned; awaiting approval
	ScanRecorded  ScanStatus = "recorded"  // user approved; row appended
	ScanAbandoned ScanStatus = "abandoned" // user walked away (not set anywhere yet)
)

// ValidScanStatus reports whether s is one of the known statuses, matching
// the DB CHECK vocabulary.
func ValidScanStatus(s ScanStatus) bool {
	switch s {
	case ScanScanned, ScanRecorded, ScanAbandoned:
		return true
	}
	return false
}

// OttoScan is one "Ask Otto" interaction: what the LLM extracted from the
// thread, waiting for the user's edited approval on the confirm card.
// Fields reuses OrderExtraction — the confirm card edits exactly that shape,
// and RecordOrder receives the user-approved version of it.
type OttoScan struct {
	ID               uuid.UUID // the scanId used in controller paths
	AccountID        uuid.UUID
	UserID           uuid.UUID
	MessageID        string // Graph message id the user asked Otto about
	ConversationID   string
	EmailURL         string // latest message's webLink
	Fields           OrderExtraction
	SelectedResource string // drive item id — set at record time
	Status           ScanStatus
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
