# Dashboard 2 Migration — Plan 1 of 4: Core Domain & Ingestion

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remodel the Overseer domain to the Dashboard 2 spec's core — vendors as identity, multi-line orders with timeline/documents, PO-based matching with a Needs Review fallback, and the new board API — so the board fills itself from email.

**Architecture:** Evolve the existing clean-architecture monolith in place: new/changed entities at the center, breaking dbmate migrations, rewritten `orders`/`messages` usecases, new `vendors`/`review` usecase packages, controllers on the existing echo router, events on the existing `/ws` hub. Legacy Otto scan path is retired early so nothing compiles against the old extraction shape.

**Tech Stack:** Go, echo, pgx, dbmate migrations (`overseer/db/migrations/`), Anthropic SDK behind `gateway/llm`, two-tier tests (usecase unit tests with hand stubs + controller E2E over `testutil` fakes).

## Global Constraints

- Spec: `docs/tech_spec_dashboard2.md` — field names, enums, and rules there are authoritative.
- Layering never breaks: `controller / worker → usecase → gateway / repo`; ports live in the consuming usecase package; adapters never import usecases.
- Every table carries `tenant_id` + `user_id`; every query filters by them from auth context. (`accounts` renames to `tenants` in the first Dashboard-2 migration — Task 1; Go entities use `TenantID`. Auth **transport** is untouched in this plan — session-cookie auth per `In_Prog_Tech_Spec.md` lands in Plan 4.)
- Casing: `PascalCase` Go, `snake_case` DB, `camelCase` JSON. Enums are typed strings matching DB `CHECK` constraints.
- Breaking migrations allowed (no production data). Migrations are append-only numbered files in `overseer/db/migrations/`, DDL copied from the spec's Schema Updates.
- Money is `float64` in Go / `NUMERIC(12,2)` in Postgres for this MVP (single user, display-centric; revisit if math compounds). Dates that are calendar dates use `*time.Time` + DATE columns.
- Constants: `StallDays = 21`, `ArchiveAfterDays = 30`, timeline kinds `email|stage|flag|checkup|edit`.
- No test calls a real external service. `go test ./...` green at the end of every task.
- Run all commands from `overseer/`.

## Phase Roadmap (this document = Plan 1)

| Plan | Scope (spec sections) | Depends on |
|---|---|---|
| **1 — Core Domain & Ingestion (this plan)** | Vendors + unknown senders · orders remodel (items/documents/timeline/stage) · legacy Otto retirement · LLM triage/extraction reshape · matching pipeline (`HandleChangeMessage`) · Needs Review · sync rebuild with progress · board API (`GET/PATCH /orders`, detail) · auto-archive worker · WS event additions | — |
| **2 — Otto & Sheet Connections** | `extractions` queue + issues engine · workbooks + per-workbook mappings + `connections(sheet)` · deterministic mapped writes (excel gateway growth: ReadRows/UpdateRow/DeleteTableRow/CreateWorkbook/ListTabs) · accept/reject/restore/retry · pipeline + assign start creating extraction rows | 1 |
| **3 — Checkups Remodel** | `checkups`/`checkup_orders`/`checkup_events` replace `checkup_messages` · 3 trigger types + send window + cap + awaiting_manual_check · reply detection in the pipeline · send/skip/coverage gaps · scheduler rework | 1 (2 for none) |
| **4 — Audit, Contact List, Auth & Accounts, Inbox Connection** | `flags` remodel + accept fan-out + dispute lifecycle (pauses checkups → needs 3) · impact/exposure/vendor patterns · Contact List endpoints · session-cookie auth per `In_Prog_Tech_Spec.md` Flows A–D (login/callback/consent/logout, `user_logins`/`sessions`/`msal_token_cache`, `resolve_login`, `GetAccessToken(home_account_id)`, `needs_reconsent`, lifecycle webhook) · `/user/me` + onboarding + tutorial · consent-triggered sync · `connections(inbox)` + backfill | 1, 2, 3 |

Plans 2–4 are written (full task detail, this format) after the preceding plan lands and is reviewed. Deliberate Plan-1 shims, resolved later: pipeline and Needs Review **assign** do not yet create extraction rows (Plan 2); board `atStake`/`openFlagCount`/`contested` return zero/false (Plan 4 remodels flags — the old `discrepancies` table keeps working behind a tiny type shim); `POST /sync` consent-gating (and consent-triggered auto-sync) lands with auth in Plan 4; `CancelPendingByConversation` reply-cancel survives untouched until Plan 3 replaces it.

---

### Task 1: Vendor entities + schema + repo

**Files:**
- Create: `internal/entity/vendor.go`
- Create: `db/migrations/20260809000007_vendors.sql`
- Create: `internal/repo/vendors.go`
- Modify: `internal/testutil/fakes.go` (add in-memory vendors repo)
- Test: `internal/entity/vendor_test.go`

**Interfaces:**
- Consumes: `entity` conventions (uuid PKs, typed-string enums), `repo/db.go` pool.
- Produces: `entity.Vendor{ID, TenantID, UserID uuid.UUID, VendorName, ContactName string, LastActivityDate *time.Time, CreatedAt, UpdatedAt time.Time}` · `entity.VendorEmail{ID, UserID, VendorID uuid.UUID, Email string, CreatedAt time.Time}` · `entity.UnknownSender{ID, TenantID, UserID uuid.UUID, Email string, MessageCount int, Status entity.UnknownSenderStatus, CreatedAt, UpdatedAt time.Time}` with `UnknownSenderStatus ∈ pending|attached|ignored` + `ValidUnknownSenderStatus(s) bool` · repo methods `CreateVendor`, `AttachEmail`, `GetVendorBySenderEmail(ctx, userID, email) (entity.Vendor, bool, error)`, `ListVendors`, `UpsertUnknownSender(ctx, s) error` (insert or `message_count+1`), `SetUnknownSenderStatus`.

