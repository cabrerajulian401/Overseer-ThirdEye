# Thread Audit (Discrepancy Detection) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the Audit feature: when a new message arrives in a known order conversation, compare it against the thread history with the LLM, store any buyer/seller discrepancies, push `discrepancy.found` over `/ws`, and serve/manage findings via `GET /discrepancies` + `PATCH /discrepancies/{discrepancyId}`.

**Architecture:** Audit rides the existing webhook job (`usecase/messages.HandleChangeMessage`) — no infrastructure of its own. A new `usecase/orders.CatchDiscrepancies` re-fetches the thread from Graph (no messages table; Graph is the source of truth), calls a new `gateway/llm.FindDiscrepancies`, inserts rows into a new `discrepancies` table, and publishes `entity.EventDiscrepancyFound` (the constant already exists). Layering rule never breaks: `controller / worker → usecase → gateway / repo`.

**Tech Stack:** Go, Echo, pgx/pgxpool, dbmate migrations (embedded), gorilla/websocket, Anthropic SDK (behind `gateway/llm`), hand-written fakes in `internal/testutil`.

**Branch:** all work happens on `thread-audit`, branched from `order_list` (which holds the webhook pipeline this feature rides; `main` does not have it until PR #2 merges).

## Global Constraints

- Module path: `github.com/thirdeye/overseer`; all commands run from `overseer/` (e.g. `go test ./...`).
- Layering rule (never break): `controller / worker → usecase → gateway / repo`. Gateways never touch the DB; usecases own orchestration; ports live in the consuming usecase package's `ports.go`.
- `entity` imports only stdlib + `github.com/google/uuid`; entities carry **no** JSON/DB tags.
- Casing: `PascalCase` Go types · `snake_case` DB columns · `camelCase` JSON. Enums are typed strings matching the DB `CHECK` vocabulary: status `open/resolved/dismissed`, priority `high/medium/low`, metric type `price/date/unit`.
- Migrations: dbmate files in `overseer/db/migrations/`, named `YYYYMMDDHHMMSS_name.sql` with `-- migrate:up` / `-- migrate:down`; DDL copied verbatim from tech_spec.md → Schema Updates; append-only.
- No test ever calls a real external service. Usecase tests stub ports (hand-written, in `testutil` or in-package); controller E2E tests run the real router + usecases over `httptest` with `testutil` fakes.
- One realtime channel: new features add event **types** (`discrepancy.found`), never new sockets.
- Server mints all IDs; `userId`/`accountId` come from auth context only; every repo query filters by owner.
- Commit after every task with the exact message given in its final step.

## File Structure

| File | Responsibility |
|---|---|
| `overseer/internal/entity/discrepancy.go` (new) | Discrepancy domain types + enum validators |
| `overseer/db/migrations/20260707000004_discrepancies.sql` (new) | `discrepancies` table DDL |
| `overseer/internal/gateway/graph/graph.go` (modify) | `ListConversationMessages` — re-fetch thread history |
| `overseer/internal/gateway/llm/llm.go` (modify) | `FindDiscrepancies` — prompt, schema, output validation |
| `overseer/internal/repo/discrepancies.go` (new) | insert / list-by-user / update-status SQL + JSONB mapping |
| `overseer/internal/usecase/orders/ports.go` (modify) | `ThreadFetcher`, `DiscrepancyLLM`, `DiscrepancyRepo` ports |
| `overseer/internal/usecase/orders/catch_discrepancies.go` (new) | the Audit usecase |
| `overseer/internal/usecase/orders/list_discrepancies.go` (new) | GET usecase |
| `overseer/internal/usecase/orders/update_discrepancy_status.go` (new) | PATCH usecase |
| `overseer/internal/usecase/messages/handle_change_message.go` + `ports.go` (modify) | run Audit after the order upsert |
| `overseer/internal/controller/ws/hub.go` (modify) | `discrepancy.found` wire DTO |
| `overseer/internal/controller/http/discrepancies.go` (new) + `dto.go`, `router.go` (modify) | REST surface |
| `overseer/internal/testutil/fakes.go` (modify) | FakeGraph thread fetch, FakeLLM findings, InMemDiscrepancies |
| `overseer/cmd/overseer/main.go` (modify) | wiring + compile-time conformance asserts |

---

### Task 1: Discrepancy entity + migration

**Files:**
- Create: `overseer/internal/entity/discrepancy.go`
- Create: `overseer/internal/entity/discrepancy_test.go`
- Create: `overseer/db/migrations/20260707000004_discrepancies.sql`

**Interfaces:**
- Consumes: nothing (innermost ring).
- Produces: `entity.Discrepancy`, `entity.DiscrepancyFinding`, `entity.DiscrepancyDetail`, `entity.DiscrepancyQuote`, `entity.DiscrepancyStatus` (+`ValidDiscrepancyStatus`), `entity.Priority` (+`ValidPriority`), `entity.MetricType` (+`ValidMetricType`) — every later task uses these exact names.

- [ ] **Step 1: Write the failing test**

```go
// overseer/internal/entity/discrepancy_test.go
package entity

import "testing"

func TestValidDiscrepancyStatus(t *testing.T) {
	for _, s := range []DiscrepancyStatus{DiscrepancyOpen, DiscrepancyResolved, DiscrepancyDismissed} {
		if !ValidDiscrepancyStatus(s) {
			t.Errorf("ValidDiscrepancyStatus(%q) = false, want true", s)
		}
	}
	if ValidDiscrepancyStatus("archived") {
		t.Error(`ValidDiscrepancyStatus("archived") = true, want false`)
	}
}

func TestValidPriority(t *testing.T) {
	for _, p := range []Priority{PriorityHigh, PriorityMedium, PriorityLow} {
		if !ValidPriority(p) {
			t.Errorf("ValidPriority(%q) = false, want true", p)
		}
	}
	if ValidPriority("urgent") {
		t.Error(`ValidPriority("urgent") = true, want false`)
	}
}

func TestValidMetricType(t *testing.T) {
	for _, m := range []MetricType{MetricPrice, MetricDate, MetricUnit} {
		if !ValidMetricType(m) {
			t.Errorf("ValidMetricType(%q) = false, want true", m)
		}
	}
	if ValidMetricType("weight") {
		t.Error(`ValidMetricType("weight") = true, want false`)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd overseer && go test ./internal/entity/`
Expected: FAIL — `undefined: DiscrepancyStatus`, `undefined: ValidDiscrepancyStatus`, etc.

- [ ] **Step 3: Write the entity file**

```go
// overseer/internal/entity/discrepancy.go
package entity

import (
	"time"

	"github.com/google/uuid"
)

type DiscrepancyStatus string

const (
	DiscrepancyOpen      DiscrepancyStatus = "open"
	DiscrepancyResolved  DiscrepancyStatus = "resolved"
	DiscrepancyDismissed DiscrepancyStatus = "dismissed"
)

// ValidDiscrepancyStatus reports whether s is one of the known statuses,
// matching the DB CHECK vocabulary.
func ValidDiscrepancyStatus(s DiscrepancyStatus) bool {
	switch s {
	case DiscrepancyOpen, DiscrepancyResolved, DiscrepancyDismissed:
		return true
	}
	return false
}

type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

func ValidPriority(p Priority) bool {
	switch p {
	case PriorityHigh, PriorityMedium, PriorityLow:
		return true
	}
	return false
}

// MetricType names which agreed-upon metric a discrepancy is about.
type MetricType string

const (
	MetricPrice MetricType = "price"
	MetricDate  MetricType = "date"
	MetricUnit  MetricType = "unit"
)

func ValidMetricType(m MetricType) bool {
	switch m {
	case MetricPrice, MetricDate, MetricUnit:
		return true
	}
	return false
}

// DiscrepancyDetail is the previously-agreed vs newly-stated value pair.
type DiscrepancyDetail struct {
	PreviouslyAgreed string
	NewMetric        string
	Type             MetricType
}

// DiscrepancyQuote evidences a finding: a verbatim quote from the history and
// a deeplink to the message it came from.
type DiscrepancyQuote struct {
	Quote       string
	MessageLink string
}

// DiscrepancyFinding is what gateway/llm.FindDiscrepancies returns: a finding
// with no ownership attached yet. CatchDiscrepancies stamps account/user/
// conversation/order fields before persisting.
type DiscrepancyFinding struct {
	Title    string
	Priority Priority
	Detail   DiscrepancyDetail
	Quotes   []DiscrepancyQuote
}

type Discrepancy struct {
	ID               uuid.UUID
	AccountID        uuid.UUID
	UserID           uuid.UUID
	ConversationID   string // Graph conversation id — same lookup key as orders
	Status           DiscrepancyStatus
	CompanyName      string
	EmailAddress     string
	EmailDeeplinkURL string // so the UI can open a draft to the supplier
	OrderID          int    // supplier's PO number, copied from the order row
	Priority         Priority
	Title            string
	Detail           DiscrepancyDetail
	Quotes           []DiscrepancyQuote
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd overseer && go test ./internal/entity/`
Expected: PASS

- [ ] **Step 5: Write the migration**

```sql
-- overseer/db/migrations/20260707000004_discrepancies.sql
-- migrate:up

CREATE TABLE IF NOT EXISTS discrepancies (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id         UUID NOT NULL REFERENCES accounts(id),
    user_id            UUID NOT NULL REFERENCES users(id),
    conversation_id    TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')), -- the UI needs to clear findings, not show them forever
    company_name       TEXT NOT NULL,
    email_address      TEXT,
    email_deeplink_url TEXT,                                  -- so the UI can open a draft to the supplier
    order_id           INTEGER,
    priority           TEXT NOT NULL CHECK (priority IN ('high','medium','low')),
    discrepancy_title  TEXT NOT NULL,
    discrepancy_detail JSONB,                                 -- { "previously_agreed": ..., "new_metric": ..., "type": "price" | "date" | "unit" }
    discrepancy_quotes JSONB,                                 -- [{ "quote": ..., "message_link": ... }] — old conversation history w/ link to message
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The read path: GET /discrepancies filters by owner (+ status), newest first.
CREATE INDEX IF NOT EXISTS idx_discrepancies_user ON discrepancies (user_id, status);

-- migrate:down

DROP TABLE IF EXISTS discrepancies;
```

- [ ] **Step 6: Verify everything builds**

Run: `cd overseer && go build ./... && go vet ./...`
Expected: no output (success). (`go:embed db/migrations/*.sql` in `embed.go` picks the new file up automatically.)

- [ ] **Step 7: Commit**

```bash
git add overseer/internal/entity/discrepancy.go overseer/internal/entity/discrepancy_test.go overseer/db/migrations/20260707000004_discrepancies.sql
git commit -m "feat: discrepancy entity and discrepancies table migration"
```

---

### Task 2: `gateway/graph.ListConversationMessages`

**Files:**
- Modify: `overseer/internal/gateway/graph/graph.go` (append method; add `"sort"` and `"strings"` imports)
- Modify: `overseer/internal/gateway/graph/graph_test.go` (append test)

**Interfaces:**
- Consumes: existing `fetchPage`, `toEntity`, `pageSize`, `maxMessages` in the same file.
- Produces: `func (c *Client) ListConversationMessages(ctx context.Context, accessToken, conversationID string) ([]entity.EmailMessage, error)` — returns the whole conversation **oldest → newest**. Task 5's `ThreadFetcher` port has this exact signature.

- [ ] **Step 1: Write the failing test** (append to `graph_test.go`)

```go
func TestListConversationMessages(t *testing.T) {
	var page2 bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me/messages" {
			t.Errorf("path = %q, want /me/messages", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("auth header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			page2 = true
			fmt.Fprint(w, `{"value": [{"id": "m-1", "conversationId": "conv'9", "createdDateTime": "2026-07-01T09:00:00Z"}]}`)
			return
		}
		// single quotes in the id must be doubled inside the OData literal
		if got := r.URL.Query().Get("$filter"); got != `conversationId eq 'conv''9'` {
			t.Errorf("$filter = %q", got)
		}
		fmt.Fprintf(w, `{
			"value": [{"id": "m-2", "conversationId": "conv'9", "createdDateTime": "2026-07-03T09:00:00Z"}],
			"@odata.nextLink": %q
		}`, "http://"+r.Host+"/me/messages?page=2")
	}))
	defer srv.Close()

	c := &Client{HTTPClient: srv.Client(), BaseURL: srv.URL}
	msgs, err := c.ListConversationMessages(context.Background(), "tok-123", "conv'9")
	if err != nil {
		t.Fatalf("ListConversationMessages: %v", err)
	}
	if !page2 {
		t.Error("pagination nextLink not followed")
	}
	if len(msgs) != 2 {
		t.Fatalf("len = %d, want 2", len(msgs))
	}
	// oldest → newest (the entity.Thread convention), sorted client-side
	// because Graph rejects $orderby combined with this $filter.
	if msgs[0].ID != "m-1" || msgs[1].ID != "m-2" {
		t.Errorf("order = %s, %s; want m-1, m-2", msgs[0].ID, msgs[1].ID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd overseer && go test ./internal/gateway/graph/`
Expected: FAIL — `c.ListConversationMessages undefined`

- [ ] **Step 3: Implement** (append to `graph.go`; add `"sort"` and `"strings"` to imports)

```go
// ListConversationMessages fetches every message in a conversation — the
// Audit read path: history is re-fetched from Graph at audit time, never
// stored. Results are sorted oldest → newest (the entity.Thread convention)
// here, because Graph rejects $orderby combined with the conversationId
// $filter (InefficientFilter).
func (c *Client) ListConversationMessages(ctx context.Context, accessToken, conversationID string) ([]entity.EmailMessage, error) {
	q := url.Values{}
	q.Set("$select", "id,conversationId,conversationIndex,from,subject,body,bodyPreview,webLink,createdDateTime")
	q.Set("$filter", fmt.Sprintf("conversationId eq '%s'", strings.ReplaceAll(conversationID, "'", "''")))
	q.Set("$top", fmt.Sprintf("%d", pageSize))
	next := c.BaseURL + "/me/messages?" + q.Encode()

	var out []entity.EmailMessage
	for next != "" && len(out) < maxMessages {
		page, err := c.fetchPage(ctx, accessToken, next)
		if err != nil {
			return nil, err
		}
		for _, gm := range page.Value {
			out = append(out, toEntity(gm))
		}
		next = page.NextLink
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedDateTime.Before(out[j].CreatedDateTime) })
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd overseer && go test ./internal/gateway/graph/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add overseer/internal/gateway/graph/
git commit -m "feat: graph gateway ListConversationMessages for audit thread re-fetch"
```

---

### Task 3: `gateway/llm.FindDiscrepancies`

**Files:**
- Modify: `overseer/internal/gateway/llm/llm.go` (append section)
- Create: `overseer/internal/gateway/llm/llm_test.go`

**Interfaces:**
- Consumes: existing `callJSON`, `truncate`, `maxBodyChars`, `maxThreadMessages` in `llm.go`; `entity.DiscrepancyFinding` etc. from Task 1.
- Produces: `func (g *Gateway) FindDiscrepancies(ctx context.Context, newMsg entity.EmailMessage, history []entity.EmailMessage) ([]entity.DiscrepancyFinding, error)` — Task 5's `DiscrepancyLLM` port has this exact signature. Internal: `toFindings(out findingsResult, history []entity.EmailMessage) ([]entity.DiscrepancyFinding, error)` validates model output (the unit-testable core; the model call itself follows the package's existing untested-`complete` pattern).

