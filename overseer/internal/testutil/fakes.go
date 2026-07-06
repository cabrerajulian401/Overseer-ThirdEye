// Package testutil holds the shared fakes for controller E2E tests: fake
// Graph/LLM gateways, an in-memory order repo, and an event recorder that
// captures what would have been pushed over /ws. No test touches a real
// external service.
package testutil

import (
	"context"
	"fmt"
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

	// GetMessage support: message id → message. GetErr forces a failure.
	MessagesByID map[string]entity.EmailMessage
	GetErr       error
	GetCalls     []string

	// Subscription support.
	CreatedSubID string
	CreateErr    error
	CreateCalls  []SubscriptionCall
	RenewErr     error
	RenewedIDs   []string
}

// SubscriptionCall records one CreateSubscription invocation.
type SubscriptionCall struct {
	Token           string
	NotificationURL string
	ClientState     string
	Expires         time.Time
}

func (f *FakeGraph) ListMessages(_ context.Context, accessToken string, since time.Time) ([]entity.EmailMessage, error) {
	f.TokensSeen = append(f.TokensSeen, accessToken)
	f.SincesSeen = append(f.SincesSeen, since)
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Messages, nil
}

func (f *FakeGraph) GetMessage(_ context.Context, accessToken, messageID string) (entity.EmailMessage, error) {
	f.TokensSeen = append(f.TokensSeen, accessToken)
	f.GetCalls = append(f.GetCalls, messageID)
	if f.GetErr != nil {
		return entity.EmailMessage{}, f.GetErr
	}
	msg, ok := f.MessagesByID[messageID]
	if !ok {
		return entity.EmailMessage{}, fmt.Errorf("fake graph: no message %q", messageID)
	}
	return msg, nil
}

func (f *FakeGraph) CreateSubscription(_ context.Context, accessToken, notificationURL, clientState string, expires time.Time) (string, error) {
	f.CreateCalls = append(f.CreateCalls, SubscriptionCall{Token: accessToken, NotificationURL: notificationURL, ClientState: clientState, Expires: expires})
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	return f.CreatedSubID, nil
}

func (f *FakeGraph) RenewSubscription(_ context.Context, _ string, subscriptionID string, _ time.Time) error {
	if f.RenewErr != nil {
		return f.RenewErr
	}
	f.RenewedIDs = append(f.RenewedIDs, subscriptionID)
	return nil
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

func (r *InMemOrders) GetByConversation(_ context.Context, userID uuid.UUID, conversationID string) (entity.Order, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.rows[key(userID, conversationID)]
	return o, ok, nil
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

// --- InMemSubscriptions --------------------------------------------------------

// InMemSubscriptions backs both the messages usecases' SubscriptionResolver
// and the subscriptions usecases' SubscriptionRepo.
type InMemSubscriptions struct {
	mu    sync.Mutex
	users map[uuid.UUID]entity.User           // user id → user
	subs  map[string]entity.GraphSubscription // graph subscription id → row
}

func NewInMemSubscriptions() *InMemSubscriptions {
	return &InMemSubscriptions{
		users: make(map[uuid.UUID]entity.User),
		subs:  make(map[string]entity.GraphSubscription),
	}
}

// AddUser registers the user rows the join in ResolveSubscription would see.
func (r *InMemSubscriptions) AddUser(u entity.User) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[u.ID] = u
}

func (r *InMemSubscriptions) UpsertForUser(_ context.Context, userID uuid.UUID, graphSubscriptionID string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.subs { // one subscription per user, like ON CONFLICT (user_id)
		if s.UserID == userID {
			delete(r.subs, id)
		}
	}
	r.subs[graphSubscriptionID] = entity.GraphSubscription{
		ID: uuid.New(), UserID: userID,
		GraphSubscriptionID: graphSubscriptionID, ExpiresAt: expiresAt, CreatedAt: time.Now(),
	}
	return nil
}

func (r *InMemSubscriptions) ResolveSubscription(_ context.Context, graphSubscriptionID string) (entity.User, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subs[graphSubscriptionID]
	if !ok {
		return entity.User{}, false, nil
	}
	u, ok := r.users[s.UserID]
	return u, ok, nil
}

func (r *InMemSubscriptions) ListExpiring(_ context.Context, before time.Time) ([]entity.GraphSubscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []entity.GraphSubscription
	for _, s := range r.subs {
		if s.ExpiresAt.Before(before) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *InMemSubscriptions) UpdateExpiry(_ context.Context, graphSubscriptionID string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subs[graphSubscriptionID]
	if !ok {
		return fmt.Errorf("fake subscriptions: no subscription %q", graphSubscriptionID)
	}
	s.ExpiresAt = expiresAt
	r.subs[graphSubscriptionID] = s
	return nil
}

// --- FakeTokens ----------------------------------------------------------------

// FakeTokens is an in-memory TokenSource/TokenSink.
type FakeTokens struct {
	mu     sync.Mutex
	tokens map[uuid.UUID]string
}

func NewFakeTokens() *FakeTokens {
	return &FakeTokens{tokens: make(map[uuid.UUID]string)}
}

func (f *FakeTokens) Put(userID uuid.UUID, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[userID] = token
}

func (f *FakeTokens) GraphToken(userID uuid.UUID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[userID]
	return t, ok
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
