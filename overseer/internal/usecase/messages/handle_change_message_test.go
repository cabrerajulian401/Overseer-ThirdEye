package messages

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

type fixture struct {
	uc     *HandleChangeMessage
	user   entity.User
	graph  *testutil.FakeGraph
	llm    *testutil.FakeLLM
	repo   *testutil.InMemOrders
	subs   *testutil.InMemSubscriptions
	tokens *testutil.FakeTokens
	events *testutil.EventRecorder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	user := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "op@distributor.com"}

	graph := &testutil.FakeGraph{MessagesByID: map[string]entity.EmailMessage{}}
	llm := &testutil.FakeLLM{Extractions: map[string]entity.OrderExtraction{}}
	repo := testutil.NewInMemOrders()
	subs := testutil.NewInMemSubscriptions()
	tokens := testutil.NewFakeTokens()
	events := &testutil.EventRecorder{}

	subs.AddUser(user)
	if err := subs.UpsertForUser(context.Background(), user.ID, "sub-1", time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	tokens.Put(user.ID, "graph-tok")

	return &fixture{
		uc: &HandleChangeMessage{
			Subs:   subs,
			Tokens: tokens,
			Latest: &GetLatestMessage{Graph: graph},
			LLM:    llm,
			Repo:   repo,
			Events: events,
		},
		user: user, graph: graph, llm: llm, repo: repo, subs: subs, tokens: tokens, events: events,
	}
}

func date(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestNewConversationCreatesOrder(t *testing.T) {
	f := newFixture(t)
	f.graph.MessagesByID["msg-1"] = entity.EmailMessage{
		ID: "msg-1", ConversationID: "conv-new", From: "dana@delmar.com",
		FromName: "Dana Delmar", Subject: "PO 4471 confirmed",
		WebLink: "https://outlook.example/msg-1", CreatedDateTime: time.Now(),
	}
	f.llm.FilterResult = []string{"conv-new"} // classified as a goods order
	f.llm.Extractions["conv-new"] = entity.OrderExtraction{
		CompanyName: "Delmar Tooling", OrderID: 4471,
		Status: entity.OrderStatusConfirmed, CurrentEstDate: date(2026, 7, 12),
	}

	outcome, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-1", MessageID: "msg-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome != OutcomeCreated {
		t.Errorf("outcome = %q, want created", outcome)
	}

	saved, found, _ := f.repo.GetByConversation(context.Background(), f.user.ID, "conv-new")
	if !found {
		t.Fatal("order row not created")
	}
	if saved.CompanyName != "Delmar Tooling" || saved.AccountID != f.user.AccountID || saved.UserID != f.user.ID {
		t.Errorf("saved = %+v", saved)
	}

	updates := f.events.OfType(entity.EventOrderUpdated)
	if len(updates) != 1 {
		t.Fatalf("order.updated events = %d, want 1", len(updates))
	}
	if o, ok := updates[0].Data.(entity.Order); !ok || o.ConversationID != "conv-new" {
		t.Errorf("event data = %+v", updates[0].Data)
	}
}

func TestNewConversationNonOrderIsSkipped(t *testing.T) {
	f := newFixture(t)
	f.graph.MessagesByID["msg-junk"] = entity.EmailMessage{
		ID: "msg-junk", ConversationID: "conv-junk", From: "news@saas.com",
		Subject: "Weekly newsletter", CreatedDateTime: time.Now(),
	}
	f.llm.FilterResult = nil // filter keeps nothing

	outcome, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-1", MessageID: "msg-junk"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome != OutcomeSkippedNotOrder {
		t.Errorf("outcome = %q, want skipped_not_order", outcome)
	}
	if f.repo.Count() != 0 {
		t.Errorf("repo rows = %d, want 0", f.repo.Count())
	}
	if len(f.events.OfType(entity.EventOrderUpdated)) != 0 {
		t.Error("junk message published an event")
	}
	if f.llm.ExtractCalls != 0 {
		t.Error("extraction ran on a filtered-out message")
	}
}

