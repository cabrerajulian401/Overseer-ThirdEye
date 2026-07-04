# Overseer — Go Code Organization (Clean Architecture)

How the Go backend is laid out. Follows the layering rule from the tech spec:
`controller / worker → usecase → gateway / repo`, with **entities at the center**.
Companion docs: [tech_spec.md](../tech_spec.md), [ARCHITECTURE.md](../ARCHITECTURE.md).

## Directory Layout

```
overseer/
├── cmd/
│   └── overseer/
│       └── main.go               # composition root: config, DB pool, wire gateways → usecases → controllers/workers, start HTTP + scheduler
├── internal/
│   ├── entity/                   # ── INNERMOST RING ──  domain types, imported by every layer, imports nothing
│   │   ├── account.go            # Account, User, GraphSubscription
│   │   ├── order.go              # Order, OrderStatus
│   │   ├── checkup.go            # CheckupMessage, CheckupStatus
│   │   ├── discrepancy.go        # Discrepancy, DiscrepancyStatus, Priority, DiscrepancyDetail
│   │   ├── ottoscan.go           # OttoScan, ScanStatus, ExtractedFields
│   │   ├── message.go            # EmailMessage, Thread (Graph data passed between layers)
│   │   └── event.go              # realtime Event envelope + event type constants
│   ├── usecase/                  # ── APPLICATION RING ──  business logic; defines the interfaces it needs (ports)
│   │   ├── orders/
│   │   │   ├── ports.go          # MessageLister, OrderExtractor, OrderRepo, EventPublisher... (interfaces)
│   │   │   ├── build_active_order_list.go
│   │   │   ├── build_active_order_list_test.go   # unit test — every usecase file gets a sibling _test.go
│   │   │   ├── format_initial_order_data.go
│   │   │   └── catch_discrepancies.go
│   │   ├── otto/
│   │   │   ├── ports.go
│   │   │   ├── resolve_destinations.go   # the agent loop: extract → drive search → rank
│   │   │   └── record_order.go
│   │   ├── checkups/
│   │   │   ├── ports.go
│   │   │   ├── create_checkup_messages.go
│   │   │   ├── send_due_checkups.go
│   │   │   └── update_status.go
│   │   └── messages/
│   │       └── get_latest_message.go
│   ├── gateway/                  # ── ADAPTER RING ──  external services; no DB access, no business logic
│   │   ├── graph/                # ListMessages, GetMessage, SendMail, subscriptions
│   │   ├── drive/                # OneDrive search
│   │   ├── excel/                # workbook table-row append
│   │   └── llm/                  # FilterOrderMessages, ExtractOrderFields, RankDestinations,
│   │                             #   PlanSheetAppend, FindDiscrepancies — prompts, retries,
│   │                             #   output validation, cost logging all live HERE
│   ├── repo/                     # ── ADAPTER RING ──  Postgres; implements usecase repo ports
│   │   ├── migrations/           # numbered SQL files (embed.FS), DDL matches tech_spec.md Schema Updates
│   │   │   ├── 0001_accounts_users.sql       # accounts, users, graph_subscriptions
│   │   │   ├── 0002_orders.sql               # orders + idx_orders_conversation
│   │   │   ├── 0003_checkup_messages.sql     # checkup_messages + idx_checkups_due
│   │   │   ├── 0004_discrepancies.sql
│   │   │   └── 0005_otto_scans.sql
│   │   ├── migrate.go            # go:embed migrations/*.sql; runs pending migrations at startup
│   │   ├── orders.go             # one file per table: SQL consts + methods + row↔entity mapping
│   │   ├── checkups.go
│   │   ├── discrepancies.go
│   │   ├── ottoscans.go
│   │   └── users.go              # users, accounts, graph_subscriptions
│   ├── controller/               # ── ENTRYPOINT RING ──  transport only; parse → call usecase → map to DTO
│   │   ├── http/
│   │   │   ├── router.go
│   │   │   ├── orders.go         # GET /orders, POST /sync
│   │   │   ├── otto.go           # POST /otto/scans, POST /otto/scans/{scanId}/record
│   │   │   ├── checkups.go       # POST /checkups, PATCH /checkups/{checkupId}
│   │   │   ├── webhooks.go       # POST /webhooks/graph (validationToken echo, enqueue, 202)
│   │   │   ├── dto.go            # camelCase JSON request/response shapes
│   │   │   ├── orders_e2e_test.go    # E2E: real router+usecases, faked gateways/repos
│   │   │   ├── otto_e2e_test.go      # one E2E file per controller file
│   │   │   ├── checkups_e2e_test.go
│   │   │   └── webhooks_e2e_test.go
│   │   └── ws/
│   │       └── hub.go            # GET /ws — the single realtime channel; broadcasts entity.Event
│   ├── worker/                   # ── ENTRYPOINT RING ──  non-HTTP triggers; call usecases like controllers do
│   │   ├── scheduler.go          # 1-min ticker → SendDueCheckups + subscription renewal
│   │   └── handle_change_message.go  # webhook job: extract → upsert → audit → cancel-on-reply
│   └── testutil/                 # shared fakes for E2E tests: FakeGraph, FakeLLM, FakeDrive,
│       └── fakes.go              #   FakeExcel, in-memory repos, EventRecorder (captures /ws pushes)
└── go.mod
```