- [ ] **Step 1: Write the failing test**

```go
// overseer/internal/gateway/llm/llm_test.go
package llm

import (
	"testing"

	"github.com/thirdeye/overseer/internal/entity"
)

func auditHistory() []entity.EmailMessage {
	return []entity.EmailMessage{
		{ID: "m-1", WebLink: "https://outlook.example/m-1"},
		{ID: "m-2", WebLink: "https://outlook.example/m-2"},
	}
}

func validFinding() finding {
	return finding{
		Explanation:      "history agrees $41.00/unit; new message states $42.50/unit",
		Title:            "Price raised $41.00 → $42.50/unit",
		Priority:         "high",
		Type:             "price",
		PreviouslyAgreed: "$41.00/unit",
		NewMetric:        "$42.50/unit",
		Quotes:           []findingQuote{{Quote: "we agree on $41.00/unit", MessageIndex: 1}},
	}
}

func TestToFindingsMapsQuoteLinks(t *testing.T) {
	got, err := toFindings(findingsResult{Findings: []finding{validFinding()}}, auditHistory())
	if err != nil {
		t.Fatalf("toFindings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	f := got[0]
	if f.Title != "Price raised $41.00 → $42.50/unit" || f.Priority != entity.PriorityHigh {
		t.Errorf("finding = %+v", f)
	}
	if f.Detail.Type != entity.MetricPrice || f.Detail.PreviouslyAgreed != "$41.00/unit" || f.Detail.NewMetric != "$42.50/unit" {
		t.Errorf("detail = %+v", f.Detail)
	}
	if len(f.Quotes) != 1 || f.Quotes[0].MessageLink != "https://outlook.example/m-2" {
		t.Errorf("quotes = %+v", f.Quotes)
	}
}

func TestToFindingsEmptyIsClean(t *testing.T) {
	got, err := toFindings(findingsResult{}, auditHistory())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty, nil", got, err)
	}
}

func TestToFindingsRejectsBadOutput(t *testing.T) {
	cases := map[string]func(f *finding){
		"empty title":        func(f *finding) { f.Title = "" },
		"invalid priority":   func(f *finding) { f.Priority = "urgent" },
		"invalid type":       func(f *finding) { f.Type = "weight" },
		"index out of range": func(f *finding) { f.Quotes[0].MessageIndex = 2 },
		"negative index":     func(f *finding) { f.Quotes[0].MessageIndex = -1 },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			f := validFinding()
			corrupt(&f)
			if _, err := toFindings(findingsResult{Findings: []finding{f}}, auditHistory()); err == nil {
				t.Fatal("want validation error, got nil")
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd overseer && go test ./internal/gateway/llm/`
Expected: FAIL — `undefined: finding`, `undefined: findingsResult`, `undefined: toFindings`

- [ ] **Step 3: Implement** (append to `llm.go`, after the ExtractOrderFields section, before "shared plumbing")