- [ ] **Step 1: Write the failing entity test**

```go
// internal/entity/vendor_test.go
package entity

import "testing"

func TestValidUnknownSenderStatus(t *testing.T) {
	for _, ok := range []UnknownSenderStatus{UnknownSenderPending, UnknownSenderAttached, UnknownSenderIgnored} {
		if !ValidUnknownSenderStatus(ok) {
			t.Fatalf("%s should be valid", ok)
		}
	}
	if ValidUnknownSenderStatus("added") { // old vocabulary must not pass
		t.Fatal("'added' is not a valid status")
	}
}
```

- [ ] **Step 2: Run it — expect FAIL (undefined types)**

Run: `go test ./internal/entity/ -run TestValidUnknownSenderStatus`
Expected: compile error `undefined: UnknownSenderStatus`

- [ ] **Step 3: Implement `entity/vendor.go`**

```go
package entity

import (
	"time"

	"github.com/google/uuid"
)

type UnknownSenderStatus string

const (
	UnknownSenderPending  UnknownSenderStatus = "pending"
	UnknownSenderAttached UnknownSenderStatus = "attached"
	UnknownSenderIgnored  UnknownSenderStatus = "ignored"
)

func ValidUnknownSenderStatus(s UnknownSenderStatus) bool {
	switch s {
	case UnknownSenderPending, UnknownSenderAttached, UnknownSenderIgnored:
		return true
	}
	return false
}

type Vendor struct {
	ID               uuid.UUID
	TenantID        uuid.UUID
	UserID           uuid.UUID
	VendorName       string
	ContactName      string
	LastActivityDate *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type VendorEmail struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	VendorID  uuid.UUID
	Email     string
	CreatedAt time.Time
}

type UnknownSender struct {
	ID           uuid.UUID
	TenantID    uuid.UUID
	UserID       uuid.UUID
	Email        string
	MessageCount int
	Status       UnknownSenderStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
```

- [ ] **Step 4: Run test — expect PASS**

Run: `go test ./internal/entity/ -run TestValidUnknownSenderStatus`

- [ ] **Step 5: Write the migrations** — two files:
    1. `db/migrations/20260809000006_tenants_rename.sql` — the spec's identity rename, schema part only: `ALTER TABLE accounts RENAME TO tenants;` + `RENAME COLUMN company_name TO name;` + `ADD COLUMN industry TEXT, ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();` + `ALTER TABLE users RENAME COLUMN account_id TO tenant_id;` + `ALTER TABLE users ALTER COLUMN tenant_id DROP NOT NULL;` (down reverses). The rest of the identity work (`user_logins`, `sessions`, `msal_token_cache`, session auth) is **Plan 4** — do not create those tables here. Update `repo/users.go` + `entity/account.go` (`Account` → `Tenant`, `TenantID` fields) so the build stays green.
    2. `db/migrations/20260809000007_vendors.sql` — copy the `vendors`, `vendor_emails`, `unknown_senders` DDL verbatim from `docs/tech_spec_dashboard2.md` → Schema Updates, with dbmate's `-- migrate:up` / `-- migrate:down` markers (down: `DROP TABLE unknown_senders; DROP TABLE vendor_emails; DROP TABLE vendors;`).

- [ ] **Step 6: Implement `repo/vendors.go`** — follow `repo/orders.go` style: `const` SQL + methods + row↔entity mapping. Queries: insert vendor; insert vendor_email; `SELECT v.* FROM vendors v JOIN vendor_emails e ON e.vendor_id = v.id WHERE e.user_id = $1 AND lower(e.email) = lower($2)` for `GetVendorBySenderEmail` (returns `found=false` on no rows, not an error); list by user ordered by `vendor_name`; `INSERT … ON CONFLICT (user_id, email) DO UPDATE SET message_count = unknown_senders.message_count + 1, updated_at = now()` for `UpsertUnknownSender`; status update.

- [ ] **Step 7: Add `testutil.FakeVendors`** — in-memory maps mirroring those six methods (the E2E router wiring picks it up in later tasks).

- [ ] **Step 8: Verify build + all tests, commit**

Run: `go build ./... && go test ./...`
```bash
git add internal/entity/vendor.go internal/entity/vendor_test.go db/migrations/20260809000007_vendors.sql internal/repo/vendors.go internal/testutil/fakes.go
git commit -m "feat: vendor identity spine — entities, schema, repo"
```

---

### Task 2: Vendor usecases (ResolveSender is the pipeline's gate)

**Files:**
- Create: `internal/usecase/vendors/ports.go`, `internal/usecase/vendors/resolve_sender.go`, `internal/usecase/vendors/manage_vendors.go`
- Test: `internal/usecase/vendors/resolve_sender_test.go`, `internal/usecase/vendors/manage_vendors_test.go`

**Interfaces:**
- Consumes: Task 1 repo method signatures (declared here as ports).
- Produces: `ResolveSender.Execute(ctx, userID, tenantID uuid.UUID, email string) (entity.Vendor, bool, error)` — on miss, upserts the unknown-sender row and returns `found=false`. `CreateVendor.Execute(ctx, in CreateVendorInput{TenantID, UserID uuid.UUID, VendorName, ContactName, FirstEmail string}) (entity.Vendor, error)` — creating with an email that matches a pending unknown sender flips it to `attached`. `AttachEmail.Execute(ctx, userID uuid.UUID, vendorID uuid.UUID, email string) error`.

