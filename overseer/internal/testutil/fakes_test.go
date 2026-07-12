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

func TestInMemDiscrepancies(t *testing.T) {
	repo := NewInMemDiscrepancies()
	userID := uuid.New()

	stored, err := repo.Insert(context.Background(), entity.Discrepancy{
		AccountID: uuid.New(), UserID: userID, ConversationID: "conv-1",
		Status: entity.DiscrepancyOpen, CompanyName: "Delmar Tooling",
		Priority: entity.PriorityHigh, Title: "Price raised",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID == uuid.Nil {
		t.Fatal("Insert did not mint an id")
	}

	open := entity.DiscrepancyOpen
	rows, err := repo.ListByUser(context.Background(), userID, &open)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListByUser = %v, %v", rows, err)
	}

	updated, found, err := repo.UpdateStatus(context.Background(), userID, stored.ID, entity.DiscrepancyResolved)
	if err != nil || !found || updated.Status != entity.DiscrepancyResolved {
		t.Fatalf("UpdateStatus = %+v, %v, %v", updated, found, err)
	}
	if rows, _ = repo.ListByUser(context.Background(), userID, &open); len(rows) != 0 {
		t.Fatalf("open rows after resolve = %d, want 0", len(rows))
	}
	if _, found, _ = repo.UpdateStatus(context.Background(), uuid.New(), stored.ID, entity.DiscrepancyOpen); found {
		t.Fatal("another user's update matched the row")
	}
}

func TestInMemOttoScans(t *testing.T) {
	repo := NewInMemOttoScans()
	userID := uuid.New()

	stored, err := repo.Insert(context.Background(), entity.OttoScan{
		AccountID: uuid.New(), UserID: userID,
		MessageID: "msg-1", ConversationID: "conv-1",
		Fields: entity.OrderExtraction{CompanyName: "Delmar Tooling", OrderID: 4471},
		Status: entity.ScanScanned,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID == uuid.Nil {
		t.Fatal("Insert did not mint an id")
	}

	got, found, err := repo.GetByID(context.Background(), userID, stored.ID)
	if err != nil || !found || got.Fields.CompanyName != "Delmar Tooling" {
		t.Fatalf("GetByID = %+v, %v, %v", got, found, err)
	}
	if _, found, _ := repo.GetByID(context.Background(), uuid.New(), stored.ID); found {
		t.Fatal("another user's read matched the row")
	}

	if err := repo.MarkRecorded(context.Background(), userID, stored.ID, "item-1"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = repo.GetByID(context.Background(), userID, stored.ID)
	if got.Status != entity.ScanRecorded || got.SelectedResource != "item-1" {
		t.Errorf("after MarkRecorded: %+v", got)
	}
	if err := repo.MarkRecorded(context.Background(), userID, uuid.New(), "item-1"); err == nil {
		t.Fatal("MarkRecorded on unknown id should error")
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

func TestInMemCheckups(t *testing.T) {
	repo := NewInMemCheckups()
	userID := uuid.New()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	stored, err := repo.InsertFlow(context.Background(), []entity.CheckupMessage{
		{UserID: userID, FlowID: uuid.New(), ConversationID: "conv-1", EmailAddress: "a@x.com",
			Status: entity.CheckupPending, SendDate: now.Add(-time.Minute), CancelIfReply: true},
		{UserID: userID, FlowID: uuid.New(), ConversationID: "conv-1", EmailAddress: "b@x.com",
			Status: entity.CheckupPending, SendDate: now.Add(time.Hour)},
	})
	if err != nil || len(stored) != 2 {
		t.Fatalf("InsertFlow = %v, %v", stored, err)
	}
	if stored[0].ID == uuid.Nil || stored[1].ID == uuid.Nil {
		t.Fatal("InsertFlow did not mint ids")
	}

	// ClaimDue takes only the past-due pending row and flips it to sending.
	due, err := repo.ClaimDue(context.Background(), now)
	if err != nil || len(due) != 1 || due[0].EmailAddress != "a@x.com" {
		t.Fatalf("ClaimDue = %v, %v", due, err)
	}
	if due[0].Status != entity.CheckupSending {
		t.Errorf("claimed status = %q, want sending", due[0].Status)
	}
	// a second claim finds nothing (row is no longer pending)
	if again, _ := repo.ClaimDue(context.Background(), now); len(again) != 0 {
		t.Errorf("second ClaimDue = %v, want empty", again)
	}

	// Release puts it back; MarkSent finishes it.
	if err := repo.Release(context.Background(), due[0].ID); err != nil {
		t.Fatal(err)
	}
	due, _ = repo.ClaimDue(context.Background(), now)
	if len(due) != 1 {
		t.Fatalf("reclaim after release = %v", due)
	}
	if err := repo.MarkSent(context.Background(), due[0].ID); err != nil {
		t.Fatal(err)
	}
	got, found, _ := repo.GetByID(context.Background(), userID, due[0].ID)
	if !found || got.Status != entity.CheckupSent {
		t.Errorf("after MarkSent: found=%v status=%q", found, got.Status)
	}

	// CancelPendingByConversation only hits pending cancel_if_reply rows.
	n, err := repo.CancelPendingByConversation(context.Background(), userID, "conv-1")
	if err != nil || n != 0 { // a@x.com is sent, b@x.com has CancelIfReply=false
		t.Fatalf("cancel = %d, %v (want 0)", n, err)
	}
}

func TestFakeGraphSendMail(t *testing.T) {
	g := &FakeGraph{SendFailTo: "bad@x.com"}
	if err := g.SendMail(context.Background(), "tok", "ok@x.com", "s", "b"); err != nil {
		t.Fatal(err)
	}
	if err := g.SendMail(context.Background(), "tok", "bad@x.com", "s", "b"); err == nil {
		t.Fatal("SendFailTo recipient should error")
	}
	if len(g.SentMails) != 1 || g.SentMails[0].To != "ok@x.com" || g.SentMails[0].Token != "tok" {
		t.Errorf("SentMails = %+v", g.SentMails)
	}
}