func TestExistingConversationMergesNotClobbers(t *testing.T) {
	f := newFixture(t)
	if err := f.repo.Upsert(context.Background(), entity.Order{
		AccountID: f.user.AccountID, UserID: f.user.ID,
		ConversationID: "conv-1", CompanyName: "Delmar Tooling", OrderID: 4471,
		Status:          entity.OrderStatusConfirmed,
		OriginalEstDate: date(2026, 7, 10), CurrentEstDate: date(2026, 7, 10),
		SupportingQuote: "Order confirmed for July 10",
	}); err != nil {
		t.Fatal(err)
	}

	f.graph.MessagesByID["msg-2"] = entity.EmailMessage{
		ID: "msg-2", ConversationID: "conv-1", From: "dana@delmar.com",
		FromName: "Dana Delmar", Subject: "RE: PO 4471 — delay",
		WebLink: "https://outlook.example/msg-2", CreatedDateTime: time.Now(),
	}
	// The new message only mentions the delay: no company, no order id, no
	// original date — merge must keep those from the existing row.
	f.llm.Extractions["conv-1"] = entity.OrderExtraction{
		Status: entity.OrderStatusInTransit, CurrentEstDate: date(2026, 7, 18),
		SupportingQuote: "Pushed to July 18 due to customs",
	}

	outcome, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-1", MessageID: "msg-2"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome != OutcomeUpdated {
		t.Errorf("outcome = %q, want updated", outcome)
	}
	if f.llm.FilterCalls != 0 {
		t.Error("filter ran on a known order conversation")
	}

	saved, _, _ := f.repo.GetByConversation(context.Background(), f.user.ID, "conv-1")
	if saved.CompanyName != "Delmar Tooling" || saved.OrderID != 4471 {
		t.Errorf("identity fields clobbered: %+v", saved)
	}
	if !saved.OriginalEstDate.Equal(*date(2026, 7, 10)) {
		t.Errorf("OriginalEstDate moved: %v", saved.OriginalEstDate)
	}
	if !saved.CurrentEstDate.Equal(*date(2026, 7, 18)) {
		t.Errorf("CurrentEstDate = %v, want 2026-07-18", saved.CurrentEstDate)
	}
	if saved.Status != entity.OrderStatusInTransit {
		t.Errorf("Status = %q", saved.Status)
	}
	if saved.SupportingQuote != "Pushed to July 18 due to customs" {
		t.Errorf("SupportingQuote = %q", saved.SupportingQuote)
	}
	if saved.LastMessenger != "Dana Delmar" || saved.EmailURL != "https://outlook.example/msg-2" {
		t.Errorf("latest-message fields not refreshed: %+v", saved)
	}
}

func TestRedeliveryIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.graph.MessagesByID["msg-1"] = entity.EmailMessage{
		ID: "msg-1", ConversationID: "conv-new", From: "dana@delmar.com",
		FromName: "Dana Delmar", CreatedDateTime: time.Now(),
	}
	f.llm.FilterResult = []string{"conv-new"}
	f.llm.Extractions["conv-new"] = entity.OrderExtraction{
		CompanyName: "Delmar Tooling", OrderID: 4471, Status: entity.OrderStatusConfirmed,
	}

	in := HandleInput{SubscriptionID: "sub-1", MessageID: "msg-1"}
	if _, err := f.uc.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.Execute(context.Background(), in); err != nil { // Graph re-delivers
		t.Fatal(err)
	}
	if f.repo.Count() != 1 {
		t.Errorf("repo rows = %d, want 1 (idempotent upsert)", f.repo.Count())
	}
}

func TestUnknownSubscriptionIsSkipped(t *testing.T) {
	f := newFixture(t)
	outcome, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-unknown", MessageID: "msg-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome != OutcomeSkippedUnknownSubscription {
		t.Errorf("outcome = %q", outcome)
	}
}

func TestMissingTokenErrors(t *testing.T) {
	f := newFixture(t)
	other := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "other@x.com"}
	f.subs.AddUser(other)
	if err := f.subs.UpsertForUser(context.Background(), other.ID, "sub-2", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// no token cached for `other`
	if _, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-2", MessageID: "msg-1"}); err == nil {
		t.Fatal("want error when no Graph token is cached")
	}
}
