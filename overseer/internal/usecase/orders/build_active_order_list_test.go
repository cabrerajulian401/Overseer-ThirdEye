package orders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// --- tiny hand-written stubs (ports are 1-3 methods; no mocking framework) ---

type stubGraph struct {
	msgs []entity.EmailMessage
	err  error
}

func (s *stubGraph) ListMessages(_ context.Context, _ string, _ time.Time) ([]entity.EmailMessage, error) {
	return s.msgs, s.err
}

type stubLLM struct {
	filterResult []string
	filterErr    error
	extractions  map[string]entity.OrderExtraction
	extractErr   error
	extractCalls int
}

func (s *stubLLM) FilterOrderMessages(_ context.Context, _ []entity.EmailMessage) ([]string, error) {
	return s.filterResult, s.filterErr
}

func (s *stubLLM) ExtractOrderFields(_ context.Context, t entity.Thread) (entity.OrderExtraction, error) {
	s.extractCalls++
	if s.extractErr != nil {
		return entity.OrderExtraction{}, s.extractErr
	}
	return s.extractions[t.ConversationID], nil
}

type stubRepo struct {
	rows    map[string]entity.Order // keyed by user|conversation, like the unique index
	upserts int
}

func newStubRepo() *stubRepo { return &stubRepo{rows: map[string]entity.Order{}} }

func (s *stubRepo) Upsert(_ context.Context, o entity.Order) error {
	s.upserts++
	s.rows[o.UserID.String()+"|"+o.ConversationID] = o
	return nil
}

func (s *stubRepo) List(_ context.Context, _ uuid.UUID, _ *entity.OrderStatus, _ *time.Time, _ int) ([]entity.Order, error) {
	return nil, nil
}

type stubEvents struct {
	events []entity.Event
}

func (s *stubEvents) Publish(e entity.Event) { s.events = append(s.events, e) }

// --- fixtures ---------------------------------------------------------------

func msg(conv, from, subject string, at time.Time) entity.EmailMessage {
	return entity.EmailMessage{
		ID:              conv + "-" + subject,
		ConversationID:  conv,
		From:            from,
		FromName:        from,
		Subject:         subject,
		Body:            "body of " + subject,
		CreatedDateTime: at,
	}
}

func buildUsecase(g *stubGraph, l *stubLLM, r *stubRepo, e *stubEvents) *BuildActiveOrderList {
	return &BuildActiveOrderList{
		Graph:  g,
		Format: &FormatInitialOrderData{LLM: l},
		Repo:   r,
		Events: e,
	}
}

func testInput() BuildInput {
	return BuildInput{
		SyncID:     uuid.New(),
		AccountID:  uuid.New(),
		UserID:     uuid.New(),
		GraphToken: "token",
		Since:      time.Now().AddDate(0, -1, 0),
	}
}

// --- tests -------------------------------------------------------------------

