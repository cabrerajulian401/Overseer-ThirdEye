package checkups

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

func seedWithStatus(repo *testutil.InMemCheckups, userID uuid.UUID, s entity.CheckupStatus) entity.CheckupMessage {
	m := pendingMsg(userID, "dana@delmar.com", time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC))
	m.Status = s
	return repo.Seed(m)
}

func TestUpdateStatusLegalTransitions(t *testing.T) {
	cases := []struct{ from, to entity.CheckupStatus }{
		{entity.CheckupPending, entity.CheckupPaused},
		{entity.CheckupPaused, entity.CheckupPending},
		{entity.CheckupPending, entity.CheckupCancelled},
		{entity.CheckupPaused, entity.CheckupCancelled},
	}
	for _, c := range cases {
		repo := testutil.NewInMemCheckups()
		userID := uuid.New()
		row := seedWithStatus(repo, userID, c.from)
		uc := &UpdateStatus{Repo: repo}

		updated, err := uc.Execute(context.Background(), userID, row.ID, c.to)
		if err != nil {
			t.Errorf("%s → %s: %v", c.from, c.to, err)
			continue
		}
		if updated.Status != c.to {
			t.Errorf("%s → %s: got %q", c.from, c.to, updated.Status)
		}
	}
}

func TestUpdateStatusTerminalRowsConflict(t *testing.T) {
	for _, from := range []entity.CheckupStatus{entity.CheckupSent, entity.CheckupCancelled, entity.CheckupSending} {
		repo := testutil.NewInMemCheckups()
		userID := uuid.New()
		row := seedWithStatus(repo, userID, from)
		uc := &UpdateStatus{Repo: repo}

		_, err := uc.Execute(context.Background(), userID, row.ID, entity.CheckupPaused)
		if !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("from %s: err = %v, want ErrIllegalTransition", from, err)
		}
		got, _, _ := repo.GetByID(context.Background(), userID, row.ID)
		if got.Status != from {
			t.Errorf("from %s: row mutated to %q", from, got.Status)
		}
	}
}

func TestUpdateStatusUnknownOrForeignRow(t *testing.T) {
	repo := testutil.NewInMemCheckups()
	userID := uuid.New()
	uc := &UpdateStatus{Repo: repo}

	if _, err := uc.Execute(context.Background(), userID, uuid.New(), entity.CheckupPaused); !errors.Is(err, ErrCheckupNotFound) {
		t.Errorf("unknown id: err = %v, want ErrCheckupNotFound", err)
	}

	other := seedWithStatus(repo, uuid.New(), entity.CheckupPending) // another user's row
	if _, err := uc.Execute(context.Background(), userID, other.ID, entity.CheckupPaused); !errors.Is(err, ErrCheckupNotFound) {
		t.Errorf("foreign row: err = %v, want ErrCheckupNotFound", err)
	}
}