- [ ] **Step 1: Write failing tests** (table-driven, hand stubs per repo pattern)

```go
// internal/usecase/vendors/resolve_sender_test.go
package vendors

type stubVendorRepo struct {
	byEmail  map[string]entity.Vendor
	unknowns []entity.UnknownSender
}

func (s *stubVendorRepo) GetVendorBySenderEmail(_ context.Context, _ uuid.UUID, email string) (entity.Vendor, bool, error) {
	v, ok := s.byEmail[strings.ToLower(email)]
	return v, ok, nil
}
func (s *stubVendorRepo) UpsertUnknownSender(_ context.Context, u entity.UnknownSender) error {
	s.unknowns = append(s.unknowns, u)
	return nil
}

func TestResolveSender_KnownVendor(t *testing.T) {
	repo := &stubVendorRepo{byEmail: map[string]entity.Vendor{"ap@delmar.com": {VendorName: "Delmar Tooling"}}}
	uc := &ResolveSender{Repo: repo}
	v, found, err := uc.Execute(context.Background(), userID, accountID, "AP@Delmar.com") // case-insensitive
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "Delmar Tooling", v.VendorName)
	require.Empty(t, repo.unknowns) // known sender never recorded as unknown
}

func TestResolveSender_UnknownSenderRecorded(t *testing.T) {
	repo := &stubVendorRepo{byEmail: map[string]entity.Vendor{}}
	uc := &ResolveSender{Repo: repo}
	_, found, err := uc.Execute(context.Background(), userID, accountID, "new@nowhere.com")
	require.NoError(t, err)
	require.False(t, found)
	require.Len(t, repo.unknowns, 1)
	require.Equal(t, entity.UnknownSenderPending, repo.unknowns[0].Status)
}
```

- [ ] **Step 2: Run — expect FAIL** (`go test ./internal/usecase/vendors/`)
- [ ] **Step 3: Implement** `ports.go` (interfaces exactly matching Task 1 repo methods this package needs) + the three usecase files; `ResolveSender` lowercases before lookup.
- [ ] **Step 4: Run — expect PASS**, plus `go test ./...`
- [ ] **Step 5: Commit** — `git commit -m "feat: vendor usecases — sender resolution + unknown-sender recording"`

---

### Task 3: Order entity remodel + board rule functions

**Files:**
- Modify: `internal/entity/order.go` (full rewrite)
- Test: `internal/entity/order_test.go` (rewrite)

**Interfaces:**
- Produces (everything downstream compiles against this):

```go
type OrderStage string    // "ordered" "confirmed" "in_production" "shipped" "delivered"
type StageSource string   // "auto_inferred" "manual_override"
type Confidence string    // "high" "medium" "low"
type TimelineKind string  // "email" "stage" "flag" "checkup" "edit"
type DocType string       // "invoice" "packing_list" "bol"
type ReconciliationStatus string // "pending" "mismatch" "reconciled"

type OrderSnapshot struct{ Quantity *int; UnitPrice *float64; ExpectedShipDate *time.Time }
type OrderItem struct{ ID, OrderID uuid.UUID; Position int; ItemDescription string; Quantity *int; UnitPrice *float64; ExpectedShipDate *time.Time; CreatedAt, UpdatedAt time.Time }
type OrderDocument struct{ ID, OrderID uuid.UUID; DocType DocType; Filename string; Received time.Time; ReconciliationStatus ReconciliationStatus; Parsed OrderDocParsed; SourceEmailLink string; CreatedAt, UpdatedAt time.Time }
type OrderDocParsed struct{ Quantity *int; UnitPrice *float64; Total *float64; ExpectedShipDate *time.Time }
type TimelineEntry struct{ ID, OrderID uuid.UUID; EntryDate time.Time; Kind TimelineKind; Text, Link string; CreatedAt time.Time }

type Order struct {
	ID, TenantID, UserID uuid.UUID
	VendorID              *uuid.UUID
	OrderNumber           string // 'PO-2417' — business/matching key
	ConversationID        string // primary thread; "" for manual orders
	OrderDate             *time.Time
	OrderStage            OrderStage
	StageSource           StageSource
	ExpectedShipDate      *time.Time // derived: earliest item date, denormalized
	LastActivityDate      *time.Time
	LastActivitySummary   string
	SourceEmailLink       string
	StatedTotal           *float64
	Requested, Confirmed  *OrderSnapshot
	IsNew, Archived       bool
	DeliveredDate         *time.Time
	Items                 []OrderItem
	CreatedAt, UpdatedAt  time.Time
}

func ValidOrderStage(OrderStage) bool
func StageRank(OrderStage) int                       // ordered=0 … delivered=4, unknown=-1
func (o Order) CanApplyInferredStage(s OrderStage) bool // false if manual_override, invalid, or rank not higher
func (o Order) Total() float64                        // Σ qty×price over items (nil treated as 0)
func (o Order) TotalQuantity() int
func (o Order) EarliestItemShipDate() *time.Time
func (o Order) IsStalled(today time.Time) bool        // stage≠delivered, !archived, today−lastActivity ≥ StallDays
func (o Order) IsOverdue(today time.Time) bool        // expected < today, stage ∉ {shipped, delivered}

// New extraction shapes (replaces the old flat OrderExtraction):
type ExtractedItem struct{ ItemDescription string; Quantity *int; UnitPrice *float64; ExpectedShipDate *time.Time }
type OrderExtraction struct {
	OrderNumber, VendorName string
	OrderDate               *time.Time
	Items                   []ExtractedItem
	Stage                   OrderStage
	StatedTotal             *float64
	Currency                string
	Confidence              Confidence
	EvidenceSnippet, EvidenceWhere, ActivitySummary string
}
type TriageResult struct{ IsOrderRelated bool; OrderNumber, VendorName string; Confidence Confidence }

const StallDays = 21
const ArchiveAfterDays = 30
```

