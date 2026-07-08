package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
	"github.com/thirdeye/overseer/internal/usecase/orders"
)

// env is a fully wired test server: real router, middleware, controllers,
// and usecases — only the outermost adapters (Graph, LLM, repo, events) are
// fakes.
type env struct {
	srv    *httptest.Server
	user   entity.User
	graph  *testutil.FakeGraph
	llm    *testutil.FakeLLM
	repo   *testutil.InMemOrders
	discs  *testutil.InMemDiscrepancies
	events *testutil.EventRecorder
}

func newEnv(t *testing.T) *env {
	t.Helper()

	user := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "op@distributor.com"}
	graph := &testutil.FakeGraph{}
	llm := &testutil.FakeLLM{}
	repo := testutil.NewInMemOrders()
	discs := testutil.NewInMemDiscrepancies()
	events := &testutil.EventRecorder{}
	log := slog.New(slog.DiscardHandler)

	format := &orders.FormatInitialOrderData{LLM: llm}
	controller := &OrdersController{
		List:         &orders.ListOrders{Repo: repo},
		Build:        &orders.BuildActiveOrderList{Graph: graph, Format: format, Repo: repo, Events: events},
		SyncLookback: func() time.Time { return time.Now().AddDate(0, -1, 0) },
		Dispatch:     func(fn func()) { fn() }, // synchronous: no goroutine races in tests
		Log:          log,
	}

	e := NewRouter(RouterDeps{
		Resolver: testutil.StaticResolver{User: user},
		Orders:   controller,
		Discrepancies: &DiscrepanciesController{
			List:   &orders.ListDiscrepancies{Repo: discs},
			Update: &orders.UpdateDiscrepancyStatus{Repo: discs},
			Log:    log,
		},
		WS: noopWS{},
	})

	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return &env{srv: srv, user: user, graph: graph, llm: llm, repo: repo, discs: discs, events: events}
}

type noopWS struct{}

func (noopWS) Handler(c echo.Context) error { return c.NoContent(http.StatusOK) }

func (e *env) request(t *testing.T, method, path string, authed bool) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authed {
		req.Header.Set("Authorization", "Bearer graph-token-abc")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body
}

