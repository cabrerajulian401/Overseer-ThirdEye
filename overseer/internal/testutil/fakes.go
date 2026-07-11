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

	// ListConversationMessages support: conversation id → full thread,
	// oldest → newest (like the real gateway returns).
	ConversationsByID    map[string][]entity.EmailMessage
	ListConvErr          error
	ConversationsFetched []string
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

func (f *FakeGraph) ListConversationMessages(_ context.Context, accessToken, conversationID string) ([]entity.EmailMessage, error) {
	f.TokensSeen = append(f.TokensSeen, accessToken)
	f.ConversationsFetched = append(f.ConversationsFetched, conversationID)
	if f.ListConvErr != nil {
		return nil, f.ListConvErr
	}
	return f.ConversationsByID[conversationID], nil
}

// --- FakeLLM -----------------------------------------------------------------

type FakeLLM struct {
	// FilterResult is the conversation ids FilterOrderMessages keeps.
	FilterResult []string
	FilterErr    error
	// Extractions maps conversation id → extraction returned for that thread.
	Extractions map[string]entity.OrderExtraction
	ExtractErr  error
	// Findings maps the new message's id → what FindDiscrepancies returns.
	Findings map[string][]entity.DiscrepancyFinding
	FindErr  error
	// RankResult is what RankDestinations returns; RankCandidatesSeen records
	// the candidate ids of each call.
	RankResult []entity.DriveItem
	RankErr    error
	// PlanResult is what PlanSheetAppend returns; PlanFieldsSeen records the
	// (user-approved) fields of each call.
	PlanResult entity.SheetAppendPlan
	PlanErr    error

	FilterCalls        int
	ExtractCalls       int
	ThreadsSeen        []string
	FindCalls          int
	HistoriesSeen      [][]string // message ids of each history passed in
	RankCalls          int
	RankCandidatesSeen [][]string
	PlanCalls          int
	PlanFieldsSeen     []entity.OrderExtraction
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

func (f *FakeLLM) FindDiscrepancies(_ context.Context, newMsg entity.EmailMessage, history []entity.EmailMessage) ([]entity.DiscrepancyFinding, error) {
	f.FindCalls++
	ids := make([]string, 0, len(history))
	for _, m := range history {
		ids = append(ids, m.ID)
	}
	f.HistoriesSeen = append(f.HistoriesSeen, ids)
	if f.FindErr != nil {
		return nil, f.FindErr
	}
	return f.Findings[newMsg.ID], nil
}

func (f *FakeLLM) RankDestinations(_ context.Context, _ entity.OrderExtraction, candidates []entity.DriveItem) ([]entity.DriveItem, error) {
	f.RankCalls++
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.ID)
	}
	f.RankCandidatesSeen = append(f.RankCandidatesSeen, ids)
	if f.RankErr != nil {
		return nil, f.RankErr
	}
	return f.RankResult, nil
}

func (f *FakeLLM) PlanSheetAppend(_ context.Context, _ entity.WorkbookStructure, fields entity.OrderExtraction) (entity.SheetAppendPlan, error) {
	f.PlanCalls++
	f.PlanFieldsSeen = append(f.PlanFieldsSeen, fields)
	if f.PlanErr != nil {
		return entity.SheetAppendPlan{}, f.PlanErr
	}
	return f.PlanResult, nil
}

// --- FakeDrive -----------------------------------------------------------------

type FakeDrive struct {
	// SearchResults maps query → items returned for it.
	SearchResults map[string][]entity.DriveItem
	SearchErr     error
	RecentItems   []entity.DriveItem
	RecentErr     error
	// Items maps item id → item for GetItem.
	Items      map[string]entity.DriveItem
	GetItemErr error

	QueriesSeen  []string
	RecentCalls  int
	ItemsFetched []string
	TokensSeen   []string
}

func (f *FakeDrive) Search(_ context.Context, accessToken, query string) ([]entity.DriveItem, error) {
	f.TokensSeen = append(f.TokensSeen, accessToken)
	f.QueriesSeen = append(f.QueriesSeen, query)
	if f.SearchErr != nil {
		return nil, f.SearchErr
	}
	return f.SearchResults[query], nil
}

func (f *FakeDrive) Recent(_ context.Context, accessToken string) ([]entity.DriveItem, error) {
	f.TokensSeen = append(f.TokensSeen, accessToken)
	f.RecentCalls++
	if f.RecentErr != nil {
		return nil, f.RecentErr
	}
	return f.RecentItems, nil
}

func (f *FakeDrive) GetItem(_ context.Context, accessToken, itemID string) (entity.DriveItem, error) {
	f.TokensSeen = append(f.TokensSeen, accessToken)
	f.ItemsFetched = append(f.ItemsFetched, itemID)
	if f.GetItemErr != nil {
		return entity.DriveItem{}, f.GetItemErr
	}
	item, ok := f.Items[itemID]
	if !ok {
		return entity.DriveItem{}, fmt.Errorf("fake drive: no item %q", itemID)
	}
	return item, nil
}