func TestBuildActiveOrderList_FiltersNonOrderThreads(t *testing.T) {
	now := time.Now()
	graph := &stubGraph{msgs: []entity.EmailMessage{
		msg("conv-order", "supplier@acmesteel.com", "PO 4471 confirmed", now),
		msg("conv-spam", "news@letter.com", "Weekly digest", now),
	}}
	llm := &stubLLM{
		filterResult: []string{"conv-order"}, // the LLM keeps only the order thread
		extractions: map[string]entity.OrderExtraction{
			"conv-order": {CompanyName: "Acme Steel", OrderID: 4471, Status: entity.OrderStatusConfirmed},
		},
	}
	repo := newStubRepo()
	events := &stubEvents{}

	out, err := buildUsecase(graph, llm, repo, events).Execute(context.Background(), testInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ConversationID != "conv-order" {
		t.Fatalf("expected only conv-order to survive, got %+v", out)
	}
	if llm.extractCalls != 1 {
		t.Fatalf("expected 1 extract call (filtered threads must not be extracted), got %d", llm.extractCalls)
	}
	if len(repo.rows) != 1 {
		t.Fatalf("expected 1 row upserted, got %d", len(repo.rows))
	}
}

func TestBuildActiveOrderList_UpsertIsIdempotent(t *testing.T) {
	now := time.Now()
	graph := &stubGraph{msgs: []entity.EmailMessage{
		msg("conv-1", "a@supplier.com", "Order update", now),
	}}
	llm := &stubLLM{
		filterResult: []string{"conv-1"},
		extractions: map[string]entity.OrderExtraction{
			"conv-1": {CompanyName: "Delmar Tooling", OrderID: 4471, Status: entity.OrderStatusInTransit},
		},
	}
	repo := newStubRepo()
	events := &stubEvents{}
	uc := buildUsecase(graph, llm, repo, events)
	in := testInput()

	for range 2 { // run the sync twice — second run must not double-seed
		if _, err := uc.Execute(context.Background(), in); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if len(repo.rows) != 1 {
		t.Fatalf("expected 1 distinct row after two syncs, got %d", len(repo.rows))
	}
	if repo.upserts != 2 {
		t.Fatalf("expected 2 upsert calls, got %d", repo.upserts)
	}
}

func TestBuildActiveOrderList_LLMErrorMeansNoPartialWrites(t *testing.T) {
	now := time.Now()
	graph := &stubGraph{msgs: []entity.EmailMessage{
		msg("conv-1", "a@supplier.com", "Order A", now),
		msg("conv-2", "b@supplier.com", "Order B", now),
	}}
	llm := &stubLLM{
		filterResult: []string{"conv-1", "conv-2"},
		extractErr:   errors.New("model unavailable"),
	}
	repo := newStubRepo()
	events := &stubEvents{}

	_, err := buildUsecase(graph, llm, repo, events).Execute(context.Background(), testInput())
	if err == nil {
		t.Fatal("expected an error")
	}
	if repo.upserts != 0 {
		t.Fatalf("LLM error must leave the orders table untouched, got %d upserts", repo.upserts)
	}
	if len(events.events) != 0 {
		t.Fatalf("no event should be published on failure, got %v", events.events)
	}
}

func TestBuildActiveOrderList_PublishesSyncCompleted(t *testing.T) {
	now := time.Now()
	graph := &stubGraph{msgs: []entity.EmailMessage{
		msg("conv-1", "a@supplier.com", "Order A", now),
	}}
	llm := &stubLLM{
		filterResult: []string{"conv-1"},
		extractions: map[string]entity.OrderExtraction{
			"conv-1": {CompanyName: "Acme", Status: entity.OrderStatusConfirmed},
		},
	}
	repo := newStubRepo()
	events := &stubEvents{}
	in := testInput()

	if _, err := buildUsecase(graph, llm, repo, events).Execute(context.Background(), in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(events.events) != 1 || events.events[0].Type != entity.EventSyncCompleted {
		t.Fatalf("expected one sync.completed event, got %v", events.events)
	}
	data, ok := events.events[0].Data.(entity.SyncCompleted)
	if !ok || data.SyncID != in.SyncID || data.OrdersUpserted != 1 {
		t.Fatalf("unexpected event payload: %+v", events.events[0].Data)
	}
}

func TestBuildActiveOrderList_StampsOwnershipFromInput(t *testing.T) {
	now := time.Now()
	graph := &stubGraph{msgs: []entity.EmailMessage{
		msg("conv-1", "a@supplier.com", "Order A", now),
	}}
	llm := &stubLLM{
		filterResult: []string{"conv-1"},
		extractions: map[string]entity.OrderExtraction{
			"conv-1": {CompanyName: "Acme", Status: entity.OrderStatusConfirmed},
		},
	}
	repo := newStubRepo()
	in := testInput()

	out, err := buildUsecase(graph, llm, repo, &stubEvents{}).Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].AccountID != in.AccountID || out[0].UserID != in.UserID {
		t.Fatalf("order must carry the auth context's account/user ids, got %+v", out[0])
	}
}