func (e *env) seedOrder(t *testing.T, conversationID string, status entity.OrderStatus) {
	t.Helper()
	err := e.repo.Upsert(context.Background(), entity.Order{
		AccountID:      e.user.AccountID,
		UserID:         e.user.ID,
		ConversationID: conversationID,
		CompanyName:    "Delmar Tooling",
		OrderID:        4471,
		Status:         status,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// --- GET /orders ---------------------------------------------------------------

func TestGetOrders_E2E_Returns200CamelCase(t *testing.T) {
	e := newEnv(t)
	e.seedOrder(t, "conv-1", entity.OrderStatusInTransit)

	resp, body := e.request(t, http.MethodGet, "/orders", true)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	list, ok := body["orders"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("expected 1 order, got %v", body)
	}
	first := list[0].(map[string]any)
	// camelCase on the wire
	for _, key := range []string{"companyName", "orderId", "status", "dateCreated", "conversationId"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("missing camelCase key %q in %v", key, first)
		}
	}
	if first["companyName"] != "Delmar Tooling" || first["orderId"] != float64(4471) {
		t.Fatalf("unexpected order payload: %v", first)
	}
	if cursor, present := body["nextCursor"]; !present || cursor != nil {
		t.Fatalf("expected nextCursor to be explicit null, got %v (present=%v)", cursor, present)
	}
}

func TestGetOrders_E2E_StatusFilter(t *testing.T) {
	e := newEnv(t)
	e.seedOrder(t, "conv-1", entity.OrderStatusInTransit)
	e.seedOrder(t, "conv-2", entity.OrderStatusDelivered)

	resp, body := e.request(t, http.MethodGet, "/orders?status=delivered", true)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	list := body["orders"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 delivered order, got %d", len(list))
	}
	if list[0].(map[string]any)["status"] != "delivered" {
		t.Fatalf("wrong order returned: %v", list[0])
	}
}

func TestGetOrders_E2E_InvalidStatusRejected(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.request(t, http.MethodGet, "/orders?status=teleported", true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetOrders_E2E_Pagination(t *testing.T) {
	e := newEnv(t)
	for _, conv := range []string{"conv-1", "conv-2", "conv-3"} {
		e.seedOrder(t, conv, entity.OrderStatusConfirmed)
	}

	resp, body := e.request(t, http.MethodGet, "/orders?limit=2", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(body["orders"].([]any)) != 2 {
		t.Fatalf("expected first page of 2, got %v", body)
	}
	cursor, _ := body["nextCursor"].(string)
	if cursor == "" {
		t.Fatalf("expected a nextCursor on a full page, got %v", body["nextCursor"])
	}

	resp2, body2 := e.request(t, http.MethodGet, "/orders?limit=2&cursor="+cursor, true)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	if len(body2["orders"].([]any)) != 1 {
		t.Fatalf("expected second page of 1, got %v", body2)
	}
	if body2["nextCursor"] != nil {
		t.Fatalf("expected nil cursor on last page, got %v", body2["nextCursor"])
	}
}

func TestGetOrders_E2E_NoTokenIs401(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.request(t, http.MethodGet, "/orders", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a bearer token, got %d", resp.StatusCode)
	}
}

// --- POST /sync ------------------------------------------------------------------

func syncFixtures(e *env) {
	now := time.Now()
	e.graph.Messages = []entity.EmailMessage{
		{ID: "m1", ConversationID: "conv-order", From: "maria@delmar.com", FromName: "Maria",
			Subject: "PO 4471", Body: "Your order shipped", CreatedDateTime: now},
		{ID: "m2", ConversationID: "conv-spam", From: "news@letter.com", FromName: "News",
			Subject: "Digest", Body: "Buy now", CreatedDateTime: now},
	}
	e.llm.FilterResult = []string{"conv-order"}
	e.llm.Extractions = map[string]entity.OrderExtraction{
		"conv-order": {CompanyName: "Delmar Tooling", OrderID: 4471, Status: entity.OrderStatusInTransit},
	}
}

func TestPostSync_E2E_Returns202AndSeedsOrders(t *testing.T) {
	e := newEnv(t)
	syncFixtures(e)

	resp, body := e.request(t, http.MethodPost, "/sync", true)

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	syncID, _ := body["syncId"].(string)
	if _, err := uuid.Parse(syncID); err != nil {
		t.Fatalf("expected a server-minted syncId, got %v", body)
	}

	// Side effects the fakes recorded — what WOULD have hit Graph/LLM/DB.
	if got := e.graph.TokensSeen; len(got) != 1 || got[0] != "graph-token-abc" {
		t.Fatalf("Graph must be called with the caller's bearer token, got %v", got)
	}
	if e.llm.FilterCalls != 1 || e.llm.ExtractCalls != 1 {
		t.Fatalf("expected 1 filter + 1 extract call, got %d/%d", e.llm.FilterCalls, e.llm.ExtractCalls)
	}
	if e.repo.Count() != 1 {
		t.Fatalf("expected 1 order seeded, got %d", e.repo.Count())
	}

	// Completion event recorded on the realtime channel.
	completed := e.events.OfType(entity.EventSyncCompleted)
	if len(completed) != 1 {
		t.Fatalf("expected one sync.completed event, got %v", e.events.Events)
	}
	data := completed[0].Data.(entity.SyncCompleted)
	if data.SyncID.String() != syncID || data.OrdersUpserted != 1 {
		t.Fatalf("event payload mismatch: %+v vs syncId %s", data, syncID)
	}
}

func TestPostSync_E2E_SecondCallDoesNotDoubleSeed(t *testing.T) {
	e := newEnv(t)
	syncFixtures(e)

	for range 2 {
		resp, _ := e.request(t, http.MethodPost, "/sync", true)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202, got %d", resp.StatusCode)
		}
	}

	if e.repo.Count() != 1 {
		t.Fatalf("second sync must not double-seed: got %d rows", e.repo.Count())
	}
}

func TestPostSync_E2E_OwnershipComesFromToken(t *testing.T) {
	e := newEnv(t)
	syncFixtures(e)

	// The route takes no body; ownership can only come from auth context.
	e.request(t, http.MethodPost, "/sync", true)

	rows := e.repo.All()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].UserID != e.user.ID || rows[0].AccountID != e.user.AccountID {
		t.Fatalf("row ownership must come from the token's user, got %+v", rows[0])
	}
}

func TestPostSync_E2E_NoTokenIs401(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.request(t, http.MethodPost, "/sync", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	if e.graph.TokensSeen != nil {
		t.Fatalf("Graph must not be touched on auth failure")
	}
}
