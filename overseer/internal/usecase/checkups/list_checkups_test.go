package checkups

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

func TestListCheckupsFiltersAndSorts(t *testing.T) {
	repo := testutil.NewInMemCheckups()
	userID := uuid.New()
	base := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)

	later := pendingMsg(userID, "later@x.com", base.Add(48*time.Hour))
	repo.Seed(later)
	repo.Seed(pendingMsg(userID, "soon@x.com", base))
	sent := pendingMsg(userID, "sent@x.com", base.Add(-time.Hour))
	sent.Status = entity.CheckupSent
	repo.Seed(sent)
	repo.Seed(pendingMsg(uuid.New(), "other-user@x.com", base)) // never visible

	uc := &ListCheckups{Repo: repo}

	all, err := uc.Execute(context.Background(), ListInput{UserID: userID})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all rows = %d, want 3", len(all))
	}
	if all[0].EmailAddress != "sent@x.com" || all[1].EmailAddress != "soon@x.com" || all[2].EmailAddress != "later@x.com" {
		t.Errorf("order = %s, %s, %s; want soonest send_date first", all[0].EmailAddress, all[1].EmailAddress, all[2].EmailAddress)
	}

	pending := entity.CheckupPending
	got, err := uc.Execute(context.Background(), ListInput{UserID: userID, Status: &pending})
	if err != nil || len(got) != 2 {
		t.Fatalf("pending rows = %d (%v), want 2", len(got), err)
	}
}