// --- FakeExcel -----------------------------------------------------------------

// ExcelAppend records one AppendTableRow invocation.
type ExcelAppend struct {
	ItemID string
	Table  string
	Values []string
}

type FakeExcel struct {
	// Structures maps drive item id → workbook structure.
	Structures   map[string]entity.WorkbookStructure
	StructureErr error
	AppendErr    error

	StructuresFetched []string
	Appends           []ExcelAppend
}

func (f *FakeExcel) GetWorkbookStructure(_ context.Context, _ string, itemID string) (entity.WorkbookStructure, error) {
	f.StructuresFetched = append(f.StructuresFetched, itemID)
	if f.StructureErr != nil {
		return entity.WorkbookStructure{}, f.StructureErr
	}
	structure, ok := f.Structures[itemID]
	if !ok {
		return entity.WorkbookStructure{}, fmt.Errorf("fake excel: no workbook %q", itemID)
	}
	return structure, nil
}

func (f *FakeExcel) AppendTableRow(_ context.Context, _ string, itemID, tableName string, values []string) error {
	if f.AppendErr != nil {
		return f.AppendErr
	}
	f.Appends = append(f.Appends, ExcelAppend{ItemID: itemID, Table: tableName, Values: values})
	return nil
}

// --- InMemOttoScans --------------------------------------------------------------

// InMemOttoScans is an in-memory ScanRepo.
type InMemOttoScans struct {
	mu      sync.Mutex
	rows    map[uuid.UUID]entity.OttoScan
	Inserts int
}

func NewInMemOttoScans() *InMemOttoScans {
	return &InMemOttoScans{rows: make(map[uuid.UUID]entity.OttoScan)}
}

func (r *InMemOttoScans) Insert(_ context.Context, s entity.OttoScan) (entity.OttoScan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Inserts++
	s.ID = uuid.New()
	s.CreatedAt = time.Now()
	s.UpdatedAt = s.CreatedAt
	r.rows[s.ID] = s
	return s, nil
}

func (r *InMemOttoScans) GetByID(_ context.Context, userID, id uuid.UUID) (entity.OttoScan, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.rows[id]
	if !ok || s.UserID != userID {
		return entity.OttoScan{}, false, nil
	}
	return s, true, nil
}

func (r *InMemOttoScans) MarkRecorded(_ context.Context, userID, id uuid.UUID, selectedResource string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.rows[id]
	if !ok || s.UserID != userID {
		return fmt.Errorf("fake otto scans: no scan %s for user", id)
	}
	s.Status = entity.ScanRecorded
	s.SelectedResource = selectedResource
	s.UpdatedAt = time.Now()
	r.rows[id] = s
	return nil
}

func (r *InMemOttoScans) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

func (r *InMemOttoScans) All() []entity.OttoScan {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]entity.OttoScan, 0, len(r.rows))
	for _, s := range r.rows {
		out = append(out, s)
	}
	return out
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

// --- InMemDiscrepancies --------------------------------------------------------

// InMemDiscrepancies is an in-memory DiscrepancyRepo.
type InMemDiscrepancies struct {
	mu      sync.Mutex
	rows    map[uuid.UUID]entity.Discrepancy
	Inserts int
}

func NewInMemDiscrepancies() *InMemDiscrepancies {
	return &InMemDiscrepancies{rows: make(map[uuid.UUID]entity.Discrepancy)}
}

func (r *InMemDiscrepancies) Insert(_ context.Context, d entity.Discrepancy) (entity.Discrepancy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Inserts++
	d.ID = uuid.New()
	d.CreatedAt = time.Now().Add(time.Duration(len(r.rows)) * time.Millisecond)
	d.UpdatedAt = d.CreatedAt
	r.rows[d.ID] = d
	return d, nil
}

func (r *InMemDiscrepancies) ListByUser(_ context.Context, userID uuid.UUID, status *entity.DiscrepancyStatus) ([]entity.Discrepancy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []entity.Discrepancy
	for _, d := range r.rows {
		if d.UserID != userID {
			continue
		}
		if status != nil && d.Status != *status {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (r *InMemDiscrepancies) UpdateStatus(_ context.Context, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[id]
	if !ok || d.UserID != userID {
		return entity.Discrepancy{}, false, nil
	}
	d.Status = s
	d.UpdatedAt = time.Now()
	r.rows[id] = d
	return d, true, nil
}

func (r *InMemDiscrepancies) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

func (r *InMemDiscrepancies) All() []entity.Discrepancy {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]entity.Discrepancy, 0, len(r.rows))
	for _, d := range r.rows {
		out = append(out, d)
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
