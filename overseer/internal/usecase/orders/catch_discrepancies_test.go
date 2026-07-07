package orders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

type auditFixture struct {
	uc     *CatchDiscrepancies
	order  entity.Order
	graph  *testutil.FakeGraph
	llm    *testutil.FakeLLM
	repo   *testutil.InMemDiscrepancies
	events *testutil.EventRecorder
}

func newAuditFixture() *auditFixture {
	graph := &testutil.FakeGraph{ConversationsByID: map[string][]entity.EmailMessage{}}
	llm := &testutil.FakeLLM{Findings: map[string][]entity.DiscrepancyFinding{}}
	repo := testutil.NewInMemDiscrepancies()
	events := &testutil.EventRecorder{}
	return &auditFixture{
		uc: &CatchDiscrepancies{Graph: graph, LLM: llm, Repo: repo, Events: events},
		order: entity.Order{
			ID: uuid.New(), AccountID: uuid.New(), UserID: uuid.New(),
			ConversationID: "conv-1", CompanyName: "Delmar Tooling",
			EmailAddress: "dana@delmar.com", OrderID: 4471,
			Status: entity.OrderStatusConfirmed,
		},
		graph: graph, llm: llm, repo: repo, events: events,
	}
}

func auditMsg(id, link string, daysAgo int) entity.EmailMessage {
	return entity.EmailMessage{
		ID: id, ConversationID: "conv-1", From: "dana@delmar.com",
		FromName: "Dana Delmar", WebLink: link,
		CreatedDateTime: time.Now().AddDate(0, 0, -daysAgo),
	}
}

func priceFinding() entity.DiscrepancyFinding {
	return entity.DiscrepancyFinding{
		Title:    "Price raised $41.00 → $42.50/unit",
		Priority: entity.PriorityHigh,
		Detail: entity.DiscrepancyDetail{
			PreviouslyAgreed: "$41.00/unit", NewMetric: "$42.50/unit", Type: entity.MetricPrice,
		},
		Quotes: []entity.DiscrepancyQuote{{Quote: "we agree on $41.00/unit", MessageLink: "https://outlook.example/m-1"}},
	}
}

func TestFindingStoredAndPublished(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-2", "https://outlook.example/m-2", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{
		auditMsg("m-1", "https://outlook.example/m-1", 3), newMsg,
	}
	f.llm.Findings["m-2"] = []entity.DiscrepancyFinding{priceFinding()}

	stored, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(stored) != 1 || f.repo.Count() != 1 {
		t.Fatalf("stored = %d, repo rows = %d; want 1, 1", len(stored), f.repo.Count())
	}

	d := stored[0]
	if d.ID == uuid.Nil {
		t.Error("stored row has no server-minted id")
	}
	if d.AccountID != f.order.AccountID || d.UserID != f.order.UserID ||
		d.ConversationID != "conv-1" || d.CompanyName != "Delmar Tooling" ||
		d.EmailAddress != "dana@delmar.com" || d.OrderID != 4471 {
		t.Errorf("ownership/identity not stamped from order: %+v", d)
	}
	if d.Status != entity.DiscrepancyOpen {
		t.Errorf("status = %q, want open", d.Status)
	}
	if d.EmailDeeplinkURL != "https://outlook.example/m-2" {
		t.Errorf("deeplink = %q, want the new message's webLink", d.EmailDeeplinkURL)
	}
	if d.Title != "Price raised $41.00 → $42.50/unit" || d.Priority != entity.PriorityHigh {
		t.Errorf("finding fields lost: %+v", d)
	}

	found := f.events.OfType(entity.EventDiscrepancyFound)
	if len(found) != 1 {
		t.Fatalf("discrepancy.found events = %d, want 1", len(found))
	}
	if ev, ok := found[0].Data.(entity.Discrepancy); !ok || ev.ID != d.ID {
		t.Errorf("event carries %+v, want the stored row", found[0].Data)
	}
}

func TestCleanThreadStoresNothing(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-2", "https://outlook.example/m-2", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{
		auditMsg("m-1", "https://outlook.example/m-1", 3), newMsg,
	}
	// no findings configured for m-2 → LLM returns nil

	stored, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(stored) != 0 || f.repo.Count() != 0 {
		t.Errorf("clean thread stored %d rows", f.repo.Count())
	}
	if len(f.events.OfType(entity.EventDiscrepancyFound)) != 0 {
		t.Error("clean thread published an event")
	}
	if f.llm.FindCalls != 1 {
		t.Errorf("FindCalls = %d, want 1", f.llm.FindCalls)
	}
}

func TestFirstMessageSkipsAudit(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-1", "https://outlook.example/m-1", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{newMsg} // no history

	stored, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(stored) != 0 || f.llm.FindCalls != 0 {
		t.Errorf("first message: stored=%d FindCalls=%d, want 0, 0", len(stored), f.llm.FindCalls)
	}
}

func TestNewMessageExcludedFromHistory(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-3", "https://outlook.example/m-3", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{
		auditMsg("m-1", "https://outlook.example/m-1", 5),
		auditMsg("m-2", "https://outlook.example/m-2", 2),
		newMsg,
	}

	if _, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(f.llm.HistoriesSeen) != 1 {
		t.Fatalf("FindDiscrepancies calls = %d, want 1", len(f.llm.HistoriesSeen))
	}
	got := f.llm.HistoriesSeen[0]
	if len(got) != 2 || got[0] != "m-1" || got[1] != "m-2" {
		t.Errorf("history = %v, want [m-1 m-2] (new message excluded)", got)
	}
}

func TestLLMErrorStoresNothing(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-2", "https://outlook.example/m-2", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{
		auditMsg("m-1", "https://outlook.example/m-1", 3), newMsg,
	}
	f.llm.FindErr = errors.New("model 503")

	if _, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg); err == nil {
		t.Fatal("want error from LLM failure")
	}
	if f.repo.Count() != 0 || len(f.events.Events) != 0 {
		t.Error("LLM failure left partial writes or events")
	}
}

func TestGraphErrorSurfaces(t *testing.T) {
	f := newAuditFixture()
	f.graph.ListConvErr = errors.New("graph 503")

	if _, err := f.uc.Execute(context.Background(), "tok", f.order, auditMsg("m-2", "", 0)); err == nil {
		t.Fatal("want error from Graph failure")
	}
	if f.llm.FindCalls != 0 {
		t.Error("LLM called despite Graph failure")
	}
}