## Import Rules (the dependency arrows)

| Package | May import | Must NOT import |
|---|---|---|
| `entity` | stdlib, `uuid` | anything else — it is the center |
| `usecase/*` | `entity` | gateway, repo, controller, worker, net/http, sql |
| `gateway/*`, `repo/*` | `entity` (+ their SDK/driver) | usecase¹, controller, worker |
| `controller/*`, `worker/*` | `usecase`, `entity` | gateway, repo (talk through usecases) |
| `cmd/overseer` | everything | — (it's the wiring) |

¹ Go interfaces are satisfied **structurally**, so adapters never import the usecase
package that defines the port. Compile-time conformance is asserted in `main.go`:
`var _ orders.OrderRepo = (*repo.Orders)(nil)`.

**Ports live where they are consumed.** Each usecase package declares the small
interfaces it needs in its own `ports.go` (accept interfaces, return structs).
No central `interfaces/` package.

## Entities (`internal/entity`)

Defined once at the repo level; every layer shares these types, so a field rename
is a compile error everywhere instead of a runtime surprise. Entities carry **no**
JSON/DB tags — controllers map to DTOs (camelCase), repos map to columns (snake_case).

```go
// entity/order.go
package entity

import (
	"time"

	"github.com/google/uuid"
)

type OrderStatus string

const (
	OrderStatusInTransit OrderStatus = "in_transit"
	OrderStatusConfirmed OrderStatus = "confirmed"
	OrderStatusDelivered OrderStatus = "delivered"
	OrderStatusIssue     OrderStatus = "issue"
)

type Order struct {
	ID              uuid.UUID
	AccountID       uuid.UUID
	UserID          uuid.UUID
	ConversationID  string      // Graph conversation id — webhook/audit lookup key
	CompanyName     string
	EmailAddress    string
	OrderID         int         // supplier's PO number (business id), NOT the row PK
	EmailURL        string
	Status          OrderStatus
	OriginalEstDate *time.Time  // pointer = nullable column
	CurrentEstDate  *time.Time
	LastMessenger   string
	SupportingQuote string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
```

```go
// entity/checkup.go
type CheckupStatus string

const (
	CheckupPending   CheckupStatus = "pending"   // scheduled, will send
	CheckupPaused    CheckupStatus = "paused"    // toggle off; resumable
	CheckupSent      CheckupStatus = "sent"      // terminal
	CheckupCancelled CheckupStatus = "cancelled" // terminal (user or CancelIfReply)
)

type CheckupMessage struct {
	ID             uuid.UUID // this IS the CheckupID that PATCH /checkups/{id} targets
	AccountID      uuid.UUID
	UserID         uuid.UUID
	FlowID         uuid.UUID // groups messages minted by one POST /checkups
	ConversationID string
	CompanyName    string
	EmailAddress   string
	OrderID        int
	Status         CheckupStatus
	Subject        string
	Body           string
	SendDate       time.Time
	CurrentEstDate *time.Time
	CancelIfReply  bool
	RepeatFlow     int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
```

```go
// entity/discrepancy.go
type DiscrepancyStatus string

const (
	DiscrepancyOpen      DiscrepancyStatus = "open"
	DiscrepancyResolved  DiscrepancyStatus = "resolved"
	DiscrepancyDismissed DiscrepancyStatus = "dismissed"
)

type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

type MetricType string

const (
	MetricPrice MetricType = "price"
	MetricDate  MetricType = "date"
	MetricUnit  MetricType = "unit"
)

type DiscrepancyDetail struct {
	PreviouslyAgreed string
	NewMetric        string
	Type             MetricType
}

type DiscrepancyQuote struct {
	Quote       string
	MessageLink string
}

type Discrepancy struct {
	ID               uuid.UUID
	AccountID        uuid.UUID
	UserID           uuid.UUID
	ConversationID   string
	Status           DiscrepancyStatus
	CompanyName      string
	EmailAddress     string
	EmailDeeplinkURL string
	OrderID          int
	Priority         Priority
	Title            string
	Detail           DiscrepancyDetail
	Quotes           []DiscrepancyQuote
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
```

```go
// entity/ottoscan.go
type ScanStatus string

const (
	ScanScanned   ScanStatus = "scanned"
	ScanRecorded  ScanStatus = "recorded"
	ScanAbandoned ScanStatus = "abandoned"
)

// ExtractedFields is what gateway/llm.ExtractOrderFields returns and what the
// confirm card edits; RecordOrder writes the user-approved version.
type ExtractedFields struct {
	Supplier     string
	CompanyName  string
	OrderID      int
	Price        string
	EstDate      *time.Time
	Units        string
}

type OttoScan struct {
	ID               uuid.UUID // the scanId used in controller paths
	AccountID        uuid.UUID
	UserID           uuid.UUID
	MessageID        string // Graph message id
	ConversationID   string
	Fields           ExtractedFields
	SelectedResource string // set at record time
	Status           ScanStatus
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
```

```go
// entity/account.go
type Account struct {
	ID          uuid.UUID
	CompanyName string
	CreatedAt   time.Time
}

type User struct {
	ID                   uuid.UUID
	AccountID            uuid.UUID
	Email                string
	GraphRefreshTokenEnc string // encrypted at rest
	CreatedAt            time.Time
}

type GraphSubscription struct {
	ID                  uuid.UUID
	UserID              uuid.UUID
	GraphSubscriptionID string
	ExpiresAt           time.Time // renewal job reads this (~3-day max lifetime)
	CreatedAt           time.Time
}
```

```go
// entity/event.go — the typed realtime envelope pushed over GET /ws
type EventType string

const (
	EventOrderUpdated     EventType = "order.updated"
	EventDiscrepancyFound EventType = "discrepancy.found"
	EventSyncCompleted    EventType = "sync.completed"
)

type Event struct {
	Type EventType
	Data any // entity.Order, entity.Discrepancy, ... — controller/ws serializes
}
```

## Ports Pattern (example)

```go
// usecase/checkups/ports.go — the usecase declares exactly what it needs
package checkups

type MailSender interface {
	SendMail(ctx context.Context, userID uuid.UUID, to, subject, body string) error
}

type CheckupRepo interface {
	ClaimDue(ctx context.Context, now time.Time) ([]entity.CheckupMessage, error) // pending & send_date <= now, atomically marked 'sending'
	MarkSent(ctx context.Context, id uuid.UUID) error
	UpdateStatus(ctx context.Context, id uuid.UUID, s entity.CheckupStatus) error
}
```

```go
// usecase/checkups/send_due_checkups.go — depends only on the ports above
type SendDueCheckups struct {
	Repo CheckupRepo
	Mail MailSender
}

func (uc *SendDueCheckups) Execute(ctx context.Context) error { ... }
```

`gateway/graph` implements `MailSender`, `repo/checkups.go` implements
`CheckupRepo` — neither imports the `checkups` package. `cmd/overseer/main.go`
wires them together. Usecase tests pass stubs; no Graph, no Postgres, no LLM.

## Repo Layer: Migrations & Queries

The repo layer owns everything SQL: the schema (migrations) and every query.
No SQL anywhere else — usecases speak entities, controllers speak DTOs.

**Migrations** live in `repo/migrations/` as numbered, append-only files
(never edit an applied migration; add a new one). They are embedded into the
binary and applied at startup, so a deploy can never run against a stale schema:

```go
// repo/migrate.go
package repo

import "embed"

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies pending migrations in filename order (goose/golang-migrate,
// or a ~30-line hand-rolled version tracking applied files in a schema_migrations table).
func Migrate(ctx context.Context, db *pgxpool.Pool) error { ... }
```

The DDL inside each file is copied verbatim from tech_spec.md → Schema Updates
(that section stays the source of truth; migrations are its executable form).

**Queries** live as `const` SQL next to the methods that use them — one file per
table, each file implementing the usecase ports that touch that table. Every
query filters by `user_id` (and `account_id`) from auth context; a query without
an owner filter is a bug:

```go
// repo/orders.go
package repo

const upsertOrderSQL = `
	INSERT INTO orders (id, account_id, user_id, conversation_id, company_name,
	                    email_address, order_id, email_url, status,
	                    original_est_date, current_est_date, last_messenger, supporting_quote)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	ON CONFLICT (user_id, conversation_id) DO UPDATE SET
		status = EXCLUDED.status,
		current_est_date = EXCLUDED.current_est_date,
		last_messenger = EXCLUDED.last_messenger,
		supporting_quote = EXCLUDED.supporting_quote,
		updated_at = now()`                            // idempotent — Graph re-delivers webhooks

const listOrdersSQL = `
	SELECT ... FROM orders
	WHERE user_id = $1 AND ($2::text IS NULL OR status = $2)
	  AND created_at < $3                              -- cursor: created_at of last row on prev page
	ORDER BY created_at DESC
	LIMIT $4`                                          // GET /orders?status&cursor&limit

const getOrderByConversationSQL = `
	SELECT ... FROM orders
	WHERE user_id = $1 AND conversation_id = $2`       -- the webhook-job lookup (idx_orders_conversation)

type Orders struct{ db *pgxpool.Pool }

func (r *Orders) Upsert(ctx context.Context, o entity.Order) error { ... }
// each method: run query → scan rows → return entity.Order (mapping lives here, nowhere else)
```

```go
// repo/checkups.go — the scheduler's claim query, race-safe across workers
const claimDueSQL = `
	UPDATE checkup_messages SET status = 'sending', updated_at = now()
	WHERE id IN (
		SELECT id FROM checkup_messages
		WHERE status = 'pending' AND send_date <= $1   -- <=, never = (uses idx_checkups_due)
		FOR UPDATE SKIP LOCKED
	)
	RETURNING id, account_id, user_id, flow_id, ...`   // two workers can't double-send

const cancelPendingByConversationSQL = `
	UPDATE checkup_messages SET status = 'cancelled', updated_at = now()
	WHERE user_id = $1 AND conversation_id = $2
	  AND status = 'pending' AND cancel_if_reply`      // HandleChangeMessage's reply-detection hook
```

Query inventory each repo file must cover (derived from the usecase ports):

| Repo file | Queries |
|---|---|
| `orders.go` | upsert (by user+conversation), list w/ status+cursor, get by conversation |
| `checkups.go` | insert batch (one flow → N rows), claim due, mark sent, update status, cancel pending by conversation |
| `discrepancies.go` | insert, list open by user, update status |
| `ottoscans.go` | insert, get by id, set selected_resource + status |
| `users.go` | get by id/email, save refresh token, subscription upsert, list subscriptions expiring before t |

Hand-written SQL + `pgx` is the MVP default. If query count grows, `sqlc` is the
upgrade path — it generates this same layer from the SQL files without changing
any usecase code, because the ports don't know how queries are executed.

## Testing

Two tiers, matching the two rings that contain logic. The ports pattern is what
makes both cheap: every external dependency (Graph, LLM, Postgres, the clock)
enters through an interface a test can replace. **No test ever calls a real
external service.**

| Tier | Scope | Real | Faked |
|---|---|---|---|
| **Usecase unit tests** | one usecase's business rules | the usecase | its ports — tiny hand-written stubs |
| **Controller E2E tests** | HTTP request → HTTP response | router, middleware, controllers, DTOs, usecases | only the outermost adapters: gateways + repos (`testutil` fakes) |

### Tier 1 — usecase unit tests (one per usecase)

Sibling `_test.go` in the same package, table-driven, stubbing ports by hand
(they're 1–3 methods each — no mocking framework needed). Run in milliseconds.

```go
// usecase/checkups/send_due_checkups_test.go
type stubRepo struct {
	due    []entity.CheckupMessage
	sent   []uuid.UUID
	claims int
}
func (s *stubRepo) ClaimDue(_ context.Context, _ time.Time) ([]entity.CheckupMessage, error) {
	s.claims++
	return s.due, nil
}
func (s *stubRepo) MarkSent(_ context.Context, id uuid.UUID) error { s.sent = append(s.sent, id); return nil }

type stubMail struct{ calls []string; failOn string }
func (s *stubMail) SendMail(_ context.Context, _ uuid.UUID, to, _, _ string) error {
	s.calls = append(s.calls, to)
	if to == s.failOn { return errors.New("graph 503") }
	return nil
}

func TestSendDueCheckups_OneFailureDoesNotStopBatch(t *testing.T) {
	repo := &stubRepo{due: []entity.CheckupMessage{msgTo("a@x.com"), msgTo("b@x.com")}}
	mail := &stubMail{failOn: "a@x.com"}
	uc := &SendDueCheckups{Repo: repo, Mail: mail}

	err := uc.Execute(context.Background())

	require.NoError(t, err)              // batch-level: log & continue
	require.Len(t, mail.calls, 2)        // b@x.com still attempted
	require.Len(t, repo.sent, 1)         // only the success marked sent
}
```

Test the clock by injecting it as a port too (`Now func() time.Time` field or a
`Clock` interface) — never call `time.Now()` inside a usecase you want to test.

**Must-cover checklist** (the business rules from the tech spec — each row is
the minimum table for that usecase's test):

| Usecase | Cases the test must cover |
|---|---|
| `BuildActiveOrderList` | non-order threads filtered out; upsert idempotent; LLM error → no partial writes |
| `FormatInitialOrderData` | malformed LLM output surfaces as error, never a half-mapped entity |
| `ResolveDestinations` | loop order (extract → search → rank); scan stored before returning; returns ≤ 3 destinations |
| `RecordOrder` | writes the **user-approved** fields, never re-extracted ones; marks scan `recorded`; returns webUrl; unknown scanId → not-found |
| `CreateCheckupMessages` | cadence expands to N rows sharing one `flow_id`; server mints IDs; SendDates computed off cadence |
| `SendDueCheckups` | claims only `pending` with `send_date <= now` (not `==`); one send failure doesn't stop the batch; nothing due → no sends |
| `UpdateStatus` | legal transitions only — `sent`/`cancelled` are terminal, `paused ↔ pending` allowed |
| `CatchDiscrepancies` | LLM finds nothing → no row, no event; finds one → row stored **and** `discrepancy.found` published |
| `HandleChangeMessage` (worker) | duplicate Graph delivery → single upsert (idempotent); reply cancels pending `cancel_if_reply` rows; publishes `order.updated` |

### Tier 2 — controller E2E tests (HTTP in → HTTP out)

`httptest.NewServer` over the **real** router with real middleware, controllers,
and usecases — only gateways and repos are `testutil` fakes. Each test sends an
actual HTTP request and asserts three things: status code, JSON body, and the
side effects the fakes recorded.

```go
// controller/http/otto_e2e_test.go
func TestPostOttoScans_E2E(t *testing.T) {
	graph := testutil.NewFakeGraph(testutil.WithThread("conv-1", threadFixture))
	llm := testutil.NewFakeLLM(
		testutil.ExtractReturns(entity.ExtractedFields{CompanyName: "Delmar Tooling", OrderID: 4471}),
		testutil.RankReturns(threeDestinations),
	)
	srv := httptest.NewServer(testutil.NewRouter(t, graph, llm)) // wires real usecases + in-mem repos
	defer srv.Close()

	resp := testutil.Post(t, srv, "/otto/scans",
		`{"messageId":"m-1","conversationId":"conv-1"}`, testutil.AsUser(userA))

	require.Equal(t, http.StatusOK, resp.Code)
	require.Len(t, resp.JSON("destinations"), 3)          // top-3 on the wire, camelCase
	require.NotEmpty(t, resp.JSON("scanId"))
	require.Equal(t, 1, llm.ExtractCalls)                 // side effects: the calls that
	require.Equal(t, []string{"conv-1"}, graph.ThreadsFetched) //   WOULD have hit Graph/LLM
}
```

Every endpoint gets an E2E file; the non-negotiable cases:

| Endpoint | E2E cases |
|---|---|
| `GET /orders` | 200 + camelCase array + `nextCursor`; `?status=` filters; no/invalid token → 401 |
| `POST /sync` | 202 + `syncId`; second call doesn't double-seed; `sync.completed` recorded by EventRecorder |
| `POST /otto/scans` | happy path above; LLM validation failure → 502-style error, no scan row leaked |
| `POST /otto/scans/{id}/record` | FakeExcel received the **edited** fields from the body; unknown id → 404; already-recorded → 409 |
| `POST /checkups` | response contains server-minted ids; N rows created; client-sent IDs ignored |
| `PATCH /checkups/{id}` | pause/resume/cancel transitions; patching a `sent` row → 409 |
| `POST /webhooks/graph` | `?validationToken=x` → 200 with `x` in body within the handler (no job run); real notification → 202 **before** any Graph/LLM fake is touched, job enqueued |
| auth (all) | `userId` comes from the token — a userId smuggled into the body is ignored (assert against fake repo rows) |

Fakes live in `internal/testutil` and are shared across E2E files. In-memory
repos are the MVP default; the upgrade path is swapping them for a real Postgres
via testcontainers (which then also exercises `repo/` SQL and migrations) —
nothing else in the tests changes.

`go test ./...` runs both tiers; neither needs Docker, credentials, or network.

## Conventions

- **Casing:** `PascalCase` Go types · `snake_case` DB columns (repo layer) · `camelCase` JSON (controller DTOs).
- **Enums are typed strings** (`OrderStatus`, not bare `string`) matching the DB `CHECK` constraints — one vocabulary from Postgres to the wire.
- **Nullable DB columns → pointer fields** on entities.
- **One usecase = one file = one `Execute` method**; controllers/workers stay thin (parse → call → map).
- **`gateway/llm` methods are task-shaped** and each is exactly one model call; multi-step LLM flows are usecase-owned loops.
- MVP shortcut allowed: skip DTOs and put `json` tags on entities *only until a second client needs a different shape* — but never put transport tags and DB tags on the same struct.