```go
// --- FindDiscrepancies --------------------------------------------------------

const discrepancySystemPrompt = `You audit a wholesale distributor's supplier email thread for buyer/seller discrepancies.
Input: JSON with "newMessage" (the message that just arrived) and "history" (the earlier messages in the same conversation, oldest to newest, each carrying its index).
Compare the new message against what the two parties previously agreed in the history. Report a finding ONLY when the history establishes an agreed value (a price, a delivery/ship date, or a quantity/unit) and the new message states a different value for the same thing.
Field guidance:
- title: one short line naming the change, e.g. "Price raised $41.00 → $42.50/unit".
- priority: "high" (price increases, major delays, quantity shortfalls), "medium" (moderate changes), "low" (minor slips).
- type: "price", "date", or "unit" — the metric that changed.
- previouslyAgreed: the agreed value as stated in the history.
- newMetric: the conflicting value in the new message.
- quotes: verbatim quotes from the HISTORY that evidence the previously agreed value; each quote carries the index of the history message it came from.
In each finding, state the explanation first, then the fields. Do not report normal updates the parties agree on, restatements of the same value, or first-time values with no prior agreement. When in doubt, report nothing: an empty findings list is the correct output for a clean thread.`

var discrepancySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"findings": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"explanation":      map[string]any{"type": "string"},
					"title":            map[string]any{"type": "string"},
					"priority":         map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
					"type":             map[string]any{"type": "string", "enum": []string{"price", "date", "unit"}},
					"previouslyAgreed": map[string]any{"type": "string"},
					"newMetric":        map[string]any{"type": "string"},
					"quotes": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"quote":        map[string]any{"type": "string"},
								"messageIndex": map[string]any{"type": "integer"},
							},
							"required":             []string{"quote", "messageIndex"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"explanation", "title", "priority", "type", "previouslyAgreed", "newMetric", "quotes"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"findings"},
	"additionalProperties": false,
}

type findingQuote struct {
	Quote        string `json:"quote"`
	MessageIndex int    `json:"messageIndex"`
}

type finding struct {
	Explanation      string         `json:"explanation"`
	Title            string         `json:"title"`
	Priority         string         `json:"priority"`
	Type             string         `json:"type"`
	PreviouslyAgreed string         `json:"previouslyAgreed"`
	NewMetric        string         `json:"newMetric"`
	Quotes           []findingQuote `json:"quotes"`
}

type findingsResult struct {
	Findings []finding `json:"findings"`
}

// FindDiscrepancies compares the new message against the conversation history
// and returns zero or more validated findings. Quote message indexes are
// resolved to webLinks here, at the gateway boundary.
func (g *Gateway) FindDiscrepancies(ctx context.Context, newMsg entity.EmailMessage, history []entity.EmailMessage) ([]entity.DiscrepancyFinding, error) {
	type auditMsg struct {
		Index    int    `json:"index"`
		From     string `json:"from"`
		FromName string `json:"fromName"`
		Date     string `json:"date"`
		Subject  string `json:"subject"`
		Body     string `json:"body"`
	}
	if len(history) > maxThreadMessages {
		history = history[len(history)-maxThreadMessages:]
	}
	toAuditMsg := func(i int, m entity.EmailMessage) auditMsg {
		body := m.Body
		if body == "" {
			body = m.BodyPreview
		}
		return auditMsg{
			Index: i, From: m.From, FromName: m.FromName,
			Date: m.CreatedDateTime.Format(time.RFC3339), Subject: m.Subject,
			Body: truncate(body, maxBodyChars),
		}
	}
	hist := make([]auditMsg, 0, len(history))
	for i, m := range history {
		hist = append(hist, toAuditMsg(i, m))
	}
	payload := map[string]any{"newMessage": toAuditMsg(-1, newMsg), "history": hist}
	userMsg, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("llm: marshal audit payload: %w", err)
	}

	var findings []entity.DiscrepancyFinding
	err = g.callJSON(ctx, "FindDiscrepancies", discrepancySystemPrompt, string(userMsg), discrepancySchema, func(raw []byte) error {
		var out findingsResult
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("invalid JSON: %w", err)
		}
		fs, err := toFindings(out, history)
		if err != nil {
			return err
		}
		findings = fs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return findings, nil
}

// toFindings validates model output at the gateway boundary: enums must
// match, titles must be present, and quote indexes must point into the
// history the model was shown.
func toFindings(out findingsResult, history []entity.EmailMessage) ([]entity.DiscrepancyFinding, error) {
	findings := make([]entity.DiscrepancyFinding, 0, len(out.Findings))
	for _, f := range out.Findings {
		if f.Title == "" {
			return nil, fmt.Errorf("finding with empty title")
		}
		priority := entity.Priority(f.Priority)
		if !entity.ValidPriority(priority) {
			return nil, fmt.Errorf("invalid priority %q", f.Priority)
		}
		metric := entity.MetricType(f.Type)
		if !entity.ValidMetricType(metric) {
			return nil, fmt.Errorf("invalid metric type %q", f.Type)
		}
		quotes := make([]entity.DiscrepancyQuote, 0, len(f.Quotes))
		for _, q := range f.Quotes {
			if q.MessageIndex < 0 || q.MessageIndex >= len(history) {
				return nil, fmt.Errorf("quote messageIndex %d out of range", q.MessageIndex)
			}
			quotes = append(quotes, entity.DiscrepancyQuote{Quote: q.Quote, MessageLink: history[q.MessageIndex].WebLink})
		}
		findings = append(findings, entity.DiscrepancyFinding{
			Title:    f.Title,
			Priority: priority,
			Detail: entity.DiscrepancyDetail{
				PreviouslyAgreed: f.PreviouslyAgreed,
				NewMetric:        f.NewMetric,
				Type:             metric,
			},
			Quotes: quotes,
		})
	}
	return findings, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd overseer && go test ./internal/gateway/llm/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add overseer/internal/gateway/llm/
git commit -m "feat: llm gateway FindDiscrepancies with validated structured output"
```

---

### Task 4: Test fakes — FakeGraph thread fetch, FakeLLM findings, InMemDiscrepancies

**Files:**
- Modify: `overseer/internal/testutil/fakes.go`
- Modify: `overseer/internal/testutil/fakes_test.go` (append test)

**Interfaces:**
- Consumes: entity types from Task 1.
- Produces (used by Tasks 5, 6, 10):
  - `FakeGraph.ConversationsByID map[string][]entity.EmailMessage`, `FakeGraph.ListConvErr error`, `FakeGraph.ConversationsFetched []string`, method `ListConversationMessages(ctx, accessToken, conversationID string) ([]entity.EmailMessage, error)`
  - `FakeLLM.Findings map[string][]entity.DiscrepancyFinding` (keyed by **new message ID**), `FakeLLM.FindErr error`, `FakeLLM.FindCalls int`, `FakeLLM.HistoriesSeen [][]string` (message ids per call), method `FindDiscrepancies(ctx, newMsg entity.EmailMessage, history []entity.EmailMessage) ([]entity.DiscrepancyFinding, error)`
  - `NewInMemDiscrepancies() *InMemDiscrepancies` with `Insert(ctx, d entity.Discrepancy) (entity.Discrepancy, error)`, `ListByUser(ctx, userID uuid.UUID, status *entity.DiscrepancyStatus) ([]entity.Discrepancy, error)`, `UpdateStatus(ctx, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, bool, error)`, plus `Count() int` and `All() []entity.Discrepancy` helpers.

- [ ] **Step 1: Write the failing test** (append to `fakes_test.go`)

```go
func TestInMemDiscrepancies(t *testing.T) {
	repo := NewInMemDiscrepancies()
	userID := uuid.New()

	stored, err := repo.Insert(context.Background(), entity.Discrepancy{
		AccountID: uuid.New(), UserID: userID, ConversationID: "conv-1",
		Status: entity.DiscrepancyOpen, CompanyName: "Delmar Tooling",
		Priority: entity.PriorityHigh, Title: "Price raised",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID == uuid.Nil {
		t.Fatal("Insert did not mint an id")
	}

	open := entity.DiscrepancyOpen
	rows, err := repo.ListByUser(context.Background(), userID, &open)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListByUser = %v, %v", rows, err)
	}

	updated, found, err := repo.UpdateStatus(context.Background(), userID, stored.ID, entity.DiscrepancyResolved)
	if err != nil || !found || updated.Status != entity.DiscrepancyResolved {
		t.Fatalf("UpdateStatus = %+v, %v, %v", updated, found, err)
	}
	if rows, _ = repo.ListByUser(context.Background(), userID, &open); len(rows) != 0 {
		t.Fatalf("open rows after resolve = %d, want 0", len(rows))
	}
	if _, found, _ = repo.UpdateStatus(context.Background(), uuid.New(), stored.ID, entity.DiscrepancyOpen); found {
		t.Fatal("another user's update matched the row")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd overseer && go test ./internal/testutil/`
Expected: FAIL — `undefined: NewInMemDiscrepancies`

- [ ] **Step 3: Implement the fakes** (append to `fakes.go`, in the matching sections)

Add to the `FakeGraph` struct (after the subscription fields):

```go
	// ListConversationMessages support: conversation id → full thread,
	// oldest → newest (like the real gateway returns).
	ConversationsByID    map[string][]entity.EmailMessage
	ListConvErr          error
	ConversationsFetched []string
```

Add after `FakeGraph.RenewSubscription`:

```go
func (f *FakeGraph) ListConversationMessages(_ context.Context, accessToken, conversationID string) ([]entity.EmailMessage, error) {
	f.TokensSeen = append(f.TokensSeen, accessToken)
	f.ConversationsFetched = append(f.ConversationsFetched, conversationID)
	if f.ListConvErr != nil {
		return nil, f.ListConvErr
	}
	return f.ConversationsByID[conversationID], nil
}
```

Add to the `FakeLLM` struct:

```go
	// Findings maps the new message's id → what FindDiscrepancies returns.
	Findings map[string][]entity.DiscrepancyFinding
	FindErr  error

	FindCalls     int
	HistoriesSeen [][]string // message ids of each history passed in
```

Add after `FakeLLM.ExtractOrderFields`:

```go
func (f *FakeLLM) FindDiscrepancies(_ context.Context, newMsg entity.EmailMessage, history []entity.EmailMessage) ([]entity.DiscrepancyFinding, error) {
	f.FindCalls++
	ids := make([]string, 0, len(history))
	for _, m := range history {
		ids = append(ids, m.ID)
	}
	f.HistoriesSeen = append(f.HistoriesSeen, ids)
	if f.FindErr != nil {
		return nil, f.FindErr
	}
	return f.Findings[newMsg.ID], nil
}
```

Add a new section before `--- EventRecorder ---`:

```go
// --- InMemDiscrepancies --------------------------------------------------------

// InMemDiscrepancies is an in-memory DiscrepancyRepo.
type InMemDiscrepancies struct {
	mu      sync.Mutex
	rows    map[uuid.UUID]entity.Discrepancy
	Inserts int
}

func NewInMemDiscrepancies() *InMemDiscrepancies {
	return &InMemDiscrepancies{rows: make(map[uuid.UUID]entity.Discrepancy)}
}

func (r *InMemDiscrepancies) Insert(_ context.Context, d entity.Discrepancy) (entity.Discrepancy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Inserts++
	d.ID = uuid.New()
	d.CreatedAt = time.Now().Add(time.Duration(len(r.rows)) * time.Millisecond)
	d.UpdatedAt = d.CreatedAt
	r.rows[d.ID] = d
	return d, nil
}

func (r *InMemDiscrepancies) ListByUser(_ context.Context, userID uuid.UUID, status *entity.DiscrepancyStatus) ([]entity.Discrepancy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []entity.Discrepancy
	for _, d := range r.rows {
		if d.UserID != userID {
			continue
		}
		if status != nil && d.Status != *status {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (r *InMemDiscrepancies) UpdateStatus(_ context.Context, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.rows[id]
	if !ok || d.UserID != userID {
		return entity.Discrepancy{}, false, nil
	}
	d.Status = s
	d.UpdatedAt = time.Now()
	r.rows[id] = d
	return d, true, nil
}

func (r *InMemDiscrepancies) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

func (r *InMemDiscrepancies) All() []entity.Discrepancy {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]entity.Discrepancy, 0, len(r.rows))
	for _, d := range r.rows {
		out = append(out, d)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd overseer && go test ./internal/testutil/ && go vet ./...`
Expected: PASS, no vet output

- [ ] **Step 5: Commit**

```bash
git add overseer/internal/testutil/
git commit -m "feat: testutil fakes for audit — thread fetch, findings, in-mem discrepancies"
```

---