- The old `OrderStatus` constants, `OrderFromExtraction`, and the flat extraction fields are deleted. **This task intentionally breaks the build** (usecases/gateways still reference the old shape); Tasks 4–9 restore it — do them in order, committing per task once its package compiles and its tests pass. Run `go build ./...` at each task to watch the breakage shrink.

- [ ] **Step 1: Write the failing rule tests**

```go
// internal/entity/order_test.go (rewrite)
func TestCanApplyInferredStage(t *testing.T) {
	cases := []struct {
		name    string
		current OrderStage
		source  StageSource
		infer   OrderStage
		want    bool
	}{
		{"advance", OrderStageOrdered, StageSourceAuto, OrderStageShipped, true},
		{"no downgrade", OrderStageShipped, StageSourceAuto, OrderStageConfirmed, false},
		{"no same-stage churn", OrderStageConfirmed, StageSourceAuto, OrderStageConfirmed, false},
		{"manual override sticks", OrderStageOrdered, StageSourceManual, OrderStageShipped, false},
		{"invalid stage", OrderStageOrdered, StageSourceAuto, OrderStage("bogus"), false},
	}
	for _, c := range cases {
		o := Order{OrderStage: c.current, StageSource: c.source}
		if got := o.CanApplyInferredStage(c.infer); got != c.want {
			t.Fatalf("%s: got %v", c.name, got)
		}
	}
}

func TestOrderDerivedValues(t *testing.T) {
	q1, q2, p1, p2 := 500, 200, 2.40, 3.10
	aug12, aug20 := date("2026-08-12"), date("2026-08-20")
	o := Order{Items: []OrderItem{
		{Quantity: &q1, UnitPrice: &p1, ExpectedShipDate: &aug20},
		{Quantity: &q2, UnitPrice: &p2, ExpectedShipDate: &aug12},
	}}
	require.InDelta(t, 500*2.40+200*3.10, o.Total(), 0.001)
	require.Equal(t, 700, o.TotalQuantity())
	require.Equal(t, aug12, *o.EarliestItemShipDate()) // earliest across lines
}

func TestStalledAndOverdue(t *testing.T) {
	today := date("2026-07-31")
	old := date("2026-07-01") // 30 days silent
	ship := date("2026-07-28")
	o := Order{OrderStage: OrderStageInProduction, LastActivityDate: &old, ExpectedShipDate: &ship}
	require.True(t, o.IsStalled(today))
	require.True(t, o.IsOverdue(today))
	o.OrderStage = OrderStageShipped
	require.False(t, o.IsOverdue(today)) // shipped is never overdue
	o.Archived = true
	require.False(t, o.IsStalled(today))
}
```

- [ ] **Step 2: Run — expect FAIL** (`go test ./internal/entity/ -run 'TestCanApply|TestOrderDerived|TestStalled'`)
- [ ] **Step 3: Rewrite `entity/order.go`** to the Produces block above (delete old constants/types; `IsStalled` uses `StallDays`; nil `LastActivityDate` → not stalled; nil `ExpectedShipDate` → not overdue).
- [ ] **Step 4: Run entity tests — expect PASS.** `go build ./...` fails elsewhere — expected until Task 9.
- [ ] **Step 5: Commit** — `git commit -m "feat!: order entity remodel — stages, items, timeline, board rules (build broken until pipeline tasks land)"`

---

### Task 4: Retire the legacy Otto scan path

