# Order List — Second Phase (Post-Sync) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After the one-time onboarding sync, keep the `orders` table and the UI live as new supplier mail arrives — via Microsoft Graph change notifications (webhooks), an idempotent `HandleChangeMessage` job, and the `order.updated` realtime event.

**Architecture:** Graph POSTs change notifications to `POST /webhooks/graph` (no auth — Graph doesn't send bearer tokens; a shared `clientState` secret authenticates notifications). The controller echoes `validationToken` for subscription handshakes and otherwise enqueues `usecase/messages/HandleChangeMessage` via `Dispatch` and returns 202 immediately. The job resolves the subscription → user, pulls the user's cached Graph token, fetches the message (`gateway/graph.GetMessage`), runs the LLM (filter for new conversations, extract always), merges + upserts into `orders`, and publishes `order.updated` over the shared `/ws` hub. `POST /subscriptions` creates the Graph subscription; an hourly worker renews expiring ones.

**Tech Stack:** Go 1.26, Echo, pgx/dbmate (Postgres), gorilla/websocket, Anthropic SDK (existing `gateway/llm` — no LLM changes needed).

## Global Constraints

- Layering rule (never break this): `controller / worker → usecase → gateway / repo`. Usecases never call controllers; usecases never call an LLM/HTTP API directly — only through gateway ports.
- REST resource-oriented URLs, no verbs in paths: `POST /webhooks/graph`, `POST /subscriptions`.
- One realtime channel: `order.updated` is a new event **type** on the existing `GET /ws` — never a new socket.
- Server mints all IDs; clients never send them.
- Casing: camelCase JSON, snake_case DB columns, PascalCase Go types. Entities carry no JSON/DB tags.
- Long work is async: the webhook handler must never do Graph/LLM work inline — enqueue and return 202.
- Webhook must-dos (from spec): echo `validationToken` with 200 within 10 seconds; `HandleChangeMessage` must be idempotent (Graph re-delivers).
- Ports live where they are consumed (in the usecase package); adapters satisfy them structurally; `cmd/overseer/main.go` pins conformance with compile-time asserts.
- Tests: usecase unit tests with `testutil` fakes + controller E2E over `httptest`. No test touches a real external service (the live-mail run is a separate manual phase).

**Scope notes (deviations & deferrals, decided up front):**
- **Token plumbing:** Graph notifications carry no token, but `HandleChangeMessage` must call Graph. MVP: an in-memory `tokencache` that the auth middleware fills on every authed request (the add-in pings the backend regularly, so the cache stays warm). The `users.graph_refresh_token_enc` OAuth refresh flow is future work.
- **Reply detection for Checkups' `CancelIfReply`:** the spec says `HandleChangeMessage` owns it, but the Checkups feature (tables, usecases) doesn't exist yet. YAGNI — leave a marked extension-point comment where it belongs; do not build it.
- **Filter on new conversations:** the spec's webhook flow never filters, but without it every newsletter becomes an order row. New conversations run `FilterOrderMessages` first; known order conversations skip the filter (already classified at sync).
- **Merge, don't clobber:** a single new message rarely mentions every field; blind upsert would wipe good sync data. Existing rows are merged field-by-field (`OriginalEstDate` never moves once set — it's the *original*).

---

## File Structure

```
overseer/
├── cmd/overseer/main.go                                 MODIFY — config, wiring, renewal worker, asserts
├── db/migrations/20260705000003_graph_subscriptions_unique.sql   CREATE — unique indexes for upserts
└── internal/
    ├── entity/order.go                                  MODIFY — add OrderFromExtraction constructor
    ├── tokencache/tokencache.go                         CREATE — in-memory userID→Graph-token cache
    ├── gateway/graph/graph.go                           MODIFY — GetMessage, CreateSubscription, RenewSubscription
    ├── gateway/graph/graph_test.go                      CREATE — httptest-backed gateway tests
    ├── repo/orders.go                                   MODIFY — GetByConversation: uuid + found-bool (unused today, safe)
    ├── repo/subscriptions.go                            CREATE — graph_subscriptions SQL
    ├── usecase/orders/format_initial_order_data.go      MODIFY — call entity.OrderFromExtraction
    ├── usecase/messages/ports.go                        CREATE — ports for the messages usecases
    ├── usecase/messages/get_latest_message.go           CREATE — spec: usecase/messages/GetLatestMessage
    ├── usecase/messages/handle_change_message.go        CREATE — the webhook job (spec: jobs/HandleChangeMessage)
    ├── usecase/messages/handle_change_message_test.go   CREATE
    ├── usecase/subscriptions/ports.go                   CREATE
    ├── usecase/subscriptions/ensure_subscription.go     CREATE — create Graph sub + persist
    ├── usecase/subscriptions/renew_due_subscriptions.go CREATE — renewal pass (worker calls this)
    ├── usecase/subscriptions/subscriptions_test.go      CREATE
    ├── controller/http/auth.go                          MODIFY — TokenSink hook
    ├── controller/http/webhooks.go                      CREATE — POST /webhooks/graph
    ├── controller/http/subscriptions.go                 CREATE — POST /subscriptions
    ├── controller/http/dto.go                           MODIFY — notification payload, subscription response
    ├── controller/http/router.go                        MODIFY — new routes (webhook is UNauthenticated)
    ├── controller/http/webhooks_e2e_test.go             CREATE
    ├── controller/ws/hub.go                             MODIFY — order.updated wire DTO
    └── testutil/fakes.go                                MODIFY — GetMessage/subscription fakes, InMemSubscriptions, FakeTokens
```

Run all commands from `overseer/`.

---

### Task 1: `gateway/graph.GetMessage`

**Files:**
- Modify: `internal/gateway/graph/graph.go`
- Test: `internal/gateway/graph/graph_test.go`

**Interfaces:**
- Consumes: existing `graphMessage`/`toEntity` mapping in the same file.
- Produces: `func (c *Client) GetMessage(ctx context.Context, accessToken, messageID string) (entity.EmailMessage, error)` — Task 5's `MessageGetter` port.

- [ ] **Step 1: Write the failing test**

Create `internal/gateway/graph/graph_test.go`:

```go
package graph

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/messages/msg-1" {
			t.Errorf("path = %q, want /me/messages/msg-1", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id": "msg-1",
			"conversationId": "conv-9",
			"conversationIndex": "AQHa",
			"subject": "RE: PO 4471",
			"bodyPreview": "New ETA is July 12",
			"webLink": "https://outlook.example/msg-1",
			"createdDateTime": "2026-07-05T10:00:00Z",
			"body": {"content": "Hi, the new ETA is July 12."},
			"from": {"emailAddress": {"name": "Dana Delmar", "address": "dana@delmar.com"}}
		}`)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	msg, err := c.GetMessage(context.Background(), "tok-123", "msg-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.ID != "msg-1" || msg.ConversationID != "conv-9" || msg.From != "dana@delmar.com" {
		t.Errorf("mapped message = %+v", msg)
	}
	want := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	if !msg.CreatedDateTime.Equal(want) {
		t.Errorf("CreatedDateTime = %v, want %v", msg.CreatedDateTime, want)
	}
}

func TestGetMessageNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"nope"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	if _, err := c.GetMessage(context.Background(), "tok", "gone"); err == nil {
		t.Fatal("want error on 404, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/gateway/graph/ -run TestGetMessage -v`
Expected: FAIL — `c.GetMessage undefined`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/gateway/graph/graph.go`:

```go
// GetMessage fetches one message by Graph id — the webhook job's read path
// (spec: gateway/graph/GetMessage).
func (c *Client) GetMessage(ctx context.Context, accessToken, messageID string) (entity.EmailMessage, error) {
	q := url.Values{}
	q.Set("$select", "id,conversationId,conversationIndex,from,subject,body,bodyPreview,webLink,createdDateTime")
	reqURL := c.BaseURL + "/me/messages/" + url.PathEscape(messageID) + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return entity.EmailMessage{}, fmt.Errorf("graph: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return entity.EmailMessage{}, fmt.Errorf("graph: get message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return entity.EmailMessage{}, fmt.Errorf("graph: get message: status %d: %s", resp.StatusCode, body)
	}

	var gm graphMessage
	if err := json.NewDecoder(resp.Body).Decode(&gm); err != nil {
		return entity.EmailMessage{}, fmt.Errorf("graph: decode message: %w", err)
	}
	return toEntity(gm), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/gateway/graph/ -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/graph/
git commit -m "feat: add gateway/graph GetMessage for webhook job"
```

---

### Task 2: `gateway/graph` subscriptions (create + renew)

**Files:**
- Modify: `internal/gateway/graph/graph.go`
- Test: `internal/gateway/graph/graph_test.go`

**Interfaces:**
- Produces (Task 7's `GraphSubscriber` port):
  - `func (c *Client) CreateSubscription(ctx context.Context, accessToken, notificationURL, clientState string, expires time.Time) (string, error)` — returns Microsoft's subscription id.
  - `func (c *Client) RenewSubscription(ctx context.Context, accessToken, subscriptionID string, expires time.Time) error`

- [ ] **Step 1: Write the failing tests**

Append to `internal/gateway/graph/graph_test.go`:

```go
func TestCreateSubscription(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/subscriptions" {
			t.Errorf("got %s %s, want POST /subscriptions", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id": "sub-abc", "expirationDateTime": "2026-07-07T10:00:00Z"}`)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	expires := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	id, err := c.CreateSubscription(context.Background(), "tok", "https://app.example/webhooks/graph", "secret-state", expires)
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if id != "sub-abc" {
		t.Errorf("id = %q, want sub-abc", id)
	}
	if gotBody["changeType"] != "created" ||
		gotBody["resource"] != "/me/messages" ||
		gotBody["notificationUrl"] != "https://app.example/webhooks/graph" ||
		gotBody["clientState"] != "secret-state" ||
		gotBody["expirationDateTime"] != "2026-07-07T10:00:00Z" {
		t.Errorf("request body = %+v", gotBody)
	}
}

func TestRenewSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/subscriptions/sub-abc" {
			t.Errorf("got %s %s, want PATCH /subscriptions/sub-abc", r.Method, r.URL.Path)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["expirationDateTime"] != "2026-07-09T10:00:00Z" {
			t.Errorf("body = %+v", body)
		}
		fmt.Fprint(w, `{"id": "sub-abc"}`)
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	expires := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	if err := c.RenewSubscription(context.Background(), "tok", "sub-abc", expires); err != nil {
		t.Fatalf("RenewSubscription: %v", err)
	}
}
```

Add `"encoding/json"` to the test imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/gateway/graph/ -run 'TestCreateSubscription|TestRenewSubscription' -v`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/gateway/graph/graph.go`:

```go
// subscriptionRequest mirrors Graph's POST /subscriptions body.
type subscriptionRequest struct {
	ChangeType         string `json:"changeType"`
	NotificationURL    string `json:"notificationUrl"`
	Resource           string `json:"resource"`
	ExpirationDateTime string `json:"expirationDateTime"`
	ClientState        string `json:"clientState"`
}

type subscriptionResponse struct {
	ID string `json:"id"`
}

// CreateSubscription registers a change-notification webhook for new inbox
// messages and returns Microsoft's subscription id. Graph validates
// notificationURL synchronously during this call (the validationToken echo).
func (c *Client) CreateSubscription(ctx context.Context, accessToken, notificationURL, clientState string, expires time.Time) (string, error) {
	payload, err := json.Marshal(subscriptionRequest{
		ChangeType:         "created",
		NotificationURL:    notificationURL,
		Resource:           "/me/messages",
		ExpirationDateTime: expires.UTC().Format(time.RFC3339),
		ClientState:        clientState,
	})
	if err != nil {
		return "", fmt.Errorf("graph: marshal subscription: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/subscriptions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("graph: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("graph: create subscription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("graph: create subscription: status %d: %s", resp.StatusCode, body)
	}

	var out subscriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("graph: decode subscription: %w", err)
	}
	return out.ID, nil
}

// RenewSubscription pushes a subscription's expiry forward (~3-day max
// lifetime per Graph; the renewal worker calls this before expiry).
func (c *Client) RenewSubscription(ctx context.Context, accessToken, subscriptionID string, expires time.Time) error {
	payload, err := json.Marshal(map[string]string{
		"expirationDateTime": expires.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("graph: marshal renewal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		c.BaseURL+"/subscriptions/"+url.PathEscape(subscriptionID), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("graph: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("graph: renew subscription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("graph: renew subscription: status %d: %s", resp.StatusCode, body)
	}
	return nil
}
```

Add `"bytes"` to the imports of `graph.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/gateway/graph/ -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/graph/
git commit -m "feat: add Graph subscription create/renew to gateway"
```

---

### Task 3: repos — `GetByConversation` rework, `repo/subscriptions`, migration, fakes

`repo.Orders.GetByConversation` exists but is unused and leaks `pgx.ErrNoRows` with a `string` userID. Rework it to the port shape before anything consumes it, add the subscriptions repo, and extend `testutil`. Repo SQL is exercised in the manual live phase (project convention: no DB in unit tests), so the tests here cover the in-memory fakes that the usecase tests depend on.

**Files:**
- Create: `db/migrations/20260705000003_graph_subscriptions_unique.sql`
- Modify: `internal/repo/orders.go`
- Create: `internal/repo/subscriptions.go`
- Modify: `internal/testutil/fakes.go`
- Test: `internal/testutil/fakes_test.go`

**Interfaces:**
- Produces:
  - `func (r *Orders) GetByConversation(ctx context.Context, userID uuid.UUID, conversationID string) (entity.Order, bool, error)` — found-bool, no pgx sentinel.
  - `repo.Subscriptions` with:
    - `UpsertForUser(ctx context.Context, userID uuid.UUID, graphSubscriptionID string, expiresAt time.Time) error`
    - `ResolveSubscription(ctx context.Context, graphSubscriptionID string) (entity.User, bool, error)` (joins `users` so the job gets `ID` + `AccountID`)
    - `ListExpiring(ctx context.Context, before time.Time) ([]entity.GraphSubscription, error)`
    - `UpdateExpiry(ctx context.Context, graphSubscriptionID string, expiresAt time.Time) error`
  - testutil: `InMemOrders.GetByConversation`, `InMemSubscriptions`, `FakeTokens`, `FakeGraph.GetMessage/CreateSubscription/RenewSubscription`.

- [ ] **Step 1: Write the migration**

Create `db/migrations/20260705000003_graph_subscriptions_unique.sql`:

```sql
-- migrate:up

-- One subscription per user (MVP): UpsertForUser is ON CONFLICT (user_id),
-- which requires a unique index. graph_subscription_id is unique because the
-- webhook resolves notifications by it.
CREATE UNIQUE INDEX IF NOT EXISTS idx_graph_subscriptions_user
    ON graph_subscriptions (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_graph_subscriptions_graph_id
    ON graph_subscriptions (graph_subscription_id);

-- migrate:down

DROP INDEX IF EXISTS idx_graph_subscriptions_graph_id;
DROP INDEX IF EXISTS idx_graph_subscriptions_user;
```

- [ ] **Step 2: Write the failing fake tests**

Create `internal/testutil/fakes_test.go`:

```go
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/testutil/ -v`
Expected: FAIL — `GetByConversation`/`NewInMemSubscriptions` undefined.

- [ ] **Step 4: Rework `repo.Orders.GetByConversation`**

In `internal/repo/orders.go`, replace the existing `GetByConversation` method with:

```go
// GetByConversation is the webhook-job lookup (idx_orders_conversation):
// found=false, not an error, when the conversation has no order row yet.
func (r *Orders) GetByConversation(ctx context.Context, userID uuid.UUID, conversationID string) (entity.Order, bool, error) {
	rows, err := r.db.Query(ctx, getOrderByConversationSQL, userID, conversationID)
	if err != nil {
		return entity.Order{}, false, fmt.Errorf("get order by conversation: %w", err)
	}
	defer rows.Close()
	out, err := scanOrders(rows)
	if err != nil {
		return entity.Order{}, false, err
	}
	if len(out) == 0 {
		return entity.Order{}, false, nil
	}
	return out[0], true, nil
}
```

The `"github.com/jackc/pgx/v5"` import stays (used by `pgx.Rows` in `scanOrders`).

- [ ] **Step 5: Create `internal/repo/subscriptions.go`**

```go
package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thirdeye/overseer/internal/entity"
)

const upsertSubscriptionSQL = `
	INSERT INTO graph_subscriptions (user_id, graph_subscription_id, expires_at)
	VALUES ($1, $2, $3)
	ON CONFLICT (user_id) DO UPDATE SET
		graph_subscription_id = EXCLUDED.graph_subscription_id,
		expires_at = EXCLUDED.expires_at` // one live subscription per user (MVP)

const resolveSubscriptionSQL = `
	SELECT u.id, u.account_id, u.email
	FROM graph_subscriptions gs
	JOIN users u ON u.id = gs.user_id
	WHERE gs.graph_subscription_id = $1` // the webhook's notification → user lookup

const listExpiringSQL = `
	SELECT id, user_id, graph_subscription_id, expires_at, created_at
	FROM graph_subscriptions
	WHERE expires_at < $1`

const updateExpirySQL = `
	UPDATE graph_subscriptions SET expires_at = $2 WHERE graph_subscription_id = $1`

type Subscriptions struct {
	db *pgxpool.Pool
}

func NewSubscriptions(db *pgxpool.Pool) *Subscriptions { return &Subscriptions{db: db} }

func (r *Subscriptions) UpsertForUser(ctx context.Context, userID uuid.UUID, graphSubscriptionID string, expiresAt time.Time) error {
	if _, err := r.db.Exec(ctx, upsertSubscriptionSQL, userID, graphSubscriptionID, expiresAt); err != nil {
		return fmt.Errorf("upsert subscription: %w", err)
	}
	return nil
}

func (r *Subscriptions) ResolveSubscription(ctx context.Context, graphSubscriptionID string) (entity.User, bool, error) {
	var u entity.User
	rows, err := r.db.Query(ctx, resolveSubscriptionSQL, graphSubscriptionID)
	if err != nil {
		return entity.User{}, false, fmt.Errorf("resolve subscription: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return entity.User{}, false, rows.Err()
	}
	if err := rows.Scan(&u.ID, &u.AccountID, &u.Email); err != nil {
		return entity.User{}, false, fmt.Errorf("scan subscription user: %w", err)
	}
	return u, true, nil
}

func (r *Subscriptions) ListExpiring(ctx context.Context, before time.Time) ([]entity.GraphSubscription, error) {
	rows, err := r.db.Query(ctx, listExpiringSQL, before)
	if err != nil {
		return nil, fmt.Errorf("list expiring subscriptions: %w", err)
	}
	defer rows.Close()

	var out []entity.GraphSubscription
	for rows.Next() {
		var s entity.GraphSubscription
		if err := rows.Scan(&s.ID, &s.UserID, &s.GraphSubscriptionID, &s.ExpiresAt, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subscriptions: %w", err)
	}
	return out, nil
}

func (r *Subscriptions) UpdateExpiry(ctx context.Context, graphSubscriptionID string, expiresAt time.Time) error {
	if _, err := r.db.Exec(ctx, updateExpirySQL, graphSubscriptionID, expiresAt); err != nil {
		return fmt.Errorf("update subscription expiry: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Extend `internal/testutil/fakes.go`**

Add to `FakeGraph` (below `ListMessages`):

```go
	// GetMessage support: message id → message. GetErr forces a failure.
	MessagesByID map[string]entity.EmailMessage
	GetErr       error
	GetCalls     []string

	// Subscription support.
	CreatedSubID     string
	CreateErr        error
	CreateCalls      []SubscriptionCall
	RenewErr         error
	RenewedIDs       []string
```

```go
// SubscriptionCall records one CreateSubscription invocation.
type SubscriptionCall struct {
	Token           string
	NotificationURL string
	ClientState     string
	Expires         time.Time
}

func (f *FakeGraph) GetMessage(_ context.Context, accessToken, messageID string) (entity.EmailMessage, error) {
	f.TokensSeen = append(f.TokensSeen, accessToken)
	f.GetCalls = append(f.GetCalls, messageID)
	if f.GetErr != nil {
		return entity.EmailMessage{}, f.GetErr
	}
	msg, ok := f.MessagesByID[messageID]
	if !ok {
		return entity.EmailMessage{}, fmt.Errorf("fake graph: no message %q", messageID)
	}
	return msg, nil
}

func (f *FakeGraph) CreateSubscription(_ context.Context, accessToken, notificationURL, clientState string, expires time.Time) (string, error) {
	f.CreateCalls = append(f.CreateCalls, SubscriptionCall{Token: accessToken, NotificationURL: notificationURL, ClientState: clientState, Expires: expires})
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	return f.CreatedSubID, nil
}

func (f *FakeGraph) RenewSubscription(_ context.Context, _ string, subscriptionID string, _ time.Time) error {
	if f.RenewErr != nil {
		return f.RenewErr
	}
	f.RenewedIDs = append(f.RenewedIDs, subscriptionID)
	return nil
}
```

Add to `InMemOrders`:

```go
func (r *InMemOrders) GetByConversation(_ context.Context, userID uuid.UUID, conversationID string) (entity.Order, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.rows[key(userID, conversationID)]
	return o, ok, nil
}
```

Add new fakes at the end of the file:

```go
// --- InMemSubscriptions --------------------------------------------------------

// InMemSubscriptions backs both the messages usecases' SubscriptionResolver
// and the subscriptions usecases' SubscriptionRepo.
type InMemSubscriptions struct {
	mu    sync.Mutex
	users map[uuid.UUID]entity.User            // user id → user
	subs  map[string]entity.GraphSubscription  // graph subscription id → row
}

func NewInMemSubscriptions() *InMemSubscriptions {
	return &InMemSubscriptions{
		users: make(map[uuid.UUID]entity.User),
		subs:  make(map[string]entity.GraphSubscription),
	}
}

// AddUser registers the user rows the join in ResolveSubscription would see.
func (r *InMemSubscriptions) AddUser(u entity.User) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[u.ID] = u
}

func (r *InMemSubscriptions) UpsertForUser(_ context.Context, userID uuid.UUID, graphSubscriptionID string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.subs { // one subscription per user, like ON CONFLICT (user_id)
		if s.UserID == userID {
			delete(r.subs, id)
		}
	}
	r.subs[graphSubscriptionID] = entity.GraphSubscription{
		ID: uuid.New(), UserID: userID,
		GraphSubscriptionID: graphSubscriptionID, ExpiresAt: expiresAt, CreatedAt: time.Now(),
	}
	return nil
}

func (r *InMemSubscriptions) ResolveSubscription(_ context.Context, graphSubscriptionID string) (entity.User, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subs[graphSubscriptionID]
	if !ok {
		return entity.User{}, false, nil
	}
	u, ok := r.users[s.UserID]
	return u, ok, nil
}

func (r *InMemSubscriptions) ListExpiring(_ context.Context, before time.Time) ([]entity.GraphSubscription, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []entity.GraphSubscription
	for _, s := range r.subs {
		if s.ExpiresAt.Before(before) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *InMemSubscriptions) UpdateExpiry(_ context.Context, graphSubscriptionID string, expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.subs[graphSubscriptionID]
	if !ok {
		return fmt.Errorf("fake subscriptions: no subscription %q", graphSubscriptionID)
	}
	s.ExpiresAt = expiresAt
	r.subs[graphSubscriptionID] = s
	return nil
}

// --- FakeTokens ----------------------------------------------------------------

// FakeTokens is an in-memory TokenSource/TokenSink.
type FakeTokens struct {
	mu     sync.Mutex
	tokens map[uuid.UUID]string
}

func NewFakeTokens() *FakeTokens {
	return &FakeTokens{tokens: make(map[uuid.UUID]string)}
}

func (f *FakeTokens) Put(userID uuid.UUID, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[userID] = token
}

func (f *FakeTokens) GraphToken(userID uuid.UUID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[userID]
	return t, ok
}
```

Add `"fmt"` to the testutil imports.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/testutil/ ./internal/repo/ ./... 2>&1 | tail -20`
Expected: PASS everywhere (nothing consumed the old `GetByConversation`, so no other package breaks).

- [ ] **Step 8: Commit**

```bash
git add db/migrations/ internal/repo/ internal/testutil/
git commit -m "feat: subscriptions repo, GetByConversation port shape, webhook fakes"
```

---

### Task 4: `tokencache` + auth-middleware sink

**Files:**
- Create: `internal/tokencache/tokencache.go`
- Test: `internal/tokencache/tokencache_test.go`
- Modify: `internal/controller/http/auth.go`
- Modify: `internal/controller/http/router.go`

**Interfaces:**
- Produces:
  - `tokencache.New() *Cache`, `(*Cache).Put(userID uuid.UUID, token string)`, `(*Cache).GraphToken(userID uuid.UUID) (string, bool)`.
  - `http.TokenSink` interface `{ Put(userID uuid.UUID, token string) }`; `AuthMiddleware(resolver UserResolver, tokens TokenSink)`; `RouterDeps.Tokens TokenSink`.
- Consumes: nothing new.

- [ ] **Step 1: Write the failing test**

Create `internal/tokencache/tokencache_test.go`:

```go
package tokencache

import (
	"testing"

	"github.com/google/uuid"
)

func TestPutAndGet(t *testing.T) {
	c := New()
	userID := uuid.New()

	if _, ok := c.GraphToken(userID); ok {
		t.Fatal("empty cache returned a token")
	}

	c.Put(userID, "tok-1")
	got, ok := c.GraphToken(userID)
	if !ok || got != "tok-1" {
		t.Fatalf("GraphToken = %q, %v", got, ok)
	}

	c.Put(userID, "tok-2") // most recent token wins
	got, _ = c.GraphToken(userID)
	if got != "tok-2" {
		t.Fatalf("GraphToken = %q, want tok-2", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tokencache/ -v`
Expected: FAIL — package doesn't exist / `New` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/tokencache/tokencache.go`:

```go
// Package tokencache holds the last Graph access token seen per user. Graph
// change notifications carry no token, but the webhook job must call Graph on
// the user's behalf — the auth middleware fills this cache on every authed
// request, and the job reads from it. In-memory MVP; the durable
// refresh-token flow (users.graph_refresh_token_enc) replaces this later.
package tokencache

import (
	"sync"

	"github.com/google/uuid"
)

type Cache struct {
	mu     sync.RWMutex
	tokens map[uuid.UUID]string
}

func New() *Cache {
	return &Cache{tokens: make(map[uuid.UUID]string)}
}

// Put stores the most recent token for the user.
func (c *Cache) Put(userID uuid.UUID, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[userID] = token
}

// GraphToken returns the last token seen for the user, if any.
func (c *Cache) GraphToken(userID uuid.UUID) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tokens[userID]
	return t, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tokencache/ -v`
Expected: PASS.

- [ ] **Step 5: Wire the sink into the auth middleware**

In `internal/controller/http/auth.go`:

Add below the `UserResolver` interface (and add `"github.com/google/uuid"` to imports):

```go
// TokenSink receives the Graph token from every authed request so background
// jobs (webhook handling, subscription renewal) can call Graph later.
type TokenSink interface {
	Put(userID uuid.UUID, token string)
}
```

Change the middleware signature and body:

```go
func AuthMiddleware(resolver UserResolver, tokens TokenSink) echo.MiddlewareFunc {
```

and inside, after `resolver.Resolve` succeeds, before `c.Set(...)`:

```go
			if tokens != nil {
				tokens.Put(user.ID, token)
			}
```

In `internal/controller/http/router.go`, add `Tokens TokenSink` to `RouterDeps` and change:

```go
	auth := AuthMiddleware(d.Resolver, d.Tokens)
```

- [ ] **Step 6: Fix compilation of existing tests and run the suite**

`orders_e2e_test.go` builds `NewRouter` without `Tokens` — a nil `TokenSink` is legal, so it compiles unchanged.

Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tokencache/ internal/controller/http/
git commit -m "feat: cache Graph tokens per user for background jobs"
```

---

### Task 5: `entity.OrderFromExtraction` (shared constructor)

The webhook job (Task 6) builds new orders exactly like the sync does. Move the unexported `orderFromExtraction` from `usecase/orders` into `entity` so both usecases share it.

**Files:**
- Modify: `internal/entity/order.go`
- Modify: `internal/usecase/orders/format_initial_order_data.go`

**Interfaces:**
- Produces: `func OrderFromExtraction(thread Thread, x OrderExtraction) Order` in `entity`.

- [ ] **Step 1: Move the function**

Append to `internal/entity/order.go`:

```go
// OrderFromExtraction builds an Order from a thread and its LLM extraction —
// conversation-level fields only; the caller stamps ownership (account/user)
// before persisting. Empty extraction fields fall back to the latest message.
func OrderFromExtraction(thread Thread, x OrderExtraction) Order {
	latest := thread.LatestMessage()
	o := Order{
		ConversationID:  thread.ConversationID,
		CompanyName:     x.CompanyName,
		EmailAddress:    x.EmailAddress,
		OrderID:         x.OrderID,
		EmailURL:        latest.WebLink,
		Status:          x.Status,
		OriginalEstDate: x.OriginalEstDate,
		CurrentEstDate:  x.CurrentEstDate,
		LastMessenger:   x.LastMessenger,
		SupportingQuote: x.SupportingQuote,
	}
	if o.LastMessenger == "" {
		o.LastMessenger = latest.FromName
	}
	if o.EmailAddress == "" {
		o.EmailAddress = latest.From
	}
	return o
}
```

In `internal/usecase/orders/format_initial_order_data.go`: delete the whole `orderFromExtraction` function and change its call site to:

```go
		orders = append(orders, entity.OrderFromExtraction(thread, extraction))
```

- [ ] **Step 2: Run the existing suite (behavior must not change)**

Run: `go test ./...`
Expected: PASS — `format_initial_order_data_test.go` still covers the mapping through the usecase.

- [ ] **Step 3: Commit**

```bash
git add internal/entity/order.go internal/usecase/orders/format_initial_order_data.go
git commit -m "refactor: move order-from-extraction mapping into entity"
```

---

### Task 6: `usecase/messages` — `GetLatestMessage` + `HandleChangeMessage`

The heart of the phase. `HandleChangeMessage` (spec: `jobs/HandleChangeMessage`) is dispatched per webhook notification.

**Files:**
- Create: `internal/usecase/messages/ports.go`
- Create: `internal/usecase/messages/get_latest_message.go`
- Create: `internal/usecase/messages/handle_change_message.go`
- Test: `internal/usecase/messages/handle_change_message_test.go`

**Interfaces:**
- Consumes: `testutil.FakeGraph.GetMessage`, `testutil.InMemOrders.GetByConversation/Upsert`, `testutil.InMemSubscriptions.ResolveSubscription`, `testutil.FakeTokens`, `testutil.FakeLLM`, `testutil.EventRecorder`, `entity.OrderFromExtraction` (Task 5).
- Produces:
  - `messages.GetLatestMessage{Graph MessageGetter}` with `Execute(ctx context.Context, accessToken, messageID string) (entity.EmailMessage, error)`.
  - `messages.HandleChangeMessage{Subs, Tokens, Latest, LLM, Repo, Events}` with `Execute(ctx context.Context, in HandleInput) (HandleOutcome, error)`; `HandleInput{SubscriptionID, MessageID string}`; outcomes `OutcomeCreated | OutcomeUpdated | OutcomeSkippedNotOrder | OutcomeSkippedUnknownSubscription`.

- [ ] **Step 1: Write the ports**

Create `internal/usecase/messages/ports.go`:

```go
// Package messages holds the post-sync message usecases: the webhook job that
// keeps the order list live. Ports live where they are consumed.
package messages

import (
	"context"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// MessageGetter is the slice of gateway/graph the webhook job needs.
type MessageGetter interface {
	GetMessage(ctx context.Context, accessToken, messageID string) (entity.EmailMessage, error)
}

// OrderLLM matches usecase/orders' port: filter decides order-relevance for
// unseen conversations, extract structures the fields.
type OrderLLM interface {
	FilterOrderMessages(ctx context.Context, msgs []entity.EmailMessage) ([]string, error)
	ExtractOrderFields(ctx context.Context, thread entity.Thread) (entity.OrderExtraction, error)
}

// OrderRepo is the orders-table slice this job needs.
type OrderRepo interface {
	Upsert(ctx context.Context, o entity.Order) error
	GetByConversation(ctx context.Context, userID uuid.UUID, conversationID string) (entity.Order, bool, error)
}

// SubscriptionResolver maps a Graph subscription id to its owning user.
type SubscriptionResolver interface {
	ResolveSubscription(ctx context.Context, graphSubscriptionID string) (entity.User, bool, error)
}

// TokenSource hands out the user's most recent Graph access token.
type TokenSource interface {
	GraphToken(userID uuid.UUID) (string, bool)
}

// EventPublisher pushes typed events over the shared realtime channel.
type EventPublisher interface {
	Publish(e entity.Event)
}
```

- [ ] **Step 2: Write `GetLatestMessage`**

Create `internal/usecase/messages/get_latest_message.go`:

```go
package messages

import (
	"context"
	"fmt"

	"github.com/thirdeye/overseer/internal/entity"
)

// GetLatestMessage fetches the message a change notification points at
// (spec: usecase/messages/GetLatestMessage).
type GetLatestMessage struct {
	Graph MessageGetter
}

func (uc *GetLatestMessage) Execute(ctx context.Context, accessToken, messageID string) (entity.EmailMessage, error) {
	msg, err := uc.Graph.GetMessage(ctx, accessToken, messageID)
	if err != nil {
		return entity.EmailMessage{}, fmt.Errorf("get message %s: %w", messageID, err)
	}
	return msg, nil
}
```

- [ ] **Step 3: Write the failing tests**

Create `internal/usecase/messages/handle_change_message_test.go`:

```go
package messages

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

type fixture struct {
	uc     *HandleChangeMessage
	user   entity.User
	graph  *testutil.FakeGraph
	llm    *testutil.FakeLLM
	repo   *testutil.InMemOrders
	subs   *testutil.InMemSubscriptions
	tokens *testutil.FakeTokens
	events *testutil.EventRecorder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	user := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "op@distributor.com"}

	graph := &testutil.FakeGraph{MessagesByID: map[string]entity.EmailMessage{}}
	llm := &testutil.FakeLLM{Extractions: map[string]entity.OrderExtraction{}}
	repo := testutil.NewInMemOrders()
	subs := testutil.NewInMemSubscriptions()
	tokens := testutil.NewFakeTokens()
	events := &testutil.EventRecorder{}

	subs.AddUser(user)
	if err := subs.UpsertForUser(context.Background(), user.ID, "sub-1", time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	tokens.Put(user.ID, "graph-tok")

	return &fixture{
		uc: &HandleChangeMessage{
			Subs:   subs,
			Tokens: tokens,
			Latest: &GetLatestMessage{Graph: graph},
			LLM:    llm,
			Repo:   repo,
			Events: events,
		},
		user: user, graph: graph, llm: llm, repo: repo, subs: subs, tokens: tokens, events: events,
	}
}

func date(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestNewConversationCreatesOrder(t *testing.T) {
	f := newFixture(t)
	f.graph.MessagesByID["msg-1"] = entity.EmailMessage{
		ID: "msg-1", ConversationID: "conv-new", From: "dana@delmar.com",
		FromName: "Dana Delmar", Subject: "PO 4471 confirmed",
		WebLink: "https://outlook.example/msg-1", CreatedDateTime: time.Now(),
	}
	f.llm.FilterResult = []string{"conv-new"} // classified as a goods order
	f.llm.Extractions["conv-new"] = entity.OrderExtraction{
		CompanyName: "Delmar Tooling", OrderID: 4471,
		Status: entity.OrderStatusConfirmed, CurrentEstDate: date(2026, 7, 12),
	}

	outcome, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-1", MessageID: "msg-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome != OutcomeCreated {
		t.Errorf("outcome = %q, want created", outcome)
	}

	saved, found, _ := f.repo.GetByConversation(context.Background(), f.user.ID, "conv-new")
	if !found {
		t.Fatal("order row not created")
	}
	if saved.CompanyName != "Delmar Tooling" || saved.AccountID != f.user.AccountID || saved.UserID != f.user.ID {
		t.Errorf("saved = %+v", saved)
	}

	updates := f.events.OfType(entity.EventOrderUpdated)
	if len(updates) != 1 {
		t.Fatalf("order.updated events = %d, want 1", len(updates))
	}
	if o, ok := updates[0].Data.(entity.Order); !ok || o.ConversationID != "conv-new" {
		t.Errorf("event data = %+v", updates[0].Data)
	}
}

func TestNewConversationNonOrderIsSkipped(t *testing.T) {
	f := newFixture(t)
	f.graph.MessagesByID["msg-junk"] = entity.EmailMessage{
		ID: "msg-junk", ConversationID: "conv-junk", From: "news@saas.com",
		Subject: "Weekly newsletter", CreatedDateTime: time.Now(),
	}
	f.llm.FilterResult = nil // filter keeps nothing

	outcome, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-1", MessageID: "msg-junk"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome != OutcomeSkippedNotOrder {
		t.Errorf("outcome = %q, want skipped_not_order", outcome)
	}
	if f.repo.Count() != 0 {
		t.Errorf("repo rows = %d, want 0", f.repo.Count())
	}
	if len(f.events.OfType(entity.EventOrderUpdated)) != 0 {
		t.Error("junk message published an event")
	}
	if f.llm.ExtractCalls != 0 {
		t.Error("extraction ran on a filtered-out message")
	}
}

func TestExistingConversationMergesNotClobbers(t *testing.T) {
	f := newFixture(t)
	if err := f.repo.Upsert(context.Background(), entity.Order{
		AccountID: f.user.AccountID, UserID: f.user.ID,
		ConversationID: "conv-1", CompanyName: "Delmar Tooling", OrderID: 4471,
		Status:          entity.OrderStatusConfirmed,
		OriginalEstDate: date(2026, 7, 10), CurrentEstDate: date(2026, 7, 10),
		SupportingQuote: "Order confirmed for July 10",
	}); err != nil {
		t.Fatal(err)
	}

	f.graph.MessagesByID["msg-2"] = entity.EmailMessage{
		ID: "msg-2", ConversationID: "conv-1", From: "dana@delmar.com",
		FromName: "Dana Delmar", Subject: "RE: PO 4471 — delay",
		WebLink: "https://outlook.example/msg-2", CreatedDateTime: time.Now(),
	}
	// The new message only mentions the delay: no company, no order id, no
	// original date — merge must keep those from the existing row.
	f.llm.Extractions["conv-1"] = entity.OrderExtraction{
		Status: entity.OrderStatusInTransit, CurrentEstDate: date(2026, 7, 18),
		SupportingQuote: "Pushed to July 18 due to customs",
	}

	outcome, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-1", MessageID: "msg-2"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome != OutcomeUpdated {
		t.Errorf("outcome = %q, want updated", outcome)
	}
	if f.llm.FilterCalls != 0 {
		t.Error("filter ran on a known order conversation")
	}

	saved, _, _ := f.repo.GetByConversation(context.Background(), f.user.ID, "conv-1")
	if saved.CompanyName != "Delmar Tooling" || saved.OrderID != 4471 {
		t.Errorf("identity fields clobbered: %+v", saved)
	}
	if !saved.OriginalEstDate.Equal(*date(2026, 7, 10)) {
		t.Errorf("OriginalEstDate moved: %v", saved.OriginalEstDate)
	}
	if !saved.CurrentEstDate.Equal(*date(2026, 7, 18)) {
		t.Errorf("CurrentEstDate = %v, want 2026-07-18", saved.CurrentEstDate)
	}
	if saved.Status != entity.OrderStatusInTransit {
		t.Errorf("Status = %q", saved.Status)
	}
	if saved.SupportingQuote != "Pushed to July 18 due to customs" {
		t.Errorf("SupportingQuote = %q", saved.SupportingQuote)
	}
	if saved.LastMessenger != "Dana Delmar" || saved.EmailURL != "https://outlook.example/msg-2" {
		t.Errorf("latest-message fields not refreshed: %+v", saved)
	}
}

func TestRedeliveryIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.graph.MessagesByID["msg-1"] = entity.EmailMessage{
		ID: "msg-1", ConversationID: "conv-new", From: "dana@delmar.com",
		FromName: "Dana Delmar", CreatedDateTime: time.Now(),
	}
	f.llm.FilterResult = []string{"conv-new"}
	f.llm.Extractions["conv-new"] = entity.OrderExtraction{
		CompanyName: "Delmar Tooling", OrderID: 4471, Status: entity.OrderStatusConfirmed,
	}

	in := HandleInput{SubscriptionID: "sub-1", MessageID: "msg-1"}
	if _, err := f.uc.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.Execute(context.Background(), in); err != nil { // Graph re-delivers
		t.Fatal(err)
	}
	if f.repo.Count() != 1 {
		t.Errorf("repo rows = %d, want 1 (idempotent upsert)", f.repo.Count())
	}
}

func TestUnknownSubscriptionIsSkipped(t *testing.T) {
	f := newFixture(t)
	outcome, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-unknown", MessageID: "msg-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome != OutcomeSkippedUnknownSubscription {
		t.Errorf("outcome = %q", outcome)
	}
}

func TestMissingTokenErrors(t *testing.T) {
	f := newFixture(t)
	other := entity.User{ID: uuid.New(), AccountID: uuid.New(), Email: "other@x.com"}
	f.subs.AddUser(other)
	if err := f.subs.UpsertForUser(context.Background(), other.ID, "sub-2", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// no token cached for `other`
	if _, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-2", MessageID: "msg-1"}); err == nil {
		t.Fatal("want error when no Graph token is cached")
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/usecase/messages/ -v`
Expected: FAIL — `HandleChangeMessage` undefined.

- [ ] **Step 5: Write the implementation**

Create `internal/usecase/messages/handle_change_message.go`:

```go
package messages

import (
	"context"
	"fmt"
	"slices"

	"github.com/thirdeye/overseer/internal/entity"
)

// HandleChangeMessage is the webhook job (spec: jobs/HandleChangeMessage):
// notification → resolve subscription to user → fetch the message → LLM
// structure → merge-upsert into orders → order.updated on the realtime
// channel. Graph delivers at-least-once, so the whole path is idempotent —
// the upsert is keyed on (user_id, conversation_id).
//
// Extension point (Checkups, not built yet): this is the only place that sees
// supplier replies — when checkup_messages exists, cancel pending rows with
// cancel_if_reply on this conversation here.
type HandleChangeMessage struct {
	Subs   SubscriptionResolver
	Tokens TokenSource
	Latest *GetLatestMessage
	LLM    OrderLLM
	Repo   OrderRepo
	Events EventPublisher
}

type HandleInput struct {
	SubscriptionID string // Graph's subscription id from the notification
	MessageID      string // Graph message id from resourceData
}

// HandleOutcome tells the caller (for logging) what the job did; skips are
// normal operation, not errors.
type HandleOutcome string

const (
	OutcomeCreated                    HandleOutcome = "created"
	OutcomeUpdated                    HandleOutcome = "updated"
	OutcomeSkippedNotOrder            HandleOutcome = "skipped_not_order"
	OutcomeSkippedUnknownSubscription HandleOutcome = "skipped_unknown_subscription"
)

func (uc *HandleChangeMessage) Execute(ctx context.Context, in HandleInput) (HandleOutcome, error) {
	user, ok, err := uc.Subs.ResolveSubscription(ctx, in.SubscriptionID)
	if err != nil {
		return "", fmt.Errorf("resolve subscription: %w", err)
	}
	if !ok {
		return OutcomeSkippedUnknownSubscription, nil
	}

	token, ok := uc.Tokens.GraphToken(user.ID)
	if !ok {
		return "", fmt.Errorf("no cached graph token for user %s", user.ID)
	}

	msg, err := uc.Latest.Execute(ctx, token, in.MessageID)
	if err != nil {
		return "", err
	}
	thread := entity.Thread{ConversationID: msg.ConversationID, Messages: []entity.EmailMessage{msg}}

	existing, found, err := uc.Repo.GetByConversation(ctx, user.ID, msg.ConversationID)
	if err != nil {
		return "", fmt.Errorf("get order by conversation: %w", err)
	}

	// Unseen conversations get classified first — without this gate every
	// newsletter that arrives post-sync would become an order row. Known
	// order conversations were classified at sync time; skip straight to
	// extraction.
	if !found {
		kept, err := uc.LLM.FilterOrderMessages(ctx, thread.Messages)
		if err != nil {
			return "", fmt.Errorf("filter message: %w", err)
		}
		if !slices.Contains(kept, msg.ConversationID) {
			return OutcomeSkippedNotOrder, nil
		}
	}

	extraction, err := uc.LLM.ExtractOrderFields(ctx, thread)
	if err != nil {
		return "", fmt.Errorf("extract order fields (conversation %s): %w", msg.ConversationID, err)
	}

	outcome := OutcomeCreated
	var order entity.Order
	if found {
		outcome = OutcomeUpdated
		order = mergeExtraction(existing, extraction, msg)
	} else {
		order = entity.OrderFromExtraction(thread, extraction)
		order.AccountID = user.AccountID
		order.UserID = user.ID
	}

	if err := uc.Repo.Upsert(ctx, order); err != nil {
		return "", fmt.Errorf("upsert order (conversation %s): %w", msg.ConversationID, err)
	}

	// Re-read so the event carries the authoritative row (id, timestamps).
	if saved, ok, err := uc.Repo.GetByConversation(ctx, user.ID, msg.ConversationID); err == nil && ok {
		order = saved
	}

	uc.Events.Publish(entity.Event{Type: entity.EventOrderUpdated, Data: order})
	return outcome, nil
}

// mergeExtraction folds a single new message's extraction into the existing
// order. One message rarely mentions every field, so empty extraction fields
// keep their stored values; OriginalEstDate never moves once set.
func mergeExtraction(existing entity.Order, x entity.OrderExtraction, latest entity.EmailMessage) entity.Order {
	o := existing
	if o.CompanyName == "" {
		o.CompanyName = x.CompanyName
	}
	if x.OrderID != 0 {
		o.OrderID = x.OrderID
	}
	if entity.ValidOrderStatus(x.Status) {
		o.Status = x.Status
	}
	if o.OriginalEstDate == nil {
		o.OriginalEstDate = x.OriginalEstDate
	}
	if x.CurrentEstDate != nil {
		o.CurrentEstDate = x.CurrentEstDate
	}
	if x.SupportingQuote != "" {
		o.SupportingQuote = x.SupportingQuote
	}
	switch {
	case x.LastMessenger != "":
		o.LastMessenger = x.LastMessenger
	case latest.FromName != "":
		o.LastMessenger = latest.FromName
	}
	if latest.WebLink != "" {
		o.EmailURL = latest.WebLink
	}
	if o.EmailAddress == "" {
		o.EmailAddress = latest.From
	}
	return o
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/usecase/messages/ -v`
Expected: PASS (all six tests).

- [ ] **Step 7: Commit**

```bash
git add internal/usecase/messages/
git commit -m "feat: HandleChangeMessage webhook job with merge-upsert"
```

---

### Task 7: `usecase/subscriptions` — ensure + renew

**Files:**
- Create: `internal/usecase/subscriptions/ports.go`
- Create: `internal/usecase/subscriptions/ensure_subscription.go`
- Create: `internal/usecase/subscriptions/renew_due_subscriptions.go`
- Test: `internal/usecase/subscriptions/subscriptions_test.go`

**Interfaces:**
- Consumes: `testutil.FakeGraph` subscription methods, `testutil.InMemSubscriptions`, `testutil.FakeTokens` (Task 3).
- Produces:
  - `subscriptions.EnsureSubscription{Graph, Repo, NotificationURL, ClientState string, TTL time.Duration, Now func() time.Time}` with `Execute(ctx, EnsureInput{UserID uuid.UUID, GraphToken string}) (EnsureOutput{GraphSubscriptionID string, ExpiresAt time.Time}, error)`.
  - `subscriptions.RenewDueSubscriptions{Graph, Repo, Tokens, TTL, RenewAhead time.Duration, Now func() time.Time, Log *slog.Logger}` with `Execute(ctx) (int, error)`.

- [ ] **Step 1: Write the ports**

Create `internal/usecase/subscriptions/ports.go`:

```go
// Package subscriptions manages Graph change-notification subscriptions:
// creation at onboarding and renewal before the ~3-day expiry.
package subscriptions

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// GraphSubscriber is the slice of gateway/graph these usecases need.
type GraphSubscriber interface {
	CreateSubscription(ctx context.Context, accessToken, notificationURL, clientState string, expires time.Time) (string, error)
	RenewSubscription(ctx context.Context, accessToken, subscriptionID string, expires time.Time) error
}

// SubscriptionRepo is the graph_subscriptions-table port.
type SubscriptionRepo interface {
	UpsertForUser(ctx context.Context, userID uuid.UUID, graphSubscriptionID string, expiresAt time.Time) error
	ListExpiring(ctx context.Context, before time.Time) ([]entity.GraphSubscription, error)
	UpdateExpiry(ctx context.Context, graphSubscriptionID string, expiresAt time.Time) error
}

// TokenSource hands out the user's most recent Graph access token.
type TokenSource interface {
	GraphToken(userID uuid.UUID) (string, bool)
}
```

- [ ] **Step 2: Write the failing tests**

Create `internal/usecase/subscriptions/subscriptions_test.go`:

```go
package subscriptions

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

func TestEnsureSubscription(t *testing.T) {
	graph := &testutil.FakeGraph{CreatedSubID: "sub-abc"}
	repo := testutil.NewInMemSubscriptions()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	uc := &EnsureSubscription{
		Graph:           graph,
		Repo:            repo,
		NotificationURL: "https://app.example/webhooks/graph",
		ClientState:     "secret-state",
		TTL:             48 * time.Hour,
		Now:             func() time.Time { return now },
	}

	userID := uuid.New()
	out, err := uc.Execute(context.Background(), EnsureInput{UserID: userID, GraphToken: "tok"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.GraphSubscriptionID != "sub-abc" {
		t.Errorf("id = %q", out.GraphSubscriptionID)
	}
	if !out.ExpiresAt.Equal(now.Add(48 * time.Hour)) {
		t.Errorf("ExpiresAt = %v", out.ExpiresAt)
	}

	if len(graph.CreateCalls) != 1 {
		t.Fatalf("CreateSubscription calls = %d", len(graph.CreateCalls))
	}
	call := graph.CreateCalls[0]
	if call.NotificationURL != "https://app.example/webhooks/graph" || call.ClientState != "secret-state" || call.Token != "tok" {
		t.Errorf("create call = %+v", call)
	}

	// Row persisted → webhook can resolve it (user registered separately).
	repo.AddUser(entity.User{ID: userID})
	if _, found, _ := repo.ResolveSubscription(context.Background(), "sub-abc"); !found {
		t.Error("subscription row not persisted")
	}
}

func TestRenewDueSubscriptions(t *testing.T) {
	graph := &testutil.FakeGraph{}
	repo := testutil.NewInMemSubscriptions()
	tokens := testutil.NewFakeTokens()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	dueUser, freshUser, tokenlessUser := uuid.New(), uuid.New(), uuid.New()
	// expires within the renew-ahead window → due
	_ = repo.UpsertForUser(context.Background(), dueUser, "sub-due", now.Add(6*time.Hour))
	// expires far out → not due
	_ = repo.UpsertForUser(context.Background(), freshUser, "sub-fresh", now.Add(40*time.Hour))
	// due but no cached token → skipped, not fatal
	_ = repo.UpsertForUser(context.Background(), tokenlessUser, "sub-naked", now.Add(6*time.Hour))
	tokens.Put(dueUser, "tok-due")

	uc := &RenewDueSubscriptions{
		Graph:      graph,
		Repo:       repo,
		Tokens:     tokens,
		TTL:        48 * time.Hour,
		RenewAhead: 12 * time.Hour,
		Now:        func() time.Time { return now },
		Log:        slog.New(slog.DiscardHandler),
	}

	renewed, err := uc.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if renewed != 1 {
		t.Errorf("renewed = %d, want 1", renewed)
	}
	if len(graph.RenewedIDs) != 1 || graph.RenewedIDs[0] != "sub-due" {
		t.Errorf("RenewedIDs = %v", graph.RenewedIDs)
	}

	// sub-due's expiry moved forward, so nothing is due anymore.
	due, _ := repo.ListExpiring(context.Background(), now.Add(12*time.Hour))
	if len(due) != 1 || due[0].GraphSubscriptionID != "sub-naked" {
		t.Errorf("still expiring = %v, want only sub-naked", due)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/usecase/subscriptions/ -v`
Expected: FAIL — types undefined.

- [ ] **Step 4: Write the implementations**

Create `internal/usecase/subscriptions/ensure_subscription.go`:

```go
package subscriptions

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// EnsureSubscription creates (or replaces) the user's Graph message
// subscription and persists the mapping the webhook needs. POST /subscriptions
// calls this after onboarding; re-running it simply replaces the row.
type EnsureSubscription struct {
	Graph GraphSubscriber
	Repo  SubscriptionRepo

	NotificationURL string // public URL of POST /webhooks/graph
	ClientState     string // shared secret echoed back in every notification
	TTL             time.Duration
	Now             func() time.Time
}

type EnsureInput struct {
	UserID     uuid.UUID
	GraphToken string
}

type EnsureOutput struct {
	GraphSubscriptionID string
	ExpiresAt           time.Time
}

func (uc *EnsureSubscription) Execute(ctx context.Context, in EnsureInput) (EnsureOutput, error) {
	expires := uc.Now().Add(uc.TTL)

	id, err := uc.Graph.CreateSubscription(ctx, in.GraphToken, uc.NotificationURL, uc.ClientState, expires)
	if err != nil {
		return EnsureOutput{}, fmt.Errorf("create graph subscription: %w", err)
	}
	if err := uc.Repo.UpsertForUser(ctx, in.UserID, id, expires); err != nil {
		return EnsureOutput{}, fmt.Errorf("persist subscription: %w", err)
	}
	return EnsureOutput{GraphSubscriptionID: id, ExpiresAt: expires}, nil
}
```

Create `internal/usecase/subscriptions/renew_due_subscriptions.go`:

```go
package subscriptions

import (
	"context"
	"fmt"
	"log/slog"
)

// RenewDueSubscriptions is the renewal pass the scheduler worker runs:
// message subscriptions live ~3 days max, so anything expiring within
// RenewAhead gets pushed forward by TTL. Per-subscription failures are
// logged and skipped — one dead token must not stall the rest.
type RenewDueSubscriptions struct {
	Graph  GraphSubscriber
	Repo   SubscriptionRepo
	Tokens TokenSource

	TTL        time.Duration
	RenewAhead time.Duration
	Now        func() time.Time
	Log        *slog.Logger
}

func (uc *RenewDueSubscriptions) Execute(ctx context.Context) (int, error) {
	due, err := uc.Repo.ListExpiring(ctx, uc.Now().Add(uc.RenewAhead))
	if err != nil {
		return 0, fmt.Errorf("list expiring subscriptions: %w", err)
	}

	renewed := 0
	for _, s := range due {
		token, ok := uc.Tokens.GraphToken(s.UserID)
		if !ok {
			uc.Log.Warn("subscription renewal skipped: no cached token",
				"subscriptionId", s.GraphSubscriptionID, "userId", s.UserID)
			continue
		}
		newExpiry := uc.Now().Add(uc.TTL)
		if err := uc.Graph.RenewSubscription(ctx, token, s.GraphSubscriptionID, newExpiry); err != nil {
			uc.Log.Error("subscription renewal failed",
				"subscriptionId", s.GraphSubscriptionID, "error", err)
			continue
		}
		if err := uc.Repo.UpdateExpiry(ctx, s.GraphSubscriptionID, newExpiry); err != nil {
			uc.Log.Error("subscription expiry update failed",
				"subscriptionId", s.GraphSubscriptionID, "error", err)
			continue
		}
		renewed++
	}
	return renewed, nil
}
```

(Note: `renew_due_subscriptions.go` needs `"time"` in its imports.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/usecase/subscriptions/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/usecase/subscriptions/
git commit -m "feat: subscription ensure + renewal usecases"
```

---

### Task 8: `order.updated` on the ws hub

**Files:**
- Modify: `internal/controller/ws/hub.go`
- Test: `internal/controller/ws/hub_test.go`

**Interfaces:**
- Consumes: `entity.Order`, existing `toDTO` switch.
- Produces: `entity.Order` event data serialized as the camelCase order shape (same fields as the HTTP `orderDTO`; ws is its own edge and maps its own DTOs, mirroring `syncCompletedDTO`).

- [ ] **Step 1: Write the failing test**

Create `internal/controller/ws/hub_test.go`:

```go
package ws

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

func TestToDTOOrder(t *testing.T) {
	est := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	o := entity.Order{
		ID: uuid.New(), ConversationID: "conv-1",
		CompanyName: "Delmar Tooling", EmailAddress: "dana@delmar.com",
		OrderID: 4471, EmailURL: "https://outlook.example/msg-2",
		Status: entity.OrderStatusInTransit, CurrentEstDate: &est,
		LastMessenger: "Dana Delmar", SupportingQuote: "Pushed to July 18",
		CreatedAt: time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
	}

	raw, err := json.Marshal(toDTO(o))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	if got["companyName"] != "Delmar Tooling" || got["orderId"] != float64(4471) ||
		got["status"] != "in_transit" || got["conversationId"] != "conv-1" {
		t.Errorf("dto = %v", got)
	}
	if got["currentEstDate"] != "2026-07-18" {
		t.Errorf("currentEstDate = %v", got["currentEstDate"])
	}
	if got["originalEstDate"] != nil {
		t.Errorf("originalEstDate = %v, want null", got["originalEstDate"])
	}
	if got["dateCreated"] != "2026-07-05T10:00:00Z" {
		t.Errorf("dateCreated = %v", got["dateCreated"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ws/ -v`
Expected: FAIL — `toDTO(o)` falls through to `default` and marshals PascalCase fields, so the camelCase assertions fail.

- [ ] **Step 3: Write minimal implementation**

In `internal/controller/ws/hub.go`, add next to `syncCompletedDTO` (imports gain `"time"` — `entity` is already imported):

```go
// orderDTO is order.updated's wire shape — same camelCase fields the HTTP
// layer serves, mapped here because each edge owns its own DTOs.
type orderDTO struct {
	ID              string  `json:"id"`
	ConversationID  string  `json:"conversationId"`
	CompanyName     string  `json:"companyName"`
	EmailAddress    string  `json:"emailAddress,omitempty"`
	OrderID         int     `json:"orderId"`
	EmailURL        string  `json:"emailUrl,omitempty"`
	Status          string  `json:"status"`
	DateCreated     string  `json:"dateCreated"`
	OriginalEstDate *string `json:"originalEstDate"`
	CurrentEstDate  *string `json:"currentEstDate"`
	LastMessenger   string  `json:"lastMessenger,omitempty"`
	SupportingQuote string  `json:"supportingQuote,omitempty"`
}

func dateString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}
```

Extend `toDTO`:

```go
	case entity.Order:
		return orderDTO{
			ID:              d.ID.String(),
			ConversationID:  d.ConversationID,
			CompanyName:     d.CompanyName,
			EmailAddress:    d.EmailAddress,
			OrderID:         d.OrderID,
			EmailURL:        d.EmailURL,
			Status:          string(d.Status),
			DateCreated:     d.CreatedAt.UTC().Format(time.RFC3339),
			OriginalEstDate: dateString(d.OriginalEstDate),
			CurrentEstDate:  dateString(d.CurrentEstDate),
			LastMessenger:   d.LastMessenger,
			SupportingQuote: d.SupportingQuote,
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ws/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/ws/
git commit -m "feat: order.updated wire DTO on realtime hub"
```

---

### Task 9: controllers — `POST /webhooks/graph` + `POST /subscriptions`

**Files:**
- Create: `internal/controller/http/webhooks.go`
- Create: `internal/controller/http/subscriptions.go`
- Modify: `internal/controller/http/dto.go`
- Modify: `internal/controller/http/router.go`
- Test: `internal/controller/http/webhooks_e2e_test.go`

**Interfaces:**
- Consumes: `messages.HandleChangeMessage` (Task 6), `subscriptions.EnsureSubscription` (Task 7), `TokenSink` (Task 4).
- Produces: routes `POST /webhooks/graph` (NO auth middleware — Graph sends no bearer token; `clientState` is the check) and `POST /subscriptions` (authed). `RouterDeps` gains `Subscriptions *SubscriptionsController` and `Webhooks *WebhooksController`.

- [ ] **Step 1: Write the failing E2E tests**

Create `internal/controller/http/webhooks_e2e_test.go`:

```go
package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
	"github.com/thirdeye/overseer/internal/usecase/messages"
	"github.com/thirdeye/overseer/internal/usecase/subscriptions"
)

// webhookEnv extends the Task-6 style fixture to the full router: real
// middleware + controllers + usecases, fake adapters.
type webhookEnv struct {
	*env
	subs   *testutil.InMemSubscriptions
	tokens *testutil.FakeTokens
}

func newWebhookEnv(t *testing.T) *webhookEnv {
	t.Helper()

	user := entity.User{ID: uuidMust(t), AccountID: uuidMust(t), Email: "op@distributor.com"}
	graph := &testutil.FakeGraph{MessagesByID: map[string]entity.EmailMessage{}}
	llm := &testutil.FakeLLM{Extractions: map[string]entity.OrderExtraction{}}
	repo := testutil.NewInMemOrders()
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
		env:    &env{srv: srv, user: user, graph: graph, llm: llm, repo: repo, events: events},
		subs:   subs,
		tokens: tokens,
	}
}

func uuidMust(t *testing.T) uuid.UUID {
	t.Helper()
	return uuid.New()
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

	// Unauthed → 401 (webhook is the only unauthenticated route).
	respNoAuth, err := http.Post(e.srv.URL+"/subscriptions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed status = %d, want 401", respNoAuth.StatusCode)
	}
}
```

Adjust imports at the top of the file as Go requires (`"encoding/json"`, `"net/http/httptest"`, `"github.com/google/uuid"`, `"github.com/thirdeye/overseer/internal/usecase/orders"`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/controller/http/ -run 'TestWebhook|TestPostSubscriptions' -v`
Expected: FAIL — `WebhooksController`/`SubscriptionsController` undefined.

- [ ] **Step 3: Write the DTOs**

Append to `internal/controller/http/dto.go`:

```go
// graphNotification mirrors one entry of Graph's change-notification POST.
type graphNotification struct {
	SubscriptionID string `json:"subscriptionId"`
	ChangeType     string `json:"changeType"`
	ClientState    string `json:"clientState"`
	ResourceData   struct {
		ID string `json:"id"` // Graph message id
	} `json:"resourceData"`
}

type graphNotificationPayload struct {
	Value []graphNotification `json:"value"`
}

type subscriptionCreatedResponse struct {
	SubscriptionID string `json:"subscriptionId"`
	ExpiresAt      string `json:"expiresAt"`
}
```

- [ ] **Step 4: Write the webhook controller**

Create `internal/controller/http/webhooks.go`:

```go
package http

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/usecase/messages"
)

// WebhooksController receives Graph change notifications. It is NOT behind
// the auth middleware — Graph sends no bearer token; notifications are
// authenticated by the clientState shared secret instead.
type WebhooksController struct {
	Handle *messages.HandleChangeMessage

	// ClientState must match what CreateSubscription registered; anything
	// else is dropped as spoofed.
	ClientState string

	// Dispatch runs jobs off the request path (production: `go fn()`).
	// Never do Graph/LLM work inside the webhook handler.
	Dispatch func(fn func())

	Log interface {
		Error(msg string, args ...any)
		Warn(msg string, args ...any)
		Info(msg string, args ...any)
	}
}

// POST /webhooks/graph — two contracts, one endpoint:
//   - subscription validation: echo validationToken as text/plain 200 within 10s
//   - notifications: enqueue and return 202 immediately
func (h *WebhooksController) PostGraph(c echo.Context) error {
	if token := c.QueryParam("validationToken"); token != "" {
		return c.String(http.StatusOK, token)
	}

	var payload graphNotificationPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid notification payload"})
	}

	for _, n := range payload.Value {
		if n.ClientState != h.ClientState {
			h.Log.Warn("webhook: clientState mismatch, dropping notification",
				"subscriptionId", n.SubscriptionID)
			continue
		}
		in := messages.HandleInput{SubscriptionID: n.SubscriptionID, MessageID: n.ResourceData.ID}
		h.Dispatch(func() {
			// Detached from the request context: the job outlives the 202.
			outcome, err := h.Handle.Execute(context.Background(), in)
			if err != nil {
				h.Log.Error("handle change message failed", "messageId", in.MessageID, "error", err)
				return
			}
			h.Log.Info("change message handled", "messageId", in.MessageID, "outcome", string(outcome))
		})
	}
	return c.NoContent(http.StatusAccepted)
}
```

- [ ] **Step 5: Write the subscriptions controller**

Create `internal/controller/http/subscriptions.go`:

```go
package http

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/usecase/subscriptions"
)

// SubscriptionsController is transport only: parse → call usecase → DTO.
type SubscriptionsController struct {
	Ensure *subscriptions.EnsureSubscription

	Log interface {
		Error(msg string, args ...any)
	}
}

// POST /subscriptions — register the Graph change-notification subscription
// for the authed user (called once after onboarding; re-running replaces it).
func (h *SubscriptionsController) PostSubscriptions(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}

	out, err := h.Ensure.Execute(c.Request().Context(), subscriptions.EnsureInput{
		UserID:     userID,
		GraphToken: auth.GraphToken,
	})
	if err != nil {
		h.Log.Error("create subscription failed", "error", err)
		return c.JSON(http.StatusBadGateway, errorResponse{Error: "failed to create graph subscription"})
	}

	return c.JSON(http.StatusCreated, subscriptionCreatedResponse{
		SubscriptionID: out.GraphSubscriptionID,
		ExpiresAt:      out.ExpiresAt.UTC().Format(time.RFC3339),
	})
}
```

- [ ] **Step 6: Register the routes**

In `internal/controller/http/router.go`, add to `RouterDeps`:

```go
	Subscriptions *SubscriptionsController
	Webhooks      *WebhooksController
```

and after the existing routes:

```go
	e.POST("/subscriptions", d.Subscriptions.PostSubscriptions, auth)
	// No auth middleware: Graph POSTs here directly; clientState is the check.
	e.POST("/webhooks/graph", d.Webhooks.PostGraph)
```

Guard against nil in tests that don't wire these (the existing `orders_e2e_test.go` builds the router without them): register conditionally —

```go
	if d.Subscriptions != nil {
		e.POST("/subscriptions", d.Subscriptions.PostSubscriptions, auth)
	}
	if d.Webhooks != nil {
		// No auth middleware: Graph POSTs here directly; clientState is the check.
		e.POST("/webhooks/graph", d.Webhooks.PostGraph)
	}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/controller/http/ -v`
Expected: PASS (new webhook/subscription tests AND all existing orders E2E tests).

- [ ] **Step 8: Commit**

```bash
git add internal/controller/http/
git commit -m "feat: Graph webhook endpoint and subscription creation API"
```

---

### Task 10: composition root — config, wiring, renewal worker

**Files:**
- Modify: `cmd/overseer/main.go`

**Interfaces:**
- Consumes: everything above.
- Produces: a runnable server with `POST /webhooks/graph`, `POST /subscriptions`, hourly renewal worker, and compile-time port asserts.

- [ ] **Step 1: Extend config**

In `cmd/overseer/main.go`, extend `config` and `loadConfig`:

```go
type config struct {
	Port             string
	DatabaseURL      string
	LLMModel         string
	SeedAccountName  string
	SeedUserEmail    string
	SyncLookbackDays int

	PublicBaseURL    string // where Graph reaches us; must be public https in real use
	GraphClientState string // shared secret in every notification
	SubscriptionTTL  time.Duration
	RenewAhead       time.Duration
	RenewInterval    time.Duration
}
```

```go
		PublicBaseURL:    envOr("PUBLIC_BASE_URL", "http://localhost:8080"),
		GraphClientState: envOr("GRAPH_CLIENT_STATE", "overseer-dev-client-state"),
		SubscriptionTTL:  48 * time.Hour, // Graph caps message subscriptions ~3 days
		RenewAhead:       12 * time.Hour,
		RenewInterval:    time.Hour,
```

- [ ] **Step 2: Extend compile-time asserts**

```go
var (
	_ orders.MessageLister  = (*graph.Client)(nil)
	_ orders.OrderLLM       = (*llm.Gateway)(nil)
	_ orders.OrderRepo      = (*repo.Orders)(nil)
	_ orders.EventPublisher = (*ws.Hub)(nil)

	_ messages.MessageGetter        = (*graph.Client)(nil)
	_ messages.OrderLLM             = (*llm.Gateway)(nil)
	_ messages.OrderRepo            = (*repo.Orders)(nil)
	_ messages.SubscriptionResolver = (*repo.Subscriptions)(nil)
	_ messages.TokenSource          = (*tokencache.Cache)(nil)
	_ messages.EventPublisher       = (*ws.Hub)(nil)

	_ subscriptions.GraphSubscriber  = (*graph.Client)(nil)
	_ subscriptions.SubscriptionRepo = (*repo.Subscriptions)(nil)
	_ subscriptions.TokenSource      = (*tokencache.Cache)(nil)

	_ httpctl.TokenSink = (*tokencache.Cache)(nil)
)
```

Imports gain `"github.com/thirdeye/overseer/internal/tokencache"`, `"github.com/thirdeye/overseer/internal/usecase/messages"`, `"github.com/thirdeye/overseer/internal/usecase/subscriptions"`.

- [ ] **Step 3: Wire adapters, usecases, controllers, worker**

In `main()`, after the existing adapters:

```go
	subsRepo := repo.NewSubscriptions(pool)
	tokens := tokencache.New()
```

After the existing usecases:

```go
	handleChange := &messages.HandleChangeMessage{
		Subs:   subsRepo,
		Tokens: tokens,
		Latest: &messages.GetLatestMessage{Graph: graphClient},
		LLM:    llmGateway,
		Repo:   orderRepo,
		Events: hub,
	}
	ensureSub := &subscriptions.EnsureSubscription{
		Graph:           graphClient,
		Repo:            subsRepo,
		NotificationURL: cfg.PublicBaseURL + "/webhooks/graph",
		ClientState:     cfg.GraphClientState,
		TTL:             cfg.SubscriptionTTL,
		Now:             time.Now,
	}
	renewSubs := &subscriptions.RenewDueSubscriptions{
		Graph:      graphClient,
		Repo:       subsRepo,
		Tokens:     tokens,
		TTL:        cfg.SubscriptionTTL,
		RenewAhead: cfg.RenewAhead,
		Now:        time.Now,
		Log:        log,
	}
```

Update the router deps:

```go
	e := httpctl.NewRouter(httpctl.RouterDeps{
		Resolver: staticResolver{user: seedUser},
		Tokens:   tokens,
		Orders:   ordersController,
		Subscriptions: &httpctl.SubscriptionsController{Ensure: ensureSub, Log: log},
		Webhooks: &httpctl.WebhooksController{
			Handle:      handleChange,
			ClientState: cfg.GraphClientState,
			Dispatch:    func(fn func()) { go fn() },
			Log:         log,
		},
		WS:  hub,
		Log: log,
	})
```

Before `e.Start`, the scheduler worker (spec: the renewal job the scheduler owns):

```go
	// Renewal worker: message subscriptions expire (~3 days max); renew
	// anything entering the RenewAhead window every RenewInterval.
	go func() {
		ticker := time.NewTicker(cfg.RenewInterval)
		defer ticker.Stop()
		for range ticker.C {
			if n, err := renewSubs.Execute(ctx); err != nil {
				log.Error("subscription renewal pass failed", "error", err)
			} else if n > 0 {
				log.Info("subscriptions renewed", "count", n)
			}
		}
	}()
```

- [ ] **Step 4: Full verification**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: everything compiles, vet clean, all packages PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/overseer/main.go
git commit -m "feat: wire post-sync webhook pipeline and renewal worker"
```

---

### Task 11: Live integration run (manual, needs user's MSFT token)

Same harness as Phase 1: `make db-up`, `make run` (sources `.env`), a real Graph token from the user's mailbox, `psql` in the docker container to inspect rows. Real Graph subscriptions need a public HTTPS notification URL, so the webhook leg is driven by **simulated notifications against real message ids** — `HandleChangeMessage` then does a *real* Graph `GetMessage` + *real* LLM extraction.

- [ ] **Step 1: Start the stack**

```bash
cd overseer
make db-up
make run          # in its own terminal; watch JSON logs
```

- [ ] **Step 2: Warm the token cache + baseline orders**

```bash
TOKEN='<paste Graph access token>'
curl -s -X POST localhost:8080/sync -H "Authorization: Bearer $TOKEN"
# wait for sync.completed in the logs, then:
curl -s localhost:8080/orders -H "Authorization: Bearer $TOKEN" | python3 -m json.tool
```

- [ ] **Step 3: Seed a subscription row (simulating POST /subscriptions without a public URL)**

```bash
docker compose exec postgres psql -U postgres -d overseer -c \
  "INSERT INTO graph_subscriptions (user_id, graph_subscription_id, expires_at)
   SELECT id, 'sub-local-test', now() + interval '2 days' FROM users LIMIT 1
   ON CONFLICT (user_id) DO UPDATE SET graph_subscription_id = 'sub-local-test';"
```

- [ ] **Step 4: Validation handshake check**

```bash
curl -si -X POST 'localhost:8080/webhooks/graph?validationToken=abc123' | head -5
# expect: HTTP/1.1 200, Content-Type text/plain, body abc123
```

- [ ] **Step 5: Fire a simulated notification for a real message**

Pick a real supplier-thread message id (e.g. newest message):

```bash
MSG_ID=$(curl -s 'https://graph.microsoft.com/v1.0/me/messages?$top=1&$select=id,subject' \
  -H "Authorization: Bearer $TOKEN" | python3 -c 'import json,sys; print(json.load(sys.stdin)["value"][0]["id"])')

curl -si -X POST localhost:8080/webhooks/graph \
  -H 'Content-Type: application/json' \
  -d "{\"value\":[{\"subscriptionId\":\"sub-local-test\",\"changeType\":\"created\",\"clientState\":\"overseer-dev-client-state\",\"resourceData\":{\"id\":\"$MSG_ID\"}}]}"
# expect: 202; then a "change message handled" log line with outcome created/updated/skipped_not_order
```

- [ ] **Step 6: Verify DB + realtime**

```bash
docker compose exec postgres psql -U postgres -d overseer -c \
  "SELECT company_name, status, current_est_date, last_messenger, updated_at FROM orders ORDER BY updated_at DESC LIMIT 5;"
```

For the ws event, connect before Step 5 (e.g. `npx wscat -c ws://localhost:8080/ws -H "Authorization: Bearer $TOKEN"`) and watch for `{"type":"order.updated",...}`.

- [ ] **Step 7: Negative checks**

- Send the same notification twice → second run must log `updated` and not add a row (`SELECT count(*) FROM orders`).
- Send with `"clientState":"wrong"` → 202 but a `clientState mismatch` warn log and no processing.
- Send a message id from a non-order email (newsletter) in a *new* conversation → outcome `skipped_not_order`, no new row.

---

### Task 12: Building-Journal overview

- [ ] Write `Building-Journal/OrderList_PostSync_Webhooks/OrderList_PostSync_Webhooks.md` — condensed architecture overview in the same voice/structure as `SupplierList_Sync_Onboarding.md` (What was built / design decisions one-liners / where things live / request walkthrough / what's proven vs deferred). Note: `Building-Journal` content is local-only per existing convention (`PHASE1_WALKTHROUGH*` and `.env` are gitignored; keep the same posture).

---

## Self-Review

**Spec coverage** (Second Phase, Post-Sync section):
- `POST controller/webhooks/graph` + validationToken echo + enqueue-and-202 → Task 9. ✓
- Subscription expiry (~3 days) + renewal job on the scheduler worker → Tasks 2, 7, 10. ✓
- Idempotent `jobs/HandleChangeMessage` (upsert keyed on message's conversation, at-least-once safe) → Task 6. ✓
- `gateway/graph/GetMessage` beside `ListMessages` → Task 1. ✓
- `usecase/messages/GetLatestMessage` with required params ($select matches Phase 1's field list) → Tasks 1, 6. ✓
- `order.updated` over the shared `/ws` with the spec's payload fields → Task 8. ✓
- Reply detection for Checkups' `CancelIfReply` → explicitly deferred (Checkups not built); extension-point comment in Task 6. ✓
- On-reload behavior ("load all new emails") → already served by `GET /orders` (Phase 1). ✓

**Type consistency check:** `GetByConversation(ctx, uuid.UUID, string) (entity.Order, bool, error)` — defined in Task 3, consumed by Task 6 port and fixture. `CreateSubscription(ctx, token, notificationURL, clientState string, expires time.Time) (string, error)` — Task 2 produces, Task 3 fake + Task 7 port match. `HandleInput`/`HandleOutcome` — Task 6 produces, Task 9 consumes. `TokenSink.Put(uuid.UUID, string)` — Task 4 produces, `FakeTokens` (Task 3) satisfies both `TokenSink` and `TokenSource`. ✓