### Task 5: `CatchDiscrepancies` usecase

**Files:**
- Modify: `overseer/internal/usecase/orders/ports.go` (append three ports)
- Create: `overseer/internal/usecase/orders/catch_discrepancies.go`
- Create: `overseer/internal/usecase/orders/catch_discrepancies_test.go`

**Interfaces:**
- Consumes: Task 4 fakes; Task 1 entity types; existing `EventPublisher` port in `ports.go`.
- Produces: `orders.CatchDiscrepancies` struct with fields `Graph ThreadFetcher`, `LLM DiscrepancyLLM`, `Repo DiscrepancyRepo`, `Events EventPublisher` and method `Execute(ctx context.Context, accessToken string, order entity.Order, newMsg entity.EmailMessage) ([]entity.Discrepancy, error)` — Task 6's `DiscrepancyCatcher` port matches this signature exactly. Also the three port interfaces below, satisfied structurally by `graph.Client` (Task 2), `llm.Gateway` (Task 3), `repo.Discrepancies` (Task 8) / `testutil.InMemDiscrepancies` (Task 4).

- [ ] **Step 1: Add the ports** (append to `overseer/internal/usecase/orders/ports.go`)

```go
// ThreadFetcher is the slice of gateway/graph the Audit needs: conversation
// history is re-fetched from Graph at audit time (no messages table; Graph
// is the source of truth).
type ThreadFetcher interface {
	// ListConversationMessages returns every message in the conversation,
	// oldest → newest.
	ListConversationMessages(ctx context.Context, accessToken, conversationID string) ([]entity.EmailMessage, error)
}

// DiscrepancyLLM is the slice of gateway/llm the Audit needs.
type DiscrepancyLLM interface {
	// FindDiscrepancies compares the new message against the conversation
	// history and returns zero or more findings.
	FindDiscrepancies(ctx context.Context, newMsg entity.EmailMessage, history []entity.EmailMessage) ([]entity.DiscrepancyFinding, error)
}

// DiscrepancyRepo is the discrepancies-table port. Every query filters by owner.
type DiscrepancyRepo interface {
	// Insert stores a finding and returns the stored row (server-minted id,
	// timestamps) so events carry the authoritative row.
	Insert(ctx context.Context, d entity.Discrepancy) (entity.Discrepancy, error)
	// ListByUser returns the user's discrepancies, newest first, optionally
	// filtered by status (nil = all).
	ListByUser(ctx context.Context, userID uuid.UUID, status *entity.DiscrepancyStatus) ([]entity.Discrepancy, error)
	// UpdateStatus sets the status of the user's discrepancy; found=false
	// when no such row belongs to the user.
	UpdateStatus(ctx context.Context, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, bool, error)
}
```

- [ ] **Step 2: Write the failing test**

```go
// overseer/internal/usecase/orders/catch_discrepancies_test.go
package orders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

type auditFixture struct {
	uc     *CatchDiscrepancies
	order  entity.Order
	graph  *testutil.FakeGraph
	llm    *testutil.FakeLLM
	repo   *testutil.InMemDiscrepancies
	events *testutil.EventRecorder
}

func newAuditFixture() *auditFixture {
	graph := &testutil.FakeGraph{ConversationsByID: map[string][]entity.EmailMessage{}}
	llm := &testutil.FakeLLM{Findings: map[string][]entity.DiscrepancyFinding{}}
	repo := testutil.NewInMemDiscrepancies()
	events := &testutil.EventRecorder{}
	return &auditFixture{
		uc: &CatchDiscrepancies{Graph: graph, LLM: llm, Repo: repo, Events: events},
		order: entity.Order{
			ID: uuid.New(), AccountID: uuid.New(), UserID: uuid.New(),
			ConversationID: "conv-1", CompanyName: "Delmar Tooling",
			EmailAddress: "dana@delmar.com", OrderID: 4471,
			Status: entity.OrderStatusConfirmed,
		},
		graph: graph, llm: llm, repo: repo, events: events,
	}
}

func auditMsg(id, link string, daysAgo int) entity.EmailMessage {
	return entity.EmailMessage{
		ID: id, ConversationID: "conv-1", From: "dana@delmar.com",
		FromName: "Dana Delmar", WebLink: link,
		CreatedDateTime: time.Now().AddDate(0, 0, -daysAgo),
	}
}

func priceFinding() entity.DiscrepancyFinding {
	return entity.DiscrepancyFinding{
		Title:    "Price raised $41.00 → $42.50/unit",
		Priority: entity.PriorityHigh,
		Detail: entity.DiscrepancyDetail{
			PreviouslyAgreed: "$41.00/unit", NewMetric: "$42.50/unit", Type: entity.MetricPrice,
		},
		Quotes: []entity.DiscrepancyQuote{{Quote: "we agree on $41.00/unit", MessageLink: "https://outlook.example/m-1"}},
	}
}

func TestFindingStoredAndPublished(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-2", "https://outlook.example/m-2", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{
		auditMsg("m-1", "https://outlook.example/m-1", 3), newMsg,
	}
	f.llm.Findings["m-2"] = []entity.DiscrepancyFinding{priceFinding()}

	stored, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(stored) != 1 || f.repo.Count() != 1 {
		t.Fatalf("stored = %d, repo rows = %d; want 1, 1", len(stored), f.repo.Count())
	}

	d := stored[0]
	if d.ID == uuid.Nil {
		t.Error("stored row has no server-minted id")
	}
	if d.AccountID != f.order.AccountID || d.UserID != f.order.UserID ||
		d.ConversationID != "conv-1" || d.CompanyName != "Delmar Tooling" ||
		d.EmailAddress != "dana@delmar.com" || d.OrderID != 4471 {
		t.Errorf("ownership/identity not stamped from order: %+v", d)
	}
	if d.Status != entity.DiscrepancyOpen {
		t.Errorf("status = %q, want open", d.Status)
	}
	if d.EmailDeeplinkURL != "https://outlook.example/m-2" {
		t.Errorf("deeplink = %q, want the new message's webLink", d.EmailDeeplinkURL)
	}
	if d.Title != "Price raised $41.00 → $42.50/unit" || d.Priority != entity.PriorityHigh {
		t.Errorf("finding fields lost: %+v", d)
	}

	found := f.events.OfType(entity.EventDiscrepancyFound)
	if len(found) != 1 {
		t.Fatalf("discrepancy.found events = %d, want 1", len(found))
	}
	if ev, ok := found[0].Data.(entity.Discrepancy); !ok || ev.ID != d.ID {
		t.Errorf("event carries %+v, want the stored row", found[0].Data)
	}
}

func TestCleanThreadStoresNothing(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-2", "https://outlook.example/m-2", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{
		auditMsg("m-1", "https://outlook.example/m-1", 3), newMsg,
	}
	// no findings configured for m-2 → LLM returns nil

	stored, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(stored) != 0 || f.repo.Count() != 0 {
		t.Errorf("clean thread stored %d rows", f.repo.Count())
	}
	if len(f.events.OfType(entity.EventDiscrepancyFound)) != 0 {
		t.Error("clean thread published an event")
	}
	if f.llm.FindCalls != 1 {
		t.Errorf("FindCalls = %d, want 1", f.llm.FindCalls)
	}
}

func TestFirstMessageSkipsAudit(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-1", "https://outlook.example/m-1", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{newMsg} // no history

	stored, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(stored) != 0 || f.llm.FindCalls != 0 {
		t.Errorf("first message: stored=%d FindCalls=%d, want 0, 0", len(stored), f.llm.FindCalls)
	}
}

func TestNewMessageExcludedFromHistory(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-3", "https://outlook.example/m-3", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{
		auditMsg("m-1", "https://outlook.example/m-1", 5),
		auditMsg("m-2", "https://outlook.example/m-2", 2),
		newMsg,
	}

	if _, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(f.llm.HistoriesSeen) != 1 {
		t.Fatalf("FindDiscrepancies calls = %d, want 1", len(f.llm.HistoriesSeen))
	}
	got := f.llm.HistoriesSeen[0]
	if len(got) != 2 || got[0] != "m-1" || got[1] != "m-2" {
		t.Errorf("history = %v, want [m-1 m-2] (new message excluded)", got)
	}
}

func TestLLMErrorStoresNothing(t *testing.T) {
	f := newAuditFixture()
	newMsg := auditMsg("m-2", "https://outlook.example/m-2", 0)
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{
		auditMsg("m-1", "https://outlook.example/m-1", 3), newMsg,
	}
	f.llm.FindErr = errors.New("model 503")

	if _, err := f.uc.Execute(context.Background(), "tok", f.order, newMsg); err == nil {
		t.Fatal("want error from LLM failure")
	}
	if f.repo.Count() != 0 || len(f.events.Events) != 0 {
		t.Error("LLM failure left partial writes or events")
	}
}

func TestGraphErrorSurfaces(t *testing.T) {
	f := newAuditFixture()
	f.graph.ListConvErr = errors.New("graph 503")

	if _, err := f.uc.Execute(context.Background(), "tok", f.order, auditMsg("m-2", "", 0)); err == nil {
		t.Fatal("want error from Graph failure")
	}
	if f.llm.FindCalls != 0 {
		t.Error("LLM called despite Graph failure")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd overseer && go test ./internal/usecase/orders/`
Expected: FAIL — `undefined: CatchDiscrepancies`

- [ ] **Step 4: Implement the usecase**

