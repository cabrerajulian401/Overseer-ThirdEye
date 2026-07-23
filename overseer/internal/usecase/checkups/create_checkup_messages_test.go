package checkups

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

func createInput() CreateInput {
	return CreateInput{
		AccountID:      uuid.New(),
		UserID:         uuid.New(),
		ConversationID: "conv-1",
		CompanyName:    "Delmar Tooling",
		EmailAddress:   "dana@delmar.com",
		OrderID:        4471,
		Subject:        "Follow-up on PO 4471",
		Body:           "Hi Dana, any update on PO 4471?",
		SendDate:       time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC),
		CancelIfReply:  true,
		RepeatFlow:     3,
		RepeatEvery:    7 * 24 * time.Hour,
	}
}

func TestCreateExpandsCadenceIntoRows(t *testing.T) {
	repo := testutil.NewInMemCheckups()
	uc := &CreateCheckupMessages{Repo: repo}
	in := createInput()

	stored, err := uc.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("rows = %d, want 3", len(stored))
	}

	flow := stored[0].FlowID
	if flow == uuid.Nil {
		t.Fatal("flow id not minted")
	}
	for i, m := range stored {
		if m.FlowID != flow {
			t.Errorf("row %d flow = %s, want shared %s", i, m.FlowID, flow)
		}
		if m.ID == uuid.Nil {
			t.Errorf("row %d id not minted", i)
		}
		if m.Status != entity.CheckupPending {
			t.Errorf("row %d status = %q, want pending", i, m.Status)
		}
		want := in.SendDate.Add(time.Duration(i) * in.RepeatEvery)
		if !m.SendDate.Equal(want) {
			t.Errorf("row %d send date = %v, want %v", i, m.SendDate, want)
		}
		if m.UserID != in.UserID || m.AccountID != in.AccountID {
			t.Errorf("row %d owner = %s/%s, want input's", i, m.AccountID, m.UserID)
		}
		if !m.CancelIfReply || m.RepeatFlow != 3 || m.ConversationID != "conv-1" {
			t.Errorf("row %d payload = %+v", i, m)
		}
	}
}

func TestCreateSingleMessageFlow(t *testing.T) {
	repo := testutil.NewInMemCheckups()
	uc := &CreateCheckupMessages{Repo: repo}

	for _, repeat := range []int{0, 1} {
		in := createInput()
		in.RepeatFlow = repeat
		in.RepeatEvery = 0

		stored, err := uc.Execute(context.Background(), in)
		if err != nil {
			t.Fatalf("RepeatFlow=%d: %v", repeat, err)
		}
		if len(stored) != 1 {
			t.Errorf("RepeatFlow=%d rows = %d, want 1", repeat, len(stored))
		}
		if !stored[0].SendDate.Equal(in.SendDate) {
			t.Errorf("send date = %v, want %v", stored[0].SendDate, in.SendDate)
		}
	}
}

func TestCreateRejectsRepeatWithoutInterval(t *testing.T) {
	repo := testutil.NewInMemCheckups()
	uc := &CreateCheckupMessages{Repo: repo}
	in := createInput()
	in.RepeatEvery = 0 // 3 messages but no gap

	if _, err := uc.Execute(context.Background(), in); err == nil {
		t.Fatal("expected error, got nil")
	}
	if repo.Count() != 0 {
		t.Errorf("rows leaked on invalid input: %d", repo.Count())
	}
}
