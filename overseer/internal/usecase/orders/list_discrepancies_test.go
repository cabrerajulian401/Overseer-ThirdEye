package orders

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

func TestListDiscrepanciesFiltersByStatusAndOwner(t *testing.T) {
	repo := testutil.NewInMemDiscrepancies()
	userID := uuid.New()
	seed := func(owner uuid.UUID, status entity.DiscrepancyStatus, title string) {
		if _, err := repo.Insert(context.Background(), entity.Discrepancy{
			AccountID: uuid.New(), UserID: owner, ConversationID: "conv-1",
			Status: status, CompanyName: "Delmar", Priority: entity.PriorityLow, Title: title,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed(userID, entity.DiscrepancyOpen, "open one")
	seed(userID, entity.DiscrepancyResolved, "resolved one")
	seed(uuid.New(), entity.DiscrepancyOpen, "someone else's")

	uc := &ListDiscrepancies{Repo: repo}

	all, err := uc.Execute(context.Background(), ListDiscrepanciesInput{UserID: userID})
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %d rows, %v; want 2, nil", len(all), err)
	}

	open := entity.DiscrepancyOpen
	onlyOpen, err := uc.Execute(context.Background(), ListDiscrepanciesInput{UserID: userID, Status: &open})
	if err != nil || len(onlyOpen) != 1 || onlyOpen[0].Title != "open one" {
		t.Fatalf("open = %+v, %v; want just the open row", onlyOpen, err)
	}
}