**Files:**
- Delete: `internal/controller/http/otto.go`, `internal/controller/http/otto_e2e_test.go`, `internal/usecase/otto/` (whole package), `internal/repo/ottoscans.go`, `internal/entity/ottoscan.go`, `internal/entity/ottoscan_test.go`
- Create: `db/migrations/20260809000008_drop_otto_scans.sql` (`DROP TABLE otto_scans;`; down recreates from the old migration's DDL)
- Modify: `internal/controller/http/router.go` (remove `/otto/*` routes + wiring), `cmd/overseer/main.go` (remove otto wiring), `internal/gateway/llm/` (delete `RankDestinations`, `PlanSheetAppend` + their schemas/tests), `internal/testutil/fakes.go` (drop otto fakes + those two FakeLLM methods), `internal/controller/http/dto.go` (drop scan DTOs)
- Keep: `internal/gateway/drive/` and `internal/gateway/excel/` untouched (Plan 2 consumes them)

**Interfaces:**
- Produces: a smaller compile surface — `LLMGateway` temporarily has only `FilterOrderMessages`, `ExtractOrderFields`, `FindDiscrepancies` (Task 6 reshapes these).

- [ ] **Step 1: Delete the files/routes/wiring listed above.** Grep to catch stragglers: `grep -rn "otto\|OttoScan\|RankDestinations\|PlanSheetAppend" internal/ cmd/ --include='*.go'` — expected zero hits after.
- [ ] **Step 2: Write the migration** dropping `otto_scans`.
- [ ] **Step 3: Build + remaining tests** — `go build ./...` still fails only where the old order shape is referenced (`usecase/orders`, `usecase/messages`, `gateway/llm`, `testutil`); no otto references remain.
- [ ] **Step 4: Commit** — `git commit -m "feat!: retire legacy Otto scan path (rebuilt as extraction queue in plan 2)"`

---

### Task 5: Orders schema rebuild + repo rewrite

**Files:**
- Create: `db/migrations/20260809000009_orders_rebuild.sql`
- Modify: `internal/repo/orders.go` (rewrite), `internal/testutil/fakes.go` (in-memory orders repo → new shape)
- Test: covered by usecase/E2E tiers (repo layer has no direct tests, per repo convention)

**Interfaces:**
- Consumes: Task 3 entity shapes.
- Produces (ports for Tasks 7–12; all owner-scoped):

```go
Create(ctx, o entity.Order) (entity.Order, error)                  // tx: order + items + initial timeline entries
Update(ctx, o entity.Order) (entity.Order, error)                  // tx: order row + replace items
GetByID(ctx, userID, id uuid.UUID) (entity.Order, bool, error)     // loads items
GetByOrderNumber(ctx, userID uuid.UUID, n string) (entity.Order, bool, error)
GetLatestByConversation(ctx, userID uuid.UUID, convID string) (entity.Order, bool, error)
List(ctx, f ListFilter) ([]entity.Order, string, error)            // ListFilter{UserID, Stage, VendorID, Stat, Query, Sort, Archived, Cursor, Limit}
Stats(ctx, userID uuid.UUID, today time.Time) (entity.BoardStats, error)
AppendTimelineOnce(ctx, e entity.TimelineEntry, dedupeKey string) error  // "" = always append; non-empty key skips if already recorded (webhook idempotency)
ListTimeline(ctx, orderID uuid.UUID) ([]entity.TimelineEntry, error)
ListDocuments(ctx, orderID uuid.UUID) ([]entity.OrderDocument, error)
UpsertDocument(ctx, d entity.OrderDocument) error
SetIsNew(ctx, userID, id uuid.UUID, v bool) error
ArchiveDeliveredBefore(ctx, userID uuid.UUID, cutoff time.Time) ([]entity.Order, error)
```

plus `entity.BoardStats{ActiveOrderCount int; TotalOpenValue float64; StalledOrderCount, OverdueOrderCount, OpenFlagCountTotal int}` (add to `entity/order.go` here).

- [ ] **Step 1: Write the migration** — copy `orders` (rebuild), `order_items`, `order_documents`, `order_timeline` DDL from the spec verbatim; also `ALTER TABLE discrepancies ALTER COLUMN order_id TYPE TEXT USING order_id::text;` (the Plan-1 shim keeping Audit compiling — full remodel in Plan 4). Up starts with `DROP TABLE orders;` (breaking allowed). Down restores the old `orders` DDL from `20260704000002`.
- [ ] **Step 2: Rewrite `repo/orders.go`** to the port list. Notes that matter: `Create`/`Update` run in a `pgx.Tx` (order row, `DELETE FROM order_items WHERE order_id=…` + reinsert on update, timeline appends in the same tx); `List` builds WHERE from the filter (stage/vendor/archived/cursor as today; `Stat` values `stalled|overdue` become SQL predicates mirroring `IsStalled`/`IsOverdue`; `issues` is deferred to Plan 4 — return the unfiltered list for now with a `// plan-4` comment); `Query` is `ILIKE` over `order_number`, vendor name (join), and item descriptions (EXISTS subquery); sort keys `last_update|expected_ship|order_value|vendor` (order_value sorts by a `LEFT JOIN LATERAL` items-total subquery); `is_new DESC` always leads the ORDER BY (pinning).
- [ ] **Step 3: Update `testutil`'s in-memory orders repo** to the same port set (slice + maps; filters/sorts implemented with the entity helper functions so fakes and SQL agree on the rules).
- [ ] **Step 4: Build** — `go build ./internal/repo/ ./internal/testutil/` clean (usecases still broken).
- [ ] **Step 5: Commit** — `git commit -m "feat!: orders schema rebuild — items, documents, timeline, board queries"`

---

### Task 6: LLM gateway reshape — TriageMessage + multi-line extraction

**Files:**
- Modify: `internal/gateway/llm/` (replace `FilterOrderMessages` with `TriageMessage`; rewrite `ExtractOrderFields` schema; leave `FindDiscrepancies` compiling against the shimmed `discrepancies` — only change its `order_id` handling to string)
- Modify: `internal/testutil/fakes.go` (FakeLLM: `TriageReturns(map[string]entity.TriageResult)`, `ExtractReturns(entity.OrderExtraction)`)

**Interfaces:**
- Consumes: Task 3's `entity.TriageResult`, `entity.OrderExtraction`, `entity.ExtractedItem`.
- Produces: `TriageMessage(ctx, token string, msg entity.EmailMessage, knownVendors []string, knownOrderNumbers []string) (entity.TriageResult, error)` and `ExtractOrderFields(ctx, token string, thread entity.Thread) (entity.OrderExtraction, error)`.

- [ ] **Step 1: Write the JSON schemas** following the existing gateway pattern (`additionalProperties:false`, `explanation` field ordered before decision fields, validation + 1 retry at the boundary):
    - Triage: `{explanation, isOrderRelated bool, orderNumber string, vendorName string, confidence enum(high|medium|low)}` — prompt includes the known vendor names + open order numbers so the model can echo an exact match; empty `orderNumber` allowed.
    - Extract: `{explanation, orderNumber, vendorName, orderDate str|null, items:[{itemDescription, quantity int|null, unitPrice number|null, expectedShipDate str|null}], stage enum(ordered|confirmed|in_production|shipped|delivered), statedTotal number|null, currency string, confidence enum, evidenceSnippet, evidenceWhere, activitySummary}` — validation: dates `YYYY-MM-DD`, stage must pass `ValidOrderStage`, `items` may be empty but not absent.
- [ ] **Step 2: Implement both methods + delete the old filter method**; keep the per-call `"llm call"` cost logging.
- [ ] **Step 3: Update FakeLLM** to the new signatures; record calls like the existing fakes do (`TriageCalls`, `ExtractCalls`).
- [ ] **Step 4: Build gateway + testutil** — `go build ./internal/gateway/... ./internal/testutil/`; commit `git commit -m "feat!: llm gateway — per-message triage + multi-line extraction"`

---

### Task 7: `ApplyMessageToOrder` usecase (merge rules + stage guard + timeline)

**Files:**
- Create: `internal/usecase/orders/apply_message_to_order.go`
- Modify: `internal/usecase/orders/ports.go`
- Delete: `internal/usecase/orders/format_initial_order_data.go` + test (triage/extract now called directly by callers)
- Test: `internal/usecase/orders/apply_message_to_order_test.go`

**Interfaces:**
- Consumes: Task 5 order repo ports, Task 3 entities.
- Produces: `ApplyMessageToOrder.Execute(ctx, in ApplyInput) (entity.Order, bool /*created*/, error)` with `ApplyInput{TenantID, UserID uuid.UUID; VendorID *uuid.UUID; Existing *entity.Order; X entity.OrderExtraction; Msg entity.EmailMessage; Today time.Time}`. Merge rules (unit-tested here, reused by sync + webhook + Plan-2 accept):
    - Existing order: items merged per extracted line — fill nil fields, overwrite non-nil ones; `Confirmed` snapshot rebuilt from items[0]; stage applied only through `CanApplyInferredStage`; `LastActivityDate` **only moves forward**; `IsNew = true`; `StatedTotal` set when extraction carries one; exactly one `email` timeline entry (text = `ActivitySummary`, link = `Msg.WebLink`).
    - No order: create at extracted stage (default `ordered`), `StageSource = auto_inferred`, `Requested` from the extraction, `Confirmed = nil`, one `email` timeline entry.

- [ ] **Step 1: Write the failing test table** (stub repo records Create/Update/AppendTimeline calls):

```go
func TestApply_MergeExistingOrder(t *testing.T) { /* fill-vs-overwrite per field; one timeline entry; is_new set */ }
func TestApply_StageGuard(t *testing.T)         { /* manual_override never overwritten; no downgrade shipped→confirmed */ }
func TestApply_LastActivityOnlyForward(t *testing.T) { /* older email cannot move last_activity_date backward */ }
func TestApply_CreatesWhenNoExisting(t *testing.T)   { /* ordered/auto_inferred, requested set, confirmed nil */ }
```

Write these as full table-driven tests in the file — each case builds an `ApplyInput`, runs `Execute`, and asserts the stored order + the single timeline append (follow the `stubRepo` style used across this repo's usecase tests).

- [ ] **Step 2: Run — expect FAIL** · **Step 3: Implement** · **Step 4: Run — PASS** (`go test ./internal/usecase/orders/ -run TestApply`)
- [ ] **Step 5: Commit** — `git commit -m "feat: ApplyMessageToOrder — merge rules, stage guard, single timeline entry"`

---

### Task 8: Needs Review — entity, schema, repo, usecases

**Files:**
- Create: `internal/entity/unmatched.go`, `db/migrations/20260809000010_unmatched_emails.sql`, `internal/repo/unmatched.go`, `internal/usecase/review/ports.go`, `internal/usecase/review/list_unmatched.go`, `internal/usecase/review/assign_to_order.go`, `internal/usecase/review/create_order_from_email.go`, `internal/usecase/review/set_dismissed.go`
- Modify: `internal/testutil/fakes.go` (in-memory unmatched repo)
- Test: `internal/usecase/review/assign_to_order_test.go`, `internal/usecase/review/create_order_from_email_test.go`, `internal/usecase/review/set_dismissed_test.go`

**Interfaces:**
- Consumes: order repo ports (timeline append, GetByID, Create), vendors `CreateVendor`.
- Produces: `entity.UnmatchedEmail{ID, TenantID, UserID uuid.UUID; MessageID, ConversationID, Sender, Subject string; ReceivedDate time.Time; SourceEmailLink string; Parsed entity.UnmatchedParse; Status entity.UnmatchedStatus; …}` with `UnmatchedStatus ∈ pending|assigned|converted|dismissed`, `UnmatchedParse{VendorName, ItemDescription string; Quantity *int; UnitPrice *float64}`; repo `Insert` (idempotent on `(user_id, message_id)` via `ON CONFLICT DO NOTHING`), `ListByStatus`, `GetByID`, `SetStatus`; usecases:
    - `AssignToOrder.Execute(ctx, userID uuid.UUID, id, orderID uuid.UUID, today time.Time) error` — timeline `email` entry dated `ReceivedDate` (but order `last_activity_date` only forward), status → `assigned`. (Extraction-row creation added in Plan 2 — leave a `// plan-2: queue extraction` marker.)
    - `CreateOrderFromEmail.Execute(ctx, in CreateFromEmailInput) (entity.Order, error)` — validates vendor+item+quantity; `ordered`/`manual_override`; `Requested` from draft; email = first timeline entry; status → `converted`; `NewVendorName` set → create vendor + attach sender.
    - `SetDismissed.Execute(ctx, userID, id uuid.UUID, dismissed bool) error` — `pending ↔ dismissed` only; `assigned`/`converted` are terminal → `entity.ErrIllegalTransition` (add alongside `ErrReauthRequired` in `entity/errors.go`).

- [ ] **Step 1: Failing tests** — assign happy path (timeline recorded, status flips, backward-dated email doesn't move `last_activity_date`); create-order validation errors (missing quantity → error, nothing written); dismiss/reopen legal-transition table.
- [ ] **Step 2–4: Red → implement (migration DDL verbatim from spec) → green** (`go test ./internal/usecase/review/`)
- [ ] **Step 5: Commit** — `git commit -m "feat: needs review — unmatched emails, assign/create/dismiss usecases"`

---

### Task 9: `HandleChangeMessage` rewrite — the matching pipeline

**Files:**
- Modify: `internal/usecase/messages/handle_change_message.go`, `internal/usecase/messages/ports.go`
- Test: `internal/usecase/messages/handle_change_message_test.go` (rewrite)

**Interfaces:**
- Consumes: `vendors.ResolveSender`, `llm.TriageMessage`/`ExtractOrderFields` (via ports), order repo (`GetByOrderNumber`, `GetLatestByConversation`), `ApplyMessageToOrder`, unmatched repo `Insert`, existing `SubscriptionResolver`/`TokenSource`/`GetLatestMessage`/`EventPublisher`/`DiscrepancyCatcher`/`CheckupCanceller`.
- Produces: `HandleOutcome` gains `queued_needs_review` and `skipped_unknown_sender` alongside `created|updated|skipped_not_order|skipped_unknown_subscription`.

Pipeline order (spec §matching pipeline — encode as the test table):
1. Resolve subscription → token → fetch message (unchanged).
2. `ResolveSender` — unknown sender: record (done inside ResolveSender), still triage; if order-related → `unmatched_emails` + `needs_review.created` event, outcome `skipped_unknown_sender` (no order mutation, no extraction).
3. Reply-cancel (`CheckupCanceller`) before the order gate, as today (Plan 3 replaces with full reply detection).
4. Triage with known vendors + open order numbers. Not order-related → `skipped_not_order`.
5. Match: triage `OrderNumber` → `GetByOrderNumber`; miss → `GetLatestByConversation` (same-thread continuation); miss → `unmatched_emails` insert + `needs_review.created`, outcome `queued_needs_review`.
6. Matched (or conversation hit): `ExtractOrderFields` on the thread → `ApplyMessageToOrder` → publish `order.updated`/`order.created`.
7. Audit (`DiscrepancyCatcher`) unchanged shim.
Idempotency: duplicate Graph delivery of the same message must not double-insert `unmatched_emails` (repo `ON CONFLICT`) nor double-append timeline (apply is upsert-shaped; assert single entry for a re-delivered message by keying the timeline append on message id — pass `Msg.ID` in `TimelineEntry.Link` dedupe check inside `ApplyMessageToOrder`'s repo call: `AppendTimeline` becomes `AppendTimelineOnce(ctx, e, dedupeKey string)`; update Task 5/7 signatures accordingly).

- [ ] **Step 1: Rewrite the test table** — one case per outcome above plus: unknown subscription (existing), token miss (existing), duplicate delivery (single timeline entry, single unmatched row), reply-cancel still invoked before triage.
- [ ] **Step 2–4: Red → implement → green** (`go test ./internal/usecase/messages/`); then `go build ./...` — **the full build must be clean from here on.**
- [ ] **Step 5: Commit** — `git commit -m "feat!: matching pipeline — vendor gate, PO match, needs-review fallback"`

---

### Task 10: Sync rebuild — vendor seeding + progress reporting

**Files:**
- Create: `db/migrations/20260809000011_sync_runs.sql`, `internal/repo/syncruns.go`
- Modify: `internal/usecase/orders/build_active_order_list.go` + test, `internal/entity/event.go` (add `EventSyncProgress = "sync.progress"`, `EventOrderCreated = "order.created"`, `EventNeedsReviewCreated = "needs_review.created"`, `SyncProgress{SyncID uuid.UUID; Pct int; StatusLine string}`)
- Modify: `internal/controller/ws/hub.go` (map new event payloads)

**Interfaces:**
- Consumes: triage/extract ports, `ApplyMessageToOrder`, vendors usecases, unmatched repo, sync-run repo (`Insert`, `SetProgress`, `Finish`).
- Produces: `BuildActiveOrderList.Execute(ctx, in BuildInput{TenantID, UserID uuid.UUID; Token string; SyncID uuid.UUID; Since time.Time}) (int, error)` — per thread: triage latest message → extract → seed vendor by sender when new (auto-create with domain-derived name, mirroring the spec's "vendor inferred from sender domain") → apply → progress event every thread (`Pct = threadsDone*100/threadTotal`, `StatusLine` cycling the five spec lines); unmatched threads land in `unmatched_emails`; finishes `sync_runs` row + `sync.completed` (existing event, unchanged shape).

- [ ] **Step 1: Update the usecase test** — asserts: non-order threads skipped; orders + vendors + unmatched rows created; progress events published with monotonic `Pct`; LLM error aborts before any write (existing rule, keep the case); `sync_runs` row transitions running → completed.
- [ ] **Step 2–4: Red → migration (DDL verbatim) + implement → green**; `go test ./...`
- [ ] **Step 5: Commit** — `git commit -m "feat: sync rebuild — vendor seeding, needs-review fallback, sync.progress"`

---

### Task 11: Board read/write API — list, detail, draft-save

**Files:**
- Create: `internal/usecase/orders/get_order_detail.go` (+ test), `internal/usecase/orders/save_order_draft.go` (+ test)
- Modify: `internal/usecase/orders/list_orders.go` (+ test — filters/stats/shipFigure), `internal/controller/http/orders.go` (GET list, GET `/orders/:orderId`, PATCH `/orders/:orderId`), `internal/controller/http/dto.go`, `internal/controller/http/router.go`
- Test: `internal/controller/http/orders_e2e_test.go` (rewrite)

**Interfaces:**
- Consumes: Task 5 repo ports.
- Produces:
    - `ListOrders.Execute(ctx, f ListFilter) (ListResult{Orders []BoardOrder; Stats entity.BoardStats; NextCursor string}, error)` where `BoardOrder` wraps `entity.Order` + computed `ShipFigure{Kind string; Value int}` (kinds: `delivered|days_late|silent|in_transit|today|days|tbd`, priority per spec) and `ArchiveDaysLeft *int`; `AtStake`/`OpenFlagCount`/`Contested` are zero-valued with `// plan-4` markers.
    - `GetOrderDetail.Execute(ctx, userID, id uuid.UUID) (OrderDetail, error)` — order + items + documents + timeline; **clears `is_new`** via `SetIsNew` (the one read side effect, per spec).
    - `SaveOrderDraft.Execute(ctx, in DraftInput{UserID uuid.UUID; OrderID uuid.UUID; Patch OrderPatch}) (entity.Order, error)` — `OrderPatch{OrderNumber, VendorID, OrderDate, Requested, Confirmed *…; Items *[]entity.OrderItem; OrderStage *entity.OrderStage; Archived *bool}`; one transaction; **one `edit` timeline entry** for any field combination; `OrderStage` present → `manual_override` stamp + `stage` timeline entry instead; `ExpectedShipDate` recomputed from items; `Archived` flips also stamp a timeline entry.
    - DTOs: the JSON envelope exactly as the spec's `GET controller/orders` example (camelCase, `stats` object, `shipFigure{kind,value}`).

- [ ] **Step 1: Usecase unit tests first** — ship-figure priority table (delivered beats late beats silent beats in-transit beats today/days/tbd); draft-save single-timeline-entry rule + stage-override stamping; detail clears `is_new`.
- [ ] **Step 2: Red → implement → green** (`go test ./internal/usecase/orders/`)
- [ ] **Step 3: E2E rewrite** — `GET /orders` returns the new envelope (assert `stats` + camelCase + pinning: an `is_new` row sorts first); `GET /orders/{id}` includes items/timeline and a second fetch shows `isNew:false`; `PATCH` with items + a field asserts exactly one new timeline entry via a follow-up GET; auth: smuggled `userId` ignored (keep the existing case shape).
- [ ] **Step 4: Green** (`go test ./internal/controller/...`) · **Step 5: Commit** — `git commit -m "feat: board API — list with stats and ship figures, detail, draft-save"`

---

### Task 12: Needs Review controller + E2E

**Files:**
- Create: `internal/controller/http/review.go`, `internal/controller/http/review_e2e_test.go`
- Modify: `internal/controller/http/router.go`, `internal/controller/http/dto.go`

**Interfaces:**
- Consumes: Task 8 usecases.
- Produces routes: `GET /needs-review?status=pending|dismissed` · `POST /needs-review/:id/assign {orderId}` · `POST /needs-review/:id/create-order {vendorId|newVendorName, itemDescription, quantity, unitPrice?, orderNumber?}` · `POST /needs-review/:id/dismiss` · `POST /needs-review/:id/reopen`. Assign/create respond `200` with the affected order; dismiss/reopen `204`; illegal transition → `409`.

- [ ] **Step 1: E2E tests** — assign writes timeline on target (assert via `GET /orders/{id}`); create-order 400 on missing quantity; dismiss → row appears under `?status=dismissed`; reopen restores; unknown id → `404`.
- [ ] **Step 2: Red → implement → green** · **Step 3: Commit** — `git commit -m "feat: needs review endpoints"`

---

### Task 13: Auto-archive worker + WS event coverage + docs

**Files:**
- Create: `internal/usecase/orders/archive_delivered.go` (+ test)
- Modify: `cmd/overseer/main.go` (daily ticker), `internal/controller/ws/hub.go` (+ `hub_test.go`: DTO mapping for `order.created`, `needs_review.created`, `sync.progress`), `.ai/ARCHITECTURE.md` (API table + event list + data model refresh to match this plan)
- Test: `internal/usecase/orders/archive_delivered_test.go`

**Interfaces:**
- Produces: `ArchiveDelivered.Execute(ctx, now time.Time) (int, error)` — archives `delivered` orders with `delivered_date ≤ now − ArchiveAfterDays`, one `stage`-kind timeline entry each ("Auto-archived 30 days after delivery"), publishes `order.updated` per order. Wired on a 24h ticker beside the existing two (extract the three tickers into `internal/worker/scheduler.go` while here — the `.ai` docs already describe that layout; keep each tick a one-line usecase call).

- [ ] **Step 1: Unit test** — order delivered 31 days ago archives; 29 days doesn't; already-archived untouched; count returned; clock injected (`Now` passed in, never `time.Now()` inside).
- [ ] **Step 2: Red → implement (+ scheduler extraction + hub DTOs) → green** — `go test ./...` fully green.
- [ ] **Step 3: Update `.ai/ARCHITECTURE.md`** — endpoints, events, schema summary, worker layout (fixing the pre-existing drift for the surfaces this plan touched).
- [ ] **Step 4: Commit** — `git commit -m "feat: auto-archive job, worker extraction, event coverage, docs refresh"`

---

## Self-Review Notes (already applied)

- Spec coverage for Plan-1 scope verified section by section; deliberate deferrals are listed in the roadmap paragraph (extraction rows, flags math, grant gating, reply detection) and marked `// plan-N` at the code sites that stub them.
- Type consistency: `AppendTimelineOnce(ctx, e, dedupeKey)` introduced in Task 9 — Tasks 5 and 7 reference the final signature.
- Every migration copies DDL from `docs/tech_spec_dashboard2.md` Schema Updates verbatim; that section stays the source of truth.
