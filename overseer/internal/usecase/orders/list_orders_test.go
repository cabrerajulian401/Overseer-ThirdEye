package orders

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// listStubRepo returns canned pages and records the query it saw.
type listStubRepo struct {
	rows []entity.Order

	gotUserID uuid.UUID
	gotStatus *entity.OrderStatus
	gotBefore *time.Time
	gotLimit  int
}

func (s *listStubRepo) Upsert(_ context.Context, _ entity.Order) error { return nil }

func (s *listStubRepo) List(_ context.Context, userID uuid.UUID, status *entity.OrderStatus, before *time.Time, limit int) ([]entity.Order, error) {
	s.gotUserID, s.gotStatus, s.gotBefore, s.gotLimit = userID, status, before, limit
	if len(s.rows) > limit {
		return s.rows[:limit], nil
	}
	return s.rows, nil
}

func ordersCreatedAt(times ...time.Time) []entity.Order {
	out := make([]entity.Order, len(times))
	for i, tm := range times {
		out[i] = entity.Order{ID: uuid.New(), CreatedAt: tm}
	}
	return out
}

func TestListOrders_NoMorePagesMeansNilCursor(t *testing.T) {
	repo := &listStubRepo{rows: ordersCreatedAt(time.Now())}
	uc := &ListOrders{Repo: repo}

	out, err := uc.Execute(context.Background(), ListInput{UserID: uuid.New(), Limit: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.NextCursor != nil {
		t.Fatalf("expected nil cursor when fewer than limit rows, got %v", out.NextCursor)
	}
	if repo.gotLimit != 6 {
		t.Fatalf("usecase must over-fetch by one to detect more pages, got limit %d", repo.gotLimit)
	}
}

func TestListOrders_FullPageYieldsCursorOfLastRow(t *testing.T) {
	base := time.Now()
	// 3 rows returned for limit 2 → page is rows[:2], cursor = rows[1].CreatedAt
	repo := &listStubRepo{rows: ordersCreatedAt(base, base.Add(-time.Minute), base.Add(-2*time.Minute))}
	uc := &ListOrders{Repo: repo}

	out, err := uc.Execute(context.Background(), ListInput{UserID: uuid.New(), Limit: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Orders) != 2 {
		t.Fatalf("expected page of 2, got %d", len(out.Orders))
	}
	if out.NextCursor == nil || !out.NextCursor.Equal(base.Add(-time.Minute)) {
		t.Fatalf("cursor must be the last page row's created_at, got %v", out.NextCursor)
	}
}

func TestListOrders_PassesFiltersThrough(t *testing.T) {
	repo := &listStubRepo{}
	uc := &ListOrders{Repo: repo}
	status := entity.OrderStatusInTransit
	cursor := time.Now().Add(-time.Hour)
	userID := uuid.New()

	if _, err := uc.Execute(context.Background(), ListInput{
		UserID: userID, Status: &status, Cursor: &cursor, Limit: 10,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.gotUserID != userID {
		t.Fatalf("user filter not passed through")
	}
	if repo.gotStatus == nil || *repo.gotStatus != status {
		t.Fatalf("status filter not passed through")
	}
	if repo.gotBefore == nil || !repo.gotBefore.Equal(cursor) {
		t.Fatalf("cursor not passed through")
	}
}

func TestListOrders_DefaultAndMaxLimit(t *testing.T) {
	repo := &listStubRepo{}
	uc := &ListOrders{Repo: repo}

	if _, err := uc.Execute(context.Background(), ListInput{UserID: uuid.New()}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.gotLimit != defaultPageSize+1 {
		t.Fatalf("expected default limit %d(+1), got %d", defaultPageSize, repo.gotLimit)
	}

	if _, err := uc.Execute(context.Background(), ListInput{UserID: uuid.New(), Limit: 10_000}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.gotLimit != maxPageSize+1 {
		t.Fatalf("expected capped limit %d(+1), got %d", maxPageSize, repo.gotLimit)
	}
}