```go
// overseer/internal/usecase/orders/catch_discrepancies.go
package orders

import (
	"context"
	"fmt"

	"github.com/thirdeye/overseer/internal/entity"
)

// CatchDiscrepancies is the Audit usecase (spec: usecase/orders/CatchDiscrepancies).
// It rides the webhook job: when a known order conversation gets a new
// message, re-fetch the thread from Graph, ask the LLM to compare the new
// message against the history, store every finding, and push
// discrepancy.found for each. A clean thread stores nothing and publishes
// nothing — the DB stays the source of truth for clients that connect later.
type CatchDiscrepancies struct {
	Graph  ThreadFetcher
	LLM    DiscrepancyLLM
	Repo   DiscrepancyRepo
	Events EventPublisher
}

// Execute audits one new message in an order conversation. The order row
// provides ownership and business identity (account/user/company/PO); the
// new message provides the deeplink. The first message of a conversation has
// no history to contradict, so it never audits.
func (uc *CatchDiscrepancies) Execute(ctx context.Context, accessToken string, order entity.Order, newMsg entity.EmailMessage) ([]entity.Discrepancy, error) {
	msgs, err := uc.Graph.ListConversationMessages(ctx, accessToken, order.ConversationID)
	if err != nil {
		return nil, fmt.Errorf("fetch conversation %s: %w", order.ConversationID, err)
	}

	history := make([]entity.EmailMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.ID != newMsg.ID {
			history = append(history, m)
		}
	}
	if len(history) == 0 {
		return nil, nil // nothing previously agreed — nothing to contradict
	}

	findings, err := uc.LLM.FindDiscrepancies(ctx, newMsg, history)
	if err != nil {
		return nil, fmt.Errorf("find discrepancies (conversation %s): %w", order.ConversationID, err)
	}

	stored := make([]entity.Discrepancy, 0, len(findings))
	for _, f := range findings {
		d, err := uc.Repo.Insert(ctx, entity.Discrepancy{
			AccountID:        order.AccountID,
			UserID:           order.UserID,
			ConversationID:   order.ConversationID,
			Status:           entity.DiscrepancyOpen,
			CompanyName:      order.CompanyName,
			EmailAddress:     order.EmailAddress,
			EmailDeeplinkURL: newMsg.WebLink,
			OrderID:          order.OrderID,
			Priority:         f.Priority,
			Title:            f.Title,
			Detail:           f.Detail,
			Quotes:           f.Quotes,
		})
		if err != nil {
			return stored, fmt.Errorf("insert discrepancy: %w", err)
		}
		uc.Events.Publish(entity.Event{Type: entity.EventDiscrepancyFound, Data: d})
		stored = append(stored, d)
	}
	return stored, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd overseer && go test ./internal/usecase/orders/`
Expected: PASS (all six new tests plus the existing package tests)

- [ ] **Step 6: Commit**

```bash
git add overseer/internal/usecase/orders/
git commit -m "feat: CatchDiscrepancies audit usecase"
```

---

### Task 6: Audit rides `HandleChangeMessage`

**Files:**
- Modify: `overseer/internal/usecase/messages/ports.go` (append port)
- Modify: `overseer/internal/usecase/messages/handle_change_message.go`
- Modify: `overseer/internal/usecase/messages/handle_change_message_test.go` (append tests)

**Interfaces:**
- Consumes: `orders.CatchDiscrepancies.Execute` signature from Task 5 (satisfied structurally — `messages` does NOT import `usecase/orders`).
- Produces: `HandleChangeMessage.Audit DiscrepancyCatcher` field (nil = audit leg disabled, so existing tests/wiring keep working until Task 11 wires it).

- [ ] **Step 1: Write the failing tests** (append to `handle_change_message_test.go`)

```go
type auditCall struct {
	token string
	order entity.Order
	msg   entity.EmailMessage
}

type stubAudit struct {
	calls []auditCall
	err   error
}

func (s *stubAudit) Execute(_ context.Context, token string, order entity.Order, msg entity.EmailMessage) ([]entity.Discrepancy, error) {
	s.calls = append(s.calls, auditCall{token: token, order: order, msg: msg})
	return nil, s.err
}

func TestAuditRunsAfterUpsert(t *testing.T) {
	f := newFixture(t)
	audit := &stubAudit{}
	f.uc.Audit = audit

	f.graph.MessagesByID["msg-2"] = entity.EmailMessage{
		ID: "msg-2", ConversationID: "conv-1", From: "dana@delmar.com",
		FromName: "Dana Delmar", WebLink: "https://outlook.example/msg-2",
		CreatedDateTime: time.Now(),
	}
	if err := f.repo.Upsert(context.Background(), entity.Order{
		AccountID: f.user.AccountID, UserID: f.user.ID,
		ConversationID: "conv-1", CompanyName: "Delmar Tooling", OrderID: 4471,
		Status: entity.OrderStatusConfirmed,
	}); err != nil {
		t.Fatal(err)
	}
	f.llm.Extractions["conv-1"] = entity.OrderExtraction{Status: entity.OrderStatusInTransit}

	if _, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-1", MessageID: "msg-2"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(audit.calls) != 1 {
		t.Fatalf("audit calls = %d, want 1", len(audit.calls))
	}
	call := audit.calls[0]
	if call.token != "graph-tok" {
		t.Errorf("audit token = %q, want the resolved user's graph token", call.token)
	}
	if call.order.ConversationID != "conv-1" || call.order.UserID != f.user.ID {
		t.Errorf("audit order = %+v, want the saved conv-1 row", call.order)
	}
	if call.msg.ID != "msg-2" {
		t.Errorf("audit msg = %q, want msg-2", call.msg.ID)
	}
}

func TestAuditSkippedForNonOrder(t *testing.T) {
	f := newFixture(t)
	audit := &stubAudit{}
	f.uc.Audit = audit

	f.graph.MessagesByID["msg-junk"] = entity.EmailMessage{
		ID: "msg-junk", ConversationID: "conv-junk", From: "news@saas.com",
		CreatedDateTime: time.Now(),
	}
	f.llm.FilterResult = nil // classified as not an order

	if _, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-1", MessageID: "msg-junk"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(audit.calls) != 0 {
		t.Error("audit ran on a non-order conversation")
	}
}

func TestAuditFailureDoesNotUndoOrder(t *testing.T) {
	f := newFixture(t)
	audit := &stubAudit{err: errors.New("llm 503")}
	f.uc.Audit = audit

	f.graph.MessagesByID["msg-1"] = entity.EmailMessage{
		ID: "msg-1", ConversationID: "conv-new", From: "dana@delmar.com",
		FromName: "Dana Delmar", CreatedDateTime: time.Now(),
	}
	f.llm.FilterResult = []string{"conv-new"}
	f.llm.Extractions["conv-new"] = entity.OrderExtraction{
		CompanyName: "Delmar Tooling", OrderID: 4471, Status: entity.OrderStatusConfirmed,
	}

	_, err := f.uc.Execute(context.Background(), HandleInput{SubscriptionID: "sub-1", MessageID: "msg-1"})
	if err == nil {
		t.Fatal("want audit error surfaced to the caller's log")
	}
	if f.repo.Count() != 1 {
		t.Error("audit failure lost the order upsert")
	}
	if len(f.events.OfType(entity.EventOrderUpdated)) != 1 {
		t.Error("audit failure suppressed order.updated")
	}
}
```

Also add `"errors"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd overseer && go test ./internal/usecase/messages/`
Expected: FAIL — `f.uc.Audit undefined`

- [ ] **Step 3: Implement**

Append to `overseer/internal/usecase/messages/ports.go`:

```go
// DiscrepancyCatcher is the Audit hook — usecase/orders.CatchDiscrepancies
// satisfies it structurally. It audits the new message against its
// conversation history and returns whatever it stored.
type DiscrepancyCatcher interface {
	Execute(ctx context.Context, accessToken string, order entity.Order, newMsg entity.EmailMessage) ([]entity.Discrepancy, error)
}
```

In `handle_change_message.go`, add the field to the struct (after `Events EventPublisher`):

```go
	// Audit is the Discrepancy leg (spec: Audit rides this job and has no
	// infrastructure of its own). nil disables it.
	Audit DiscrepancyCatcher
```

Replace the tail of `Execute` — the current

```go
	uc.Events.Publish(entity.Event{Type: entity.EventOrderUpdated, Data: order})
	return outcome, nil
```

with

```go
	uc.Events.Publish(entity.Event{Type: entity.EventOrderUpdated, Data: order})

	// Audit rides this job. The order work above is already committed — an
	// audit failure surfaces to the caller's log but cannot undo the upsert,
	// and a Graph re-delivery would retry it.
	if uc.Audit != nil {
		if _, err := uc.Audit.Execute(ctx, token, order, msg); err != nil {
			return outcome, fmt.Errorf("audit (conversation %s): %w", msg.ConversationID, err)
		}
	}
	return outcome, nil
```

Also update the stale "Extension point" comment on the `HandleChangeMessage` doc comment: the Checkups sentence stays, but the Audit leg is now real — change the comment block to:

```go
// HandleChangeMessage is the webhook job (spec: jobs/HandleChangeMessage):
// notification → resolve subscription to user → fetch the message → LLM
// structure → merge-upsert into orders → order.updated on the realtime
// channel → Audit (CatchDiscrepancies). Graph delivers at-least-once, so the
// whole path is idempotent — the upsert is keyed on (user_id, conversation_id).
//
// Extension point (Checkups, not built yet): this is the only place that sees
// supplier replies — when checkup_messages exists, cancel pending rows with
// cancel_if_reply on this conversation here.
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd overseer && go test ./internal/usecase/messages/`
Expected: PASS (three new tests; existing five still green — `Audit` is nil in the old fixture)

- [ ] **Step 5: Commit**

```bash
git add overseer/internal/usecase/messages/
git commit -m "feat: HandleChangeMessage runs the audit leg after order upsert"
```

---

### Task 7: `discrepancy.found` wire DTO on the ws hub

**Files:**
- Modify: `overseer/internal/controller/ws/hub.go`
- Modify: `overseer/internal/controller/ws/hub_test.go` (append test)

**Interfaces:**
- Consumes: `entity.Discrepancy` from Task 1; existing `toDTO` switch in `hub.go`.
- Produces: `entity.Discrepancy` serializes over `/ws` as the camelCase shape below (same field names Task 10 uses in the HTTP DTO — each edge owns its own copy, like `orderDTO` already does).

- [ ] **Step 1: Write the failing test** (append to `hub_test.go`)

