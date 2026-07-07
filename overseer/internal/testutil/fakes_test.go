package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

func TestInMemOrdersGetByConversation(t *testing.T) {
	repo := NewInMemOrders()
	userID := uuid.New()
	err := repo.Upsert(context.Background(), entity.Order{
		AccountID: uuid.New(), UserID: userID,
		ConversationID: "conv-1", CompanyName: "Delmar", Status: entity.OrderStatusInTransit,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, found, err := repo.GetByConversation(context.Background(), userID, "conv-1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v, want true,nil", found, err)
	}
	if got.CompanyName != "Delmar" {
		t.Errorf("CompanyName = %q", got.CompanyName)
	}

	_, found, err = repo.GetByConversation(context.Background(), userID, "conv-missing")
	if err != nil || found {
		t.Fatalf("missing row: found=%v err=%v, want false,nil", found, err)
	}
}

func TestInMemSubscriptions(t *testing.T) {
	subs := NewInMemSubscriptions()
	user := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "op@x.com"}
	subs.AddUser(user)

	exp := time.Now().Add(48 * time.Hour)
	if err := subs.UpsertForUser(context.Background(), user.ID, "sub-abc", exp); err != nil {
		t.Fatal(err)
	}

	got, found, err := subs.ResolveSubscription(context.Background(), "sub-abc")
	if err != nil || !found || got.ID != user.ID {
		t.Fatalf("resolve: got=%+v found=%v err=%v", got, found, err)
	}
	if _, found, _ := subs.ResolveSubscription(context.Background(), "sub-nope"); found {
		t.Fatal("resolved unknown subscription")
	}

	due, err := subs.ListExpiring(context.Background(), time.Now().Add(72*time.Hour))
	if err != nil || len(due) != 1 {
		t.Fatalf("ListExpiring = %v, %v", due, err)
	}
	newExp := time.Now().Add(96 * time.Hour)
	if err := subs.UpdateExpiry(context.Background(), "sub-abc", newExp); err != nil {
		t.Fatal(err)
	}
	due, _ = subs.ListExpiring(context.Background(), time.Now().Add(72*time.Hour))
	if len(due) != 0 {
		t.Fatalf("after renewal ListExpiring = %v, want empty", due)
	}
}
