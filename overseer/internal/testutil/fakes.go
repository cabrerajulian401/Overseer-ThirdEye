// Package testutil holds the shared fakes for controller E2E tests: fake
// Graph/LLM gateways, an in-memory order repo, and an event recorder that
// captures what would have been pushed over /ws. No test touches a real
// external service.
package testutil

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// --- FakeGraph ---------------------------------------------------------------

type FakeGraph struct {
	Messages []entity.EmailMessage
	Err      error

	TokensSeen []string
	SincesSeen []time.Time
}

func (f *FakeGraph) ListMessages(_ context.Context, accessToken string, since time.Time) ([]entity.EmailMessage, error) {
	f.TokensSeen = append(f.TokensSeen, accessToken)
	f.SincesSeen = append(f.SincesSeen, since)
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Messages, nil
}

// --- FakeLLM -----------------------------------------------------------------

type FakeLLM struct {
	// FilterResult is the conversation ids FilterOrderMessages keeps.
	FilterResult []string
	FilterErr    error
	// Extractions maps conversation id → extraction returned for that thread.
	Extractions map[string]entity.OrderExtraction
	ExtractErr  error

	FilterCalls  int
	ExtractCalls int
	ThreadsSeen  []string
}

func (f *FakeLLM) FilterOrderMessages(_ context.Context, _ []entity.EmailMessage) ([]string, error) {
	f.FilterCalls++
	if f.FilterErr != nil {
		return nil, f.FilterErr
	}
	return f.FilterResult, nil
}

func (f *FakeLLM) ExtractOrderFields(_ context.Context, thread entity.Thread) (entity.OrderExtraction, error) {
	f.ExtractCalls++
	f.ThreadsSeen = append(f.ThreadsSeen, thread.ConversationID)
	if f.ExtractErr != nil {
		return entity.OrderExtraction{}, f.ExtractErr
	}
	return f.Extractions[thread.ConversationID], nil
}

// --- InMemOrders -------------------------------------------------------------

// InMemOrders is an in-memory OrderRepo keyed on (user_id, conversation_id),
// mirroring the unique index the real upsert relies on.
type InMemOrders struct {
	mu      sync.Mutex
	rows    map[string]entity.Order
	Upserts int
}

func NewInMemOrders() *InMemOrders {
	return &InMemOrders{rows: make(map[string]entity.Order)}
}

func key(userID uuid.UUID, conversationID string) string {
	return userID.String() + "|" + conversationID
}

func (r *InMemOrders) Upsert(_ context.Context, o entity.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Upserts++

	k := key(o.UserID, o.ConversationID)
	if existing, ok := r.rows[k]; ok {
		o.ID = existing.ID
		o.CreatedAt = existing.CreatedAt
	} else {
		o.ID = uuid.New()
		o.CreatedAt = time.Now().Add(time.Duration(len(r.rows)) * time.Millisecond)
	}
	o.UpdatedAt = time.Now()
	r.rows[k] = o
	return nil
}

func (r *InMemOrders) List(_ context.Context, userID uuid.UUID, status *entity.OrderStatus, before *time.Time, limit int) ([]entity.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []entity.Order
	for _, o := range r.rows {
		if o.UserID != userID {
			continue
		}
		if status != nil && o.Status != *status {
			continue
		}
		if before != nil && !o.CreatedAt.Before(*before) {
			continue
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *InMemOrders) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

func (r *InMemOrders) All() []entity.Order {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]entity.Order, 0, len(r.rows))
	for _, o := range r.rows {
		out = append(out, o)
	}
	return out
}

// --- EventRecorder -----------------------------------------------------------

// EventRecorder implements EventPublisher and records what would have gone
// out over the realtime channel.
type EventRecorder struct {
	mu     sync.Mutex
	Events []entity.Event
}

func (r *EventRecorder) Publish(e entity.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Events = append(r.Events, e)
}

func (r *EventRecorder) OfType(t entity.EventType) []entity.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []entity.Event
	for _, e := range r.Events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// --- StaticResolver ----------------------------------------------------------

// StaticResolver resolves every bearer token to a fixed user — the E2E
// equivalent of MVP single-user mode.
type StaticResolver struct {
	User entity.User
}

func (r StaticResolver) Resolve(_ context.Context, _ string) (entity.User, error) {
	return r.User, nil
}
