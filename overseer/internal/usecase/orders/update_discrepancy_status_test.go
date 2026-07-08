package orders

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

func TestUpdateDiscrepancyStatus(t *testing.T) {
	repo := testutil.NewInMemDiscrepancies()
	userID := uuid.New()
	stored, err := repo.Insert(context.Background(), entity.Discrepancy{
		AccountID: uuid.New(), UserID: userID, ConversationID: "conv-1",
		Status: entity.DiscrepancyOpen, CompanyName: "Delmar",
		Priority: entity.PriorityHigh, Title: "Price raised",
	})
	if err != nil {
		t.Fatal(err)
	}

	uc := &UpdateDiscrepancyStatus{Repo: repo}

	got, err := uc.Execute(context.Background(), userID, stored.ID, entity.DiscrepancyResolved)
	if err != nil || got.Status != entity.DiscrepancyResolved {
		t.Fatalf("Execute = %+v, %v; want resolved row", got, err)
	}

	if _, err := uc.Execute(context.Background(), userID, uuid.New(), entity.DiscrepancyDismissed); !errors.Is(err, ErrDiscrepancyNotFound) {
		t.Fatalf("unknown id err = %v, want ErrDiscrepancyNotFound", err)
	}

	if _, err := uc.Execute(context.Background(), uuid.New(), stored.ID, entity.DiscrepancyDismissed); !errors.Is(err, ErrDiscrepancyNotFound) {
		t.Fatalf("other user's row err = %v, want ErrDiscrepancyNotFound", err)
	}

	if _, err := uc.Execute(context.Background(), userID, stored.ID, "archived"); err == nil {
		t.Fatal("invalid status accepted")
	}
}