```go
func TestToDTODiscrepancy(t *testing.T) {
	d := entity.Discrepancy{
		ID: uuid.New(), ConversationID: "conv-1",
		Status: entity.DiscrepancyOpen, CompanyName: "Delmar Tooling",
		EmailAddress: "dana@delmar.com", EmailDeeplinkURL: "https://outlook.example/msg-9",
		OrderID: 4471, Priority: entity.PriorityHigh,
		Title: "Price raised $41.00 → $42.50/unit",
		Detail: entity.DiscrepancyDetail{
			PreviouslyAgreed: "$41.00/unit", NewMetric: "$42.50/unit", Type: entity.MetricPrice,
		},
		Quotes:    []entity.DiscrepancyQuote{{Quote: "we agree on $41.00/unit", MessageLink: "https://outlook.example/m-2"}},
		CreatedAt: time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	}

	raw, err := json.Marshal(toDTO(d))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	if got["companyName"] != "Delmar Tooling" || got["status"] != "open" ||
		got["priority"] != "high" || got["orderId"] != float64(4471) ||
		got["title"] != "Price raised $41.00 → $42.50/unit" ||
		got["emailDeeplinkUrl"] != "https://outlook.example/msg-9" ||
		got["conversationId"] != "conv-1" {
		t.Errorf("dto = %v", got)
	}
	detail, ok := got["detail"].(map[string]any)
	if !ok || detail["previouslyAgreed"] != "$41.00/unit" ||
		detail["newMetric"] != "$42.50/unit" || detail["type"] != "price" {
		t.Errorf("detail = %v", got["detail"])
	}
	quotes, ok := got["quotes"].([]any)
	if !ok || len(quotes) != 1 {
		t.Fatalf("quotes = %v", got["quotes"])
	}
	q := quotes[0].(map[string]any)
	if q["quote"] != "we agree on $41.00/unit" || q["messageLink"] != "https://outlook.example/m-2" {
		t.Errorf("quote = %v", q)
	}
	if got["dateCreated"] != "2026-07-07T10:00:00Z" {
		t.Errorf("dateCreated = %v", got["dateCreated"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd overseer && go test ./internal/controller/ws/`
Expected: FAIL — the `default:` branch returns the raw entity, so `companyName` etc. are missing (Go field names serialize as `CompanyName`).

- [ ] **Step 3: Implement** (in `hub.go`, add DTO types after `orderDTO`, and a case in `toDTO`)

```go
type discrepancyDetailDTO struct {
	PreviouslyAgreed string `json:"previouslyAgreed"`
	NewMetric        string `json:"newMetric"`
	Type             string `json:"type"`
}

type discrepancyQuoteDTO struct {
	Quote       string `json:"quote"`
	MessageLink string `json:"messageLink,omitempty"`
}

// discrepancyDTO is discrepancy.found's wire shape — same camelCase fields
// the HTTP layer serves, mapped here because each edge owns its own DTOs.
type discrepancyDTO struct {
	ID               string                `json:"id"`
	ConversationID   string                `json:"conversationId"`
	Status           string                `json:"status"`
	CompanyName      string                `json:"companyName"`
	EmailAddress     string                `json:"emailAddress,omitempty"`
	EmailDeeplinkURL string                `json:"emailDeeplinkUrl,omitempty"`
	OrderID          int                   `json:"orderId"`
	Priority         string                `json:"priority"`
	Title            string                `json:"title"`
	Detail           discrepancyDetailDTO  `json:"detail"`
	Quotes           []discrepancyQuoteDTO `json:"quotes"`
	DateCreated      string                `json:"dateCreated"`
}
```

Add to the `toDTO` switch, after the `entity.Order` case:

```go
	case entity.Discrepancy:
		quotes := make([]discrepancyQuoteDTO, 0, len(d.Quotes))
		for _, q := range d.Quotes {
			quotes = append(quotes, discrepancyQuoteDTO{Quote: q.Quote, MessageLink: q.MessageLink})
		}
		return discrepancyDTO{
			ID:               d.ID.String(),
			ConversationID:   d.ConversationID,
			Status:           string(d.Status),
			CompanyName:      d.CompanyName,
			EmailAddress:     d.EmailAddress,
			EmailDeeplinkURL: d.EmailDeeplinkURL,
			OrderID:          d.OrderID,
			Priority:         string(d.Priority),
			Title:            d.Title,
			Detail: discrepancyDetailDTO{
				PreviouslyAgreed: d.Detail.PreviouslyAgreed,
				NewMetric:        d.Detail.NewMetric,
				Type:             string(d.Detail.Type),
			},
			Quotes:      quotes,
			DateCreated: d.CreatedAt.UTC().Format(time.RFC3339),
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd overseer && go test ./internal/controller/ws/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add overseer/internal/controller/ws/
git commit -m "feat: discrepancy.found wire DTO on realtime hub"
```

---

### Task 8: `repo/discrepancies.go` (Postgres)

**Files:**
- Create: `overseer/internal/repo/discrepancies.go`

**Interfaces:**
- Consumes: `nullString`/`deref` helpers from `repo/orders.go` (same package); Task 1 entities; the `discrepancies` table from Task 1's migration.
- Produces: `repo.Discrepancies` with `NewDiscrepancies(db *pgxpool.Pool) *Discrepancies` and methods `Insert`, `ListByUser`, `UpdateStatus` matching Task 5's `DiscrepancyRepo` port exactly. JSONB uses snake_case keys per the spec comments: `{"previously_agreed","new_metric","type"}` and `[{"quote","message_link"}]`.

No unit test — repo files in this codebase are exercised via the testcontainers upgrade path later (same as `orders.go`); verification is compile + vet + the compile-time conformance assert added in Task 11.

- [ ] **Step 1: Write the repo file**

```go
// overseer/internal/repo/discrepancies.go
package repo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thirdeye/overseer/internal/entity"
)

const insertDiscrepancySQL = `
	INSERT INTO discrepancies (account_id, user_id, conversation_id, status,
	                           company_name, email_address, email_deeplink_url, order_id,
	                           priority, discrepancy_title, discrepancy_detail, discrepancy_quotes)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	RETURNING id, account_id, user_id, conversation_id, status,
	          company_name, email_address, email_deeplink_url, order_id,
	          priority, discrepancy_title, discrepancy_detail, discrepancy_quotes,
	          created_at, updated_at`

const listDiscrepanciesSQL = `
	SELECT id, account_id, user_id, conversation_id, status,
	       company_name, email_address, email_deeplink_url, order_id,
	       priority, discrepancy_title, discrepancy_detail, discrepancy_quotes,
	       created_at, updated_at
	FROM discrepancies
	WHERE user_id = $1
	  AND ($2::text IS NULL OR status = $2)
	ORDER BY created_at DESC` // GET /discrepancies?status= (idx_discrepancies_user)

const updateDiscrepancyStatusSQL = `
	UPDATE discrepancies SET status = $3, updated_at = now()
	WHERE user_id = $1 AND id = $2
	RETURNING id, account_id, user_id, conversation_id, status,
	          company_name, email_address, email_deeplink_url, order_id,
	          priority, discrepancy_title, discrepancy_detail, discrepancy_quotes,
	          created_at, updated_at`

// detailRow / quoteRow are the JSONB column shapes — snake_case keys, per the
// spec's Schema Updates comments. Mapping lives here and nowhere else.
type detailRow struct {
	PreviouslyAgreed string `json:"previously_agreed"`
	NewMetric        string `json:"new_metric"`
	Type             string `json:"type"`
}

type quoteRow struct {
	Quote       string `json:"quote"`
	MessageLink string `json:"message_link"`
}

type Discrepancies struct {
	db *pgxpool.Pool
}

func NewDiscrepancies(db *pgxpool.Pool) *Discrepancies { return &Discrepancies{db: db} }

// Insert stores a finding and returns the stored row so events carry the
// authoritative id/timestamps.
func (r *Discrepancies) Insert(ctx context.Context, d entity.Discrepancy) (entity.Discrepancy, error) {
	detail, err := json.Marshal(detailRow{
		PreviouslyAgreed: d.Detail.PreviouslyAgreed,
		NewMetric:        d.Detail.NewMetric,
		Type:             string(d.Detail.Type),
	})
	if err != nil {
		return entity.Discrepancy{}, fmt.Errorf("marshal discrepancy detail: %w", err)
	}
	qrs := make([]quoteRow, 0, len(d.Quotes))
	for _, q := range d.Quotes {
		qrs = append(qrs, quoteRow{Quote: q.Quote, MessageLink: q.MessageLink})
	}
	quotes, err := json.Marshal(qrs)
	if err != nil {
		return entity.Discrepancy{}, fmt.Errorf("marshal discrepancy quotes: %w", err)
	}

	rows, err := r.db.Query(ctx, insertDiscrepancySQL,
		d.AccountID, d.UserID, d.ConversationID, string(d.Status),
		d.CompanyName, nullString(d.EmailAddress), nullString(d.EmailDeeplinkURL), d.OrderID,
		string(d.Priority), d.Title, detail, quotes)
	if err != nil {
		return entity.Discrepancy{}, fmt.Errorf("insert discrepancy: %w", err)
	}
	defer rows.Close()
	out, err := scanDiscrepancies(rows)
	if err != nil {
		return entity.Discrepancy{}, err
	}
	if len(out) == 0 {
		return entity.Discrepancy{}, fmt.Errorf("insert discrepancy: no row returned")
	}
	return out[0], nil
}

func (r *Discrepancies) ListByUser(ctx context.Context, userID uuid.UUID, status *entity.DiscrepancyStatus) ([]entity.Discrepancy, error) {
	var statusStr *string
	if status != nil {
		s := string(*status)
		statusStr = &s
	}
	rows, err := r.db.Query(ctx, listDiscrepanciesSQL, userID, statusStr)
	if err != nil {
		return nil, fmt.Errorf("list discrepancies: %w", err)
	}
	defer rows.Close()
	return scanDiscrepancies(rows)
}

// UpdateStatus sets the status of the user's row; found=false when the id
// doesn't exist or belongs to another user.
func (r *Discrepancies) UpdateStatus(ctx context.Context, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, bool, error) {
	rows, err := r.db.Query(ctx, updateDiscrepancyStatusSQL, userID, id, string(s))
	if err != nil {
		return entity.Discrepancy{}, false, fmt.Errorf("update discrepancy status: %w", err)
	}
	defer rows.Close()
	out, err := scanDiscrepancies(rows)
	if err != nil {
		return entity.Discrepancy{}, false, err
	}
	if len(out) == 0 {
		return entity.Discrepancy{}, false, nil
	}
	return out[0], true, nil
}

func scanDiscrepancies(rows pgx.Rows) ([]entity.Discrepancy, error) {
	var out []entity.Discrepancy
	for rows.Next() {
		var (
			d                              entity.Discrepancy
			emailAddress, emailDeeplinkURL *string
			orderID                        *int
			status, priority               string
			detailJSON, quotesJSON         []byte
		)
		if err := rows.Scan(
			&d.ID, &d.AccountID, &d.UserID, &d.ConversationID, &status,
			&d.CompanyName, &emailAddress, &emailDeeplinkURL, &orderID,
			&priority, &d.Title, &detailJSON, &quotesJSON,
			&d.CreatedAt, &d.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan discrepancy: %w", err)
		}
		d.Status = entity.DiscrepancyStatus(status)
		d.Priority = entity.Priority(priority)
		d.EmailAddress = deref(emailAddress)
		d.EmailDeeplinkURL = deref(emailDeeplinkURL)
		if orderID != nil {
			d.OrderID = *orderID
		}
		if len(detailJSON) > 0 {
			var dr detailRow
			if err := json.Unmarshal(detailJSON, &dr); err != nil {
				return nil, fmt.Errorf("unmarshal discrepancy detail: %w", err)
			}
			d.Detail = entity.DiscrepancyDetail{
				PreviouslyAgreed: dr.PreviouslyAgreed,
				NewMetric:        dr.NewMetric,
				Type:             entity.MetricType(dr.Type),
			}
		}
		if len(quotesJSON) > 0 {
			var qs []quoteRow
			if err := json.Unmarshal(quotesJSON, &qs); err != nil {
				return nil, fmt.Errorf("unmarshal discrepancy quotes: %w", err)
			}
			for _, q := range qs {
				d.Quotes = append(d.Quotes, entity.DiscrepancyQuote{Quote: q.Quote, MessageLink: q.MessageLink})
			}
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate discrepancies: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 2: Verify it compiles cleanly**

Run: `cd overseer && go build ./... && go vet ./...`
Expected: no output

- [ ] **Step 3: Commit**

```bash
git add overseer/internal/repo/discrepancies.go
git commit -m "feat: discrepancies repo — insert, list by user, update status"
```

---

### Task 9: `ListDiscrepancies` + `UpdateDiscrepancyStatus` usecases

**Files:**
- Create: `overseer/internal/usecase/orders/list_discrepancies.go`
- Create: `overseer/internal/usecase/orders/list_discrepancies_test.go`
- Create: `overseer/internal/usecase/orders/update_discrepancy_status.go`
- Create: `overseer/internal/usecase/orders/update_discrepancy_status_test.go`

**Interfaces:**
- Consumes: `DiscrepancyRepo` port (Task 5), `testutil.InMemDiscrepancies` (Task 4).
- Produces (used by Task 10's controller):
  - `orders.ListDiscrepancies{Repo DiscrepancyRepo}` + `Execute(ctx, in ListDiscrepanciesInput) ([]entity.Discrepancy, error)` with `ListDiscrepanciesInput{UserID uuid.UUID; Status *entity.DiscrepancyStatus}`
  - `orders.UpdateDiscrepancyStatus{Repo DiscrepancyRepo}` + `Execute(ctx, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, error)`
  - `orders.ErrDiscrepancyNotFound` sentinel (controller maps to 404)

- [ ] **Step 1: Write the failing tests**

```go
// overseer/internal/usecase/orders/list_discrepancies_test.go
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
```

```go
// overseer/internal/usecase/orders/update_discrepancy_status_test.go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd overseer && go test ./internal/usecase/orders/`
Expected: FAIL — `undefined: ListDiscrepancies`, `undefined: UpdateDiscrepancyStatus`

