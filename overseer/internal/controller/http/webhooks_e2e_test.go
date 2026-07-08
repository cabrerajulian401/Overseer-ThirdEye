package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
	"github.com/thirdeye/overseer/internal/usecase/messages"
	"github.com/thirdeye/overseer/internal/usecase/orders"
	"github.com/thirdeye/overseer/internal/usecase/subscriptions"
)

// webhookEnv extends env to the post-sync surface: real router + middleware +
// controllers + usecases, fake adapters — including the webhook pipeline.
type webhookEnv struct {
	*env
	subs   *testutil.InMemSubscriptions
	tokens *testutil.FakeTokens
}

func newWebhookEnv(t *testing.T) *webhookEnv {
	t.Helper()

	user := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "op@distributor.com"}
	graph := &testutil.FakeGraph{
		MessagesByID:      map[string]entity.EmailMessage{},
		ConversationsByID: map[string][]entity.EmailMessage{},
	}
	llm := &testutil.FakeLLM{
		Extractions: map[string]entity.OrderExtraction{},
		Findings:    map[string][]entity.DiscrepancyFinding{},
	}
	repo := testutil.NewInMemOrders()
	discs := testutil.NewInMemDiscrepancies()
	events := &testutil.EventRecorder{}
	subs := testutil.NewInMemSubscriptions()
	tokens := testutil.NewFakeTokens()
	log := slog.New(slog.DiscardHandler)

	subs.AddUser(user)
	if err := subs.UpsertForUser(context.Background(), user.ID, "sub-1", time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	tokens.Put(user.ID, "graph-token-abc")

	handle := &messages.HandleChangeMessage{
		Subs: subs, Tokens: tokens,
		Latest: &messages.GetLatestMessage{Graph: graph},
		LLM:    llm, Repo: repo, Events: events,
		Audit: &orders.CatchDiscrepancies{Graph: graph, LLM: llm, Repo: discs, Events: events},
	}
	ensure := &subscriptions.EnsureSubscription{
		Graph: graph, Repo: subs,
		NotificationURL: "https://app.example/webhooks/graph",
		ClientState:     "test-client-state",
		TTL:             48 * time.Hour,
		Now:             time.Now,
	}

	format := &orders.FormatInitialOrderData{LLM: llm}
	controller := &OrdersController{
		List:         &orders.ListOrders{Repo: repo},
		Build:        &orders.BuildActiveOrderList{Graph: graph, Format: format, Repo: repo, Events: events},
		SyncLookback: func() time.Time { return time.Now().AddDate(0, -1, 0) },
		Dispatch:     func(fn func()) { fn() },
		Log:          log,
	}

	e := NewRouter(RouterDeps{
		Resolver: testutil.StaticResolver{User: user},
		Tokens:   tokens,
		Orders:   controller,
		Webhooks: &WebhooksController{
			Handle:      handle,
			ClientState: "test-client-state",
			Dispatch:    func(fn func()) { fn() },
			Log:         log,
		},
		Subscriptions: &SubscriptionsController{Ensure: ensure, Log: log},
		WS:            noopWS{},
	})

	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return &webhookEnv{
		env:    &env{srv: srv, user: user, graph: graph, llm: llm, repo: repo, discs: discs, events: events},
		subs:   subs,
		tokens: tokens,
	}
}

