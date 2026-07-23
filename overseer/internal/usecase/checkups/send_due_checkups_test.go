package checkups

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

var fixedNow = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

func newSender(repo *testutil.InMemCheckups, graph *testutil.FakeGraph, tokens *testutil.FakeTokens) *SendDueCheckups {
	return &SendDueCheckups{
		Repo:   repo,
		Mail:   graph,
		Tokens: tokens,
		Now:    func() time.Time { return fixedNow },
		Log:    slog.New(slog.DiscardHandler),
	}
}

func pendingMsg(userID uuid.UUID, to string, sendDate time.Time) entity.CheckupMessage {
	return entity.CheckupMessage{
		UserID: userID, AccountID: uuid.New(), FlowID: uuid.New(),
		EmailAddress: to, Subject: "Follow-up", Body: "Any update?",
		Status: entity.CheckupPending, SendDate: sendDate,
	}
}

func TestSendsOnlyDuePendingRows(t *testing.T) {
	repo := testutil.NewInMemCheckups()
	graph := &testutil.FakeGraph{}
	tokens := testutil.NewFakeTokens()
	userID := uuid.New()
	tokens.Put(userID, "graph-tok")

	due := repo.Seed(pendingMsg(userID, "due@x.com", fixedNow.Add(-time.Minute)))
	repo.Seed(pendingMsg(userID, "future@x.com", fixedNow.Add(time.Hour)))
	paused := pendingMsg(userID, "paused@x.com", fixedNow.Add(-time.Hour))
	paused.Status = entity.CheckupPaused
	repo.Seed(paused)

	sent, err := newSender(repo, graph, tokens).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if sent != 1 {
		t.Errorf("sent = %d, want 1", sent)
	}
	if len(graph.SentMails) != 1 || graph.SentMails[0].To != "due@x.com" || graph.SentMails[0].Token != "graph-tok" {
		t.Errorf("SentMails = %+v", graph.SentMails)
	}
	got, _, _ := repo.GetByID(context.Background(), userID, due.ID)
	if got.Status != entity.CheckupSent {
		t.Errorf("due row status = %q, want sent", got.Status)
	}
}

func TestOneFailureDoesNotStopBatch(t *testing.T) {
	repo := testutil.NewInMemCheckups()
	graph := &testutil.FakeGraph{SendFailTo: "a@x.com"}
	tokens := testutil.NewFakeTokens()
	userID := uuid.New()
	tokens.Put(userID, "graph-tok")

	failing := repo.Seed(pendingMsg(userID, "a@x.com", fixedNow.Add(-2*time.Minute)))
	ok := repo.Seed(pendingMsg(userID, "b@x.com", fixedNow.Add(-time.Minute)))

	sent, err := newSender(repo, graph, tokens).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err) // batch-level: log & continue
	}
	if sent != 1 {
		t.Errorf("sent = %d, want 1", sent)
	}
	if len(graph.SentMails) != 1 || graph.SentMails[0].To != "b@x.com" {
		t.Errorf("b@x.com not attempted: %+v", graph.SentMails)
	}
	gotFail, _, _ := repo.GetByID(context.Background(), userID, failing.ID)
	if gotFail.Status != entity.CheckupPending {
		t.Errorf("failed row = %q, want pending (released for retry)", gotFail.Status)
	}
	gotOK, _, _ := repo.GetByID(context.Background(), userID, ok.ID)
	if gotOK.Status != entity.CheckupSent {
		t.Errorf("ok row = %q, want sent", gotOK.Status)
	}
}

func TestNoTokenReleasesRow(t *testing.T) {
	repo := testutil.NewInMemCheckups()
	graph := &testutil.FakeGraph{}
	tokens := testutil.NewFakeTokens() // empty: no cached token
	userID := uuid.New()

	row := repo.Seed(pendingMsg(userID, "due@x.com", fixedNow.Add(-time.Minute)))

	sent, err := newSender(repo, graph, tokens).Execute(context.Background())
	if err != nil || sent != 0 {
		t.Fatalf("Execute = %d, %v; want 0, nil", sent, err)
	}
	if len(graph.SentMails) != 0 {
		t.Errorf("sent without a token: %+v", graph.SentMails)
	}
	got, _, _ := repo.GetByID(context.Background(), userID, row.ID)
	if got.Status != entity.CheckupPending {
		t.Errorf("row = %q, want pending (released)", got.Status)
	}
}

func TestNothingDueSendsNothing(t *testing.T) {
	repo := testutil.NewInMemCheckups()
	graph := &testutil.FakeGraph{}
	tokens := testutil.NewFakeTokens()

	sent, err := newSender(repo, graph, tokens).Execute(context.Background())
	if err != nil || sent != 0 {
		t.Fatalf("Execute = %d, %v; want 0, nil", sent, err)
	}
	if len(graph.SentMails) != 0 {
		t.Errorf("SentMails = %+v, want none", graph.SentMails)
	}
}