- [ ] **Step 3: Implement**

```go
// overseer/internal/usecase/orders/list_discrepancies.go
package orders

import (
	"context"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// ListDiscrepancies serves GET /discrepancies: discrepancy.found only
// reaches clients connected at the moment of the finding, so the DB is the
// source of truth for everyone who connects later.
type ListDiscrepancies struct {
	Repo DiscrepancyRepo
}

type ListDiscrepanciesInput struct {
	UserID uuid.UUID
	Status *entity.DiscrepancyStatus // nil = all
}

func (uc *ListDiscrepancies) Execute(ctx context.Context, in ListDiscrepanciesInput) ([]entity.Discrepancy, error) {
	return uc.Repo.ListByUser(ctx, in.UserID, in.Status)
}
```

```go
// overseer/internal/usecase/orders/update_discrepancy_status.go
package orders

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

// ErrDiscrepancyNotFound reports an id that doesn't exist or belongs to
// another user — the controller maps it to 404.
var ErrDiscrepancyNotFound = errors.New("discrepancy not found")

// UpdateDiscrepancyStatus serves PATCH /discrepancies/{discrepancyId}: the
// UI clears findings by resolving or dismissing them (reopening is allowed).
type UpdateDiscrepancyStatus struct {
	Repo DiscrepancyRepo
}

func (uc *UpdateDiscrepancyStatus) Execute(ctx context.Context, userID, id uuid.UUID, s entity.DiscrepancyStatus) (entity.Discrepancy, error) {
	if !entity.ValidDiscrepancyStatus(s) {
		return entity.Discrepancy{}, fmt.Errorf("invalid status %q", s)
	}
	d, found, err := uc.Repo.UpdateStatus(ctx, userID, id, s)
	if err != nil {
		return entity.Discrepancy{}, err
	}
	if !found {
		return entity.Discrepancy{}, ErrDiscrepancyNotFound
	}
	return d, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd overseer && go test ./internal/usecase/orders/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add overseer/internal/usecase/orders/
git commit -m "feat: list + update-status discrepancy usecases"
```

---

### Task 10: HTTP surface — `GET /discrepancies`, `PATCH /discrepancies/{discrepancyId}`, webhook-audit E2E

**Files:**
- Create: `overseer/internal/controller/http/discrepancies.go`
- Create: `overseer/internal/controller/http/discrepancies_e2e_test.go`
- Modify: `overseer/internal/controller/http/dto.go` (append DTOs)
- Modify: `overseer/internal/controller/http/router.go` (add deps field + routes)
- Modify: `overseer/internal/controller/http/orders_e2e_test.go` (extend `env`/`newEnv` with the discrepancies repo + controller)
- Modify: `overseer/internal/controller/http/webhooks_e2e_test.go` (wire `Audit` into `newWebhookEnv`, add one E2E)

**Interfaces:**
- Consumes: Task 9 usecases, Task 5 `CatchDiscrepancies`, Task 4 fakes, existing `authFrom`/`errorResponse`/`AuthMiddleware`.
- Produces: routes `GET /discrepancies` (auth) and `PATCH /discrepancies/:discrepancyId` (auth); `RouterDeps.Discrepancies *DiscrepanciesController`; `env.discs *testutil.InMemDiscrepancies`.

- [ ] **Step 1: Extend the E2E env.** In `orders_e2e_test.go`, add `discs *testutil.InMemDiscrepancies` to the `env` struct, and in `newEnv` create `discs := testutil.NewInMemDiscrepancies()`, pass it to the router deps, and include it in the returned struct:

```go
	discs := testutil.NewInMemDiscrepancies()
```

```go
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
```

```go
	return &env{srv: srv, user: user, graph: graph, llm: llm, repo: repo, discs: discs, events: events}
```

- [ ] **Step 2: Write the failing E2E tests**

```go
// overseer/internal/controller/http/discrepancies_e2e_test.go
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
)

func (e *env) seedDiscrepancy(t *testing.T, status entity.DiscrepancyStatus, title string) entity.Discrepancy {
	t.Helper()
	d, err := e.discs.Insert(context.Background(), entity.Discrepancy{
		AccountID: e.user.AccountID, UserID: e.user.ID, ConversationID: "conv-1",
		Status: status, CompanyName: "Delmar Tooling", EmailAddress: "dana@delmar.com",
		EmailDeeplinkURL: "https://outlook.example/msg-9", OrderID: 4471,
		Priority: entity.PriorityHigh, Title: title,
		Detail: entity.DiscrepancyDetail{
			PreviouslyAgreed: "$41.00/unit", NewMetric: "$42.50/unit", Type: entity.MetricPrice,
		},
		Quotes: []entity.DiscrepancyQuote{{Quote: "we agree on $41.00/unit", MessageLink: "https://outlook.example/m-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *env) patchJSON(t *testing.T, path, body string, authed bool) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authed {
		req.Header.Set("Authorization", "Bearer graph-token-abc")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestGetDiscrepancies_E2E_Returns200CamelCase(t *testing.T) {
	e := newEnv(t)
	e.seedDiscrepancy(t, entity.DiscrepancyOpen, "Price raised $41.00 → $42.50/unit")

	resp, body := e.request(t, http.MethodGet, "/discrepancies", true)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	list, ok := body["discrepancies"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("body = %v", body)
	}
	first := list[0].(map[string]any)
	for _, key := range []string{"id", "companyName", "orderId", "status", "priority", "title", "detail", "quotes", "dateCreated", "conversationId", "emailDeeplinkUrl"} {
		if _, ok := first[key]; !ok {
			t.Errorf("missing camelCase key %q in %v", key, first)
		}
	}
	detail := first["detail"].(map[string]any)
	if detail["previouslyAgreed"] != "$41.00/unit" || detail["type"] != "price" {
		t.Errorf("detail = %v", detail)
	}
}

func TestGetDiscrepancies_E2E_StatusFilterAndAuth(t *testing.T) {
	e := newEnv(t)
	e.seedDiscrepancy(t, entity.DiscrepancyOpen, "open one")
	e.seedDiscrepancy(t, entity.DiscrepancyResolved, "resolved one")

	resp, body := e.request(t, http.MethodGet, "/discrepancies?status=open", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if list := body["discrepancies"].([]any); len(list) != 1 {
		t.Errorf("filtered rows = %d, want 1", len(list))
	}

	resp, _ = e.request(t, http.MethodGet, "/discrepancies?status=bogus", true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bogus status → %d, want 400", resp.StatusCode)
	}

	resp, _ = e.request(t, http.MethodGet, "/discrepancies", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed → %d, want 401", resp.StatusCode)
	}
}

func TestPatchDiscrepancy_E2E(t *testing.T) {
	e := newEnv(t)
	d := e.seedDiscrepancy(t, entity.DiscrepancyOpen, "Price raised")

	resp, body := e.patchJSON(t, "/discrepancies/"+d.ID.String(), `{"status":"resolved"}`, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", resp.StatusCode, body)
	}
	if body["status"] != "resolved" || body["id"] != d.ID.String() {
		t.Errorf("body = %v", body)
	}

	resp, _ = e.patchJSON(t, "/discrepancies/"+uuid.NewString(), `{"status":"resolved"}`, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id → %d, want 404", resp.StatusCode)
	}

	resp, _ = e.patchJSON(t, "/discrepancies/"+d.ID.String(), `{"status":"archived"}`, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid status → %d, want 400", resp.StatusCode)
	}

	resp, _ = e.patchJSON(t, "/discrepancies/not-a-uuid", `{"status":"resolved"}`, true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("malformed id → %d, want 404", resp.StatusCode)
	}
}
```