func (e *webhookEnv) postJSON(t *testing.T, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(e.srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestWebhookValidationHandshake(t *testing.T) {
	e := newWebhookEnv(t)

	resp := e.postJSON(t, "/webhooks/graph?validationToken=tok%20with%20space", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "tok with space" { // decoded token echoed back
		t.Errorf("body = %q", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
}

func TestWebhookNotificationUpdatesOrder(t *testing.T) {
	e := newWebhookEnv(t)
	e.graph.MessagesByID["msg-1"] = entity.EmailMessage{
		ID: "msg-1", ConversationID: "conv-new", From: "dana@delmar.com",
		FromName: "Dana Delmar", CreatedDateTime: time.Now(),
	}
	e.llm.FilterResult = []string{"conv-new"}
	e.llm.Extractions["conv-new"] = entity.OrderExtraction{
		CompanyName: "Delmar Tooling", OrderID: 4471, Status: entity.OrderStatusConfirmed,
	}

	resp := e.postJSON(t, "/webhooks/graph", `{
		"value": [{
			"subscriptionId": "sub-1",
			"changeType": "created",
			"clientState": "test-client-state",
			"resourceData": {"id": "msg-1"}
		}]
	}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	if e.repo.Count() != 1 {
		t.Fatalf("orders = %d, want 1", e.repo.Count())
	}
	if len(e.events.OfType(entity.EventOrderUpdated)) != 1 {
		t.Error("no order.updated event published")
	}
}

func TestWebhookNotificationRunsAudit(t *testing.T) {
	e := newWebhookEnv(t)
	// A known order conversation with history.
	if err := e.repo.Upsert(context.Background(), entity.Order{
		AccountID: e.user.AccountID, UserID: e.user.ID,
		ConversationID: "conv-1", CompanyName: "Delmar Tooling",
		EmailAddress: "dana@delmar.com", OrderID: 4471,
		Status: entity.OrderStatusConfirmed,
	}); err != nil {
		t.Fatal(err)
	}
	oldMsg := entity.EmailMessage{
		ID: "msg-1", ConversationID: "conv-1", From: "dana@delmar.com",
		WebLink: "https://outlook.example/msg-1", CreatedDateTime: time.Now().Add(-72 * time.Hour),
	}
	newMsg := entity.EmailMessage{
		ID: "msg-2", ConversationID: "conv-1", From: "dana@delmar.com",
		FromName: "Dana Delmar", WebLink: "https://outlook.example/msg-2",
		CreatedDateTime: time.Now(),
	}
	e.graph.MessagesByID["msg-2"] = newMsg
	e.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{oldMsg, newMsg}
	e.llm.Extractions["conv-1"] = entity.OrderExtraction{Status: entity.OrderStatusIssue}
	e.llm.Findings["msg-2"] = []entity.DiscrepancyFinding{{
		Title: "Price raised $41.00 → $42.50/unit", Priority: entity.PriorityHigh,
		Detail: entity.DiscrepancyDetail{PreviouslyAgreed: "$41.00/unit", NewMetric: "$42.50/unit", Type: entity.MetricPrice},
		Quotes: []entity.DiscrepancyQuote{{Quote: "we agree on $41.00/unit", MessageLink: "https://outlook.example/msg-1"}},
	}}

	resp := e.postJSON(t, "/webhooks/graph", `{
		"value": [{
			"subscriptionId": "sub-1",
			"changeType": "created",
			"clientState": "test-client-state",
			"resourceData": {"id": "msg-2"}
		}]
	}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	if e.discs.Count() != 1 {
		t.Fatalf("discrepancy rows = %d, want 1", e.discs.Count())
	}
	stored := e.discs.All()[0]
	if stored.CompanyName != "Delmar Tooling" || stored.OrderID != 4471 ||
		stored.EmailDeeplinkURL != "https://outlook.example/msg-2" ||
		stored.Status != entity.DiscrepancyOpen {
		t.Errorf("stored = %+v", stored)
	}
	if len(e.events.OfType(entity.EventDiscrepancyFound)) != 1 {
		t.Error("no discrepancy.found event recorded")
	}
	if len(e.events.OfType(entity.EventOrderUpdated)) != 1 {
		t.Error("order.updated missing — audit must not replace the order leg")
	}
}

func TestWebhookRejectsBadClientState(t *testing.T) {
	e := newWebhookEnv(t)

	resp := e.postJSON(t, "/webhooks/graph", `{
		"value": [{
			"subscriptionId": "sub-1",
			"clientState": "attacker-guess",
			"resourceData": {"id": "msg-1"}
		}]
	}`)
	// Still 202 (Graph docs: always ack), but nothing processed.
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if e.repo.Count() != 0 {
		t.Error("spoofed notification was processed")
	}
}

func TestPostSubscriptions(t *testing.T) {
	e := newWebhookEnv(t)
	e.graph.CreatedSubID = "sub-created"

	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/subscriptions", nil)
	req.Header.Set("Authorization", "Bearer graph-token-abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["subscriptionId"] != "sub-created" {
		t.Errorf("body = %v", body)
	}
	if _, ok := body["expiresAt"].(string); !ok {
		t.Errorf("expiresAt missing: %v", body)
	}

	// Unauthed → 401 (the webhook is the only unauthenticated route).
	respNoAuth, err := http.Post(e.srv.URL+"/subscriptions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed status = %d, want 401", respNoAuth.StatusCode)
	}
}
