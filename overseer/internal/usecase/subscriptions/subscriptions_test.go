package subscriptions

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

func TestEnsureSubscription(t *testing.T) {
	graph := &testutil.FakeGraph{CreatedSubID: "sub-abc"}
	repo := testutil.NewInMemSubscriptions()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	uc := &EnsureSubscription{
		Graph:           graph,
		Repo:            repo,
		NotificationURL: "https://app.example/webhooks/graph",
		ClientState:     "secret-state",
		TTL:             48 * time.Hour,
		Now:             func() time.Time { return now },
	}

	userID := uuid.New()
	out, err := uc.Execute(context.Background(), EnsureInput{UserID: userID, GraphToken: "tok"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.GraphSubscriptionID != "sub-abc" {
		t.Errorf("id = %q", out.GraphSubscriptionID)
	}
	if !out.ExpiresAt.Equal(now.Add(48 * time.Hour)) {
		t.Errorf("ExpiresAt = %v", out.ExpiresAt)
	}

	if len(graph.CreateCalls) != 1 {
		t.Fatalf("CreateSubscription calls = %d", len(graph.CreateCalls))
	}
	call := graph.CreateCalls[0]
	if call.NotificationURL != "https://app.example/webhooks/graph" || call.ClientState != "secret-state" || call.Token != "tok" {
		t.Errorf("create call = %+v", call)
	}

	// Row persisted → webhook can resolve it (user registered separately).
	repo.AddUser(entity.User{ID: userID})
	if _, found, _ := repo.ResolveSubscription(context.Background(), "sub-abc"); !found {
		t.Error("subscription row not persisted")
	}
}

func TestRenewDueSubscriptions(t *testing.T) {
	graph := &testutil.FakeGraph{}
	repo := testutil.NewInMemSubscriptions()
	tokens := testutil.NewFakeTokens()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	dueUser, freshUser, tokenlessUser := uuid.New(), uuid.New(), uuid.New()
	// expires within the renew-ahead window → due
	_ = repo.UpsertForUser(context.Background(), dueUser, "sub-due", now.Add(6*time.Hour))
	// expires far out → not due
	_ = repo.UpsertForUser(context.Background(), freshUser, "sub-fresh", now.Add(40*time.Hour))
	// due but no cached token → skipped, not fatal
	_ = repo.UpsertForUser(context.Background(), tokenlessUser, "sub-naked", now.Add(6*time.Hour))
	tokens.Put(dueUser, "tok-due")

	uc := &RenewDueSubscriptions{
		Graph:      graph,
		Repo:       repo,
		Tokens:     tokens,
		TTL:        48 * time.Hour,
		RenewAhead: 12 * time.Hour,
		Now:        func() time.Time { return now },
		Log:        slog.New(slog.DiscardHandler),
	}

	renewed, err := uc.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if renewed != 1 {
		t.Errorf("renewed = %d, want 1", renewed)
	}
	if len(graph.RenewedIDs) != 1 || graph.RenewedIDs[0] != "sub-due" {
		t.Errorf("RenewedIDs = %v", graph.RenewedIDs)
	}

	// sub-due's expiry moved forward, so nothing is due anymore.
	due, _ := repo.ListExpiring(context.Background(), now.Add(12*time.Hour))
	if len(due) != 1 || due[0].GraphSubscriptionID != "sub-naked" {
		t.Errorf("still expiring = %v, want only sub-naked", due)
	}
}