And the webhook-audit capstone — in `webhooks_e2e_test.go`, first wire audit into `newWebhookEnv`: initialize the fake maps `graph.ConversationsByID = map[string][]entity.EmailMessage{}` and `llm.Findings = map[string][]entity.DiscrepancyFinding{}` (set these literals in the existing constructor calls), add `discs := testutil.NewInMemDiscrepancies()` (reuse the `discs` created... note: `newWebhookEnv` builds its own env — give it the same `Discrepancies` controller deps as `newEnv` and set `handle.Audit`):

```go
	handle := &messages.HandleChangeMessage{
		Subs: subs, Tokens: tokens,
		Latest: &messages.GetLatestMessage{Graph: graph},
		LLM:    llm, Repo: repo, Events: events,
		Audit: &orders.CatchDiscrepancies{Graph: graph, LLM: llm, Repo: discs, Events: events},
	}
```

(and return the env with `discs: discs` via the embedded `*env`.) Then append the test:

```go
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
```

(`webhooks_e2e_test.go` already imports `orders`; add it to `newWebhookEnv`'s deps as shown.)

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd overseer && go test ./internal/controller/http/`
Expected: FAIL — `undefined: DiscrepanciesController`, `e.discs undefined`, `unknown field Audit`… (compile errors are the failing state here)

- [ ] **Step 4: Implement the DTOs** (append to `dto.go`)

```go
type discrepancyDetailDTO struct {
	PreviouslyAgreed string `json:"previouslyAgreed"`
	NewMetric        string `json:"newMetric"`
	Type             string `json:"type"`
}

type discrepancyQuoteDTO struct {
	Quote       string `json:"quote"`
	MessageLink string `json:"messageLink,omitempty"`
}

type discrepancyDTO struct {
	ID               string                `json:"id"`
	ConversationID   string                `json:"conversationId"`
	Status           string                `json:"status"`
	CompanyName      string                `json:"companyName"`
	EmailAddress     string                `json:"emailAddress,omitempty"`
	EmailDeeplinkURL string                `json:"emailDeeplinkUrl,omitempty"`
	OrderID          int                   `json:"orderId"`
	Priority         string                `json:"priority"`
	Title            string                `json:"title"`
	Detail           discrepancyDetailDTO  `json:"detail"`
	Quotes           []discrepancyQuoteDTO `json:"quotes"`
	DateCreated      string                `json:"dateCreated"`
}

type listDiscrepanciesResponse struct {
	Discrepancies []discrepancyDTO `json:"discrepancies"`
}

type patchDiscrepancyRequest struct {
	Status string `json:"status"`
}

func toDiscrepancyDTO(d entity.Discrepancy) discrepancyDTO {
	quotes := make([]discrepancyQuoteDTO, 0, len(d.Quotes))
	for _, q := range d.Quotes {
		quotes = append(quotes, discrepancyQuoteDTO{Quote: q.Quote, MessageLink: q.MessageLink})
	}
	return discrepancyDTO{
		ID:               d.ID.String(),
		ConversationID:   d.ConversationID,
		Status:           string(d.Status),
		CompanyName:      d.CompanyName,
		EmailAddress:     d.EmailAddress,
		EmailDeeplinkURL: d.EmailDeeplinkURL,
		OrderID:          d.OrderID,
		Priority:         string(d.Priority),
		Title:            d.Title,
		Detail: discrepancyDetailDTO{
			PreviouslyAgreed: d.Detail.PreviouslyAgreed,
			NewMetric:        d.Detail.NewMetric,
			Type:             string(d.Detail.Type),
		},
		Quotes:      quotes,
		DateCreated: d.CreatedAt.UTC().Format(time.RFC3339),
	}
}
```

- [ ] **Step 5: Implement the controller**

```go
// overseer/internal/controller/http/discrepancies.go
package http

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/usecase/orders"
)

// DiscrepanciesController is transport only: parse → call usecase → map to DTO.
type DiscrepanciesController struct {
	List   *orders.ListDiscrepancies
	Update *orders.UpdateDiscrepancyStatus

	Log interface {
		Error(msg string, args ...any)
	}
}

// GET /discrepancies?status= — the reload path: discrepancy.found reaches
// only connected clients, so the DB serves everyone else.
func (h *DiscrepanciesController) GetDiscrepancies(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}

	in := orders.ListDiscrepanciesInput{UserID: userID}
	if s := c.QueryParam("status"); s != "" {
		status := entity.DiscrepancyStatus(s)
		if !entity.ValidDiscrepancyStatus(status) {
			return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid status"})
		}
		in.Status = &status
	}

	out, err := h.List.Execute(c.Request().Context(), in)
	if err != nil {
		h.Log.Error("list discrepancies failed", "error", err)
		return c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to list discrepancies"})
	}

	dtos := make([]discrepancyDTO, 0, len(out))
	for _, d := range out {
		dtos = append(dtos, toDiscrepancyDTO(d))
	}
	return c.JSON(http.StatusOK, listDiscrepanciesResponse{Discrepancies: dtos})
}

// PATCH /discrepancies/{discrepancyId} — resolve / dismiss / reopen a finding.
func (h *DiscrepanciesController) PatchDiscrepancy(c echo.Context) error {
	auth := authFrom(c)
	userID, err := uuid.Parse(auth.UserID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, errorResponse{Error: "invalid auth context"})
	}
	id, err := uuid.Parse(c.Param("discrepancyId"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errorResponse{Error: "discrepancy not found"})
	}

	var req patchDiscrepancyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid request body"})
	}
	status := entity.DiscrepancyStatus(req.Status)
	if !entity.ValidDiscrepancyStatus(status) {
		return c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid status"})
	}

	d, err := h.Update.Execute(c.Request().Context(), userID, id, status)
	if err != nil {
		if errors.Is(err, orders.ErrDiscrepancyNotFound) {
			return c.JSON(http.StatusNotFound, errorResponse{Error: "discrepancy not found"})
		}
		h.Log.Error("update discrepancy failed", "error", err)
		return c.JSON(http.StatusInternalServerError, errorResponse{Error: "failed to update discrepancy"})
	}
	return c.JSON(http.StatusOK, toDiscrepancyDTO(d))
}
```

- [ ] **Step 6: Add the routes.** In `router.go`, add to `RouterDeps` (after `Subscriptions`):

```go
	Discrepancies *DiscrepanciesController
```

and after the `/ws` route registration:

```go
	if d.Discrepancies != nil {
		e.GET("/discrepancies", d.Discrepancies.GetDiscrepancies, auth)
		e.PATCH("/discrepancies/:discrepancyId", d.Discrepancies.PatchDiscrepancy, auth)
	}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd overseer && go test ./internal/controller/http/`
Expected: PASS — new E2E files plus all pre-existing E2E tests

- [ ] **Step 8: Commit**

```bash
git add overseer/internal/controller/http/
git commit -m "feat: GET/PATCH discrepancies endpoints and webhook-audit E2E"
```

---

### Task 11: Wire it in `main.go` + full verification

**Files:**
- Modify: `overseer/cmd/overseer/main.go`

**Interfaces:**
- Consumes: everything above.
- Produces: a running binary where the webhook job audits threads and the REST surface serves findings.

- [ ] **Step 1: Add compile-time conformance asserts** to the existing `var (...)` block:

```go
	_ orders.ThreadFetcher   = (*graph.Client)(nil)
	_ orders.DiscrepancyLLM  = (*llm.Gateway)(nil)
	_ orders.DiscrepancyRepo = (*repo.Discrepancies)(nil)

	_ messages.DiscrepancyCatcher = (*orders.CatchDiscrepancies)(nil)
```

- [ ] **Step 2: Wire the adapters and usecases.** In `main()`:

After `subsRepo := repo.NewSubscriptions(pool)` add:

```go
	discRepo := repo.NewDiscrepancies(pool)
```

Change the `handleChange` literal to include the audit leg:

```go
	handleChange := &messages.HandleChangeMessage{
		Subs:   subsRepo,
		Tokens: tokens,
		Latest: &messages.GetLatestMessage{Graph: graphClient},
		LLM:    llmGateway,
		Repo:   orderRepo,
		Events: hub,
		Audit: &orders.CatchDiscrepancies{
			Graph:  graphClient,
			LLM:    llmGateway,
			Repo:   discRepo,
			Events: hub,
		},
	}
```

Add the controller to the `RouterDeps` literal (after `Subscriptions:`):

```go
		Discrepancies: &httpctl.DiscrepanciesController{
			List:   &orders.ListDiscrepancies{Repo: discRepo},
			Update: &orders.UpdateDiscrepancyStatus{Repo: discRepo},
			Log:    log,
		},
```

- [ ] **Step 3: Full verification**

Run: `cd overseer && go build ./... && go vet ./... && go test ./...`
Expected: everything passes. This is the whole-feature gate: entity, gateways, usecases, worker leg, hub, controllers, E2E.

- [ ] **Step 4: Commit**

```bash
git add overseer/cmd/overseer/main.go
git commit -m "feat: wire thread-audit pipeline and discrepancy endpoints"
```

---

## Out of scope (deliberate)

- **Live-mailbox verification** of the audit leg needs a fresh MSFT Graph token (same blocker as the Phase 2 live test) — `make db-up && make run`, `POST /sync`, then a simulated notification. Not part of this plan's test gate.
- **Checkups `CancelIfReply`** stays an extension point in `HandleChangeMessage` (spec defers it).
- **A live LLM test** for `FindDiscrepancies` (mirroring `filter_live_test.go`) can be added later; the validation core is unit-tested.

## Success Criteria

1. `go test ./...` green from `overseer/` with no Docker/network/credentials.
2. A Graph notification for a known order conversation with contradicting history produces: one `discrepancies` row (owner-stamped, `open`, deeplink to the new message) and one `discrepancy.found` event — and still upserts the order + publishes `order.updated`.
3. A clean thread, a first message, or a non-order conversation produces no discrepancy row and no event.
4. `GET /discrepancies` and `PATCH /discrepancies/{discrepancyId}` behave per the E2E table (camelCase, owner-filtered, 400/404 handling).



