package entity

import "github.com/google/uuid"

// EventType names a typed event on the single realtime channel (GET /ws).
// New features add event types, never new sockets.
type EventType string

const (
	EventOrderUpdated     EventType = "order.updated"
	EventDiscrepancyFound EventType = "discrepancy.found"
	EventSyncCompleted    EventType = "sync.completed"
)

// Event is the envelope pushed over the realtime channel:
// { "type": "...", "data": {...} }. controller/ws serializes Data.
type Event struct {
	Type EventType
	Data any
}

// SyncCompleted is the Data payload for EventSyncCompleted.
type SyncCompleted struct {
	SyncID         uuid.UUID
	OrdersUpserted int
}
