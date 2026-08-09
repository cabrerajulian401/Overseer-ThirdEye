# **ThirdEye Dashboard 2 — Tech Spec**

PRD / Source of truth: `docs/Assets/ThirdEye Dashboard (2).html` (clickable prototype — behavioural source of truth) + `docs/Assets/ThirdEye Build Spec.html` (wireframe flows, Aug 2026)

Diff inventory: `docs/dashboard_difference.md` (field-level extraction of the prototype)

Predecessor spec: `docs/tech_spec.md` (Scout — the add-in-era backend this document evolves)

Architecture map: `.ai/ARCHITECTURE.md` · `.ai/go-code-organization.md`

---

**How to read this spec.** Every endpoint, usecase, gateway method, and table carries one of four tags so you can see the delta at a glance:

- **[EXISTS]** — implemented today in `overseer/`, unchanged.
- **[CHANGE]** — implemented today, but its contract, fields, or behavior must change.
- **[NEW]** — does not exist in the backend today.
- **[DROP]** — exists today, removed or replaced by this spec.

Decisions already made for this spec: **breaking changes are allowed** (Dashboard 2 replaces the Outlook add-in as the client; there is no production data, so migrations may rewrite vocabularies instead of dual-running them), and **billing/subscription is out of scope** (see Non-goals).

# **Problem statement**

## **Stakeholders**

- *Me myself & I*

## **Summary**

Scout shipped as an Outlook add-in backend: a synced order list, user-initiated Otto scans, cadence-only checkups, and webhook-driven discrepancy detection. Dashboard 2 turns ThirdEye into a self-updating purchase-order board — a standalone web dashboard where ingestion fills the board without the user opening emails. Five modules sit on one ingestion pipeline:

| Module | One-liner |
|---|---|
| **Order Board** | Every open PO as a card: stage, ship date, value, issues. Fills itself from email. |
| **Needs Review** | Inbox emails that couldn't be matched to an order — assign / create order / dismiss. |
| **Otto** | Extraction review queue → writes approved rows to an Excel workbook on OneDrive. |
| **Checkups** | Scheduled vendor-chasing emails with triggers, send caps, and reply detection. |
| **Audit** | Discrepancy flags (price/qty/date/doc-mismatch) with an accept / dispute / dismiss resolution model. |

Supporting surfaces: **Contact List** (vendor identity), **Settings** (inbox + spreadsheet connections, column mapping), **Onboarding/Account** (profile, grants, tutorial).

The backend already has the skeleton for all of this — Graph ingestion, LLM extraction, a scheduler, a realtime channel, clean-architecture layering. What changes is the **domain model** (multi-line orders, vendors as first-class identity, an extraction queue instead of user-initiated scans, rule-based checkups instead of precomputed message rows, a full flag lifecycle) and the **pipeline** (PO-based matching with a needs-review fallback instead of conversation-keyed upserts).

## **Goals**

Broad

- Evolve the existing monolith in place — same layering, same repo, no rewrite
- Make ingestion the product: the board fills and updates itself; every surface reads derived state from one source of truth
- Keep every external dependency behind its existing gateway seam

Specific

- Remodel orders: multi-line items, requested-vs-confirmed, documents, timeline, stage inference with manual override
- Introduce vendors as the identity gate for Otto and Checkups
- Turn Otto into a pipeline-fed review queue with an issues engine and mapping-driven Excel writes
- Remodel checkups as recurring rules (3 trigger types, send cap, reply detection) instead of precomputed rows
- Give Audit its full status machine (accept / dispute / dismiss with recoverability) and exposure math
- Add Needs Review, Contact List, Connections/Mapping, and Onboarding state on top of the existing auth work

## **Non-goals**

- *Billing / subscription (Free Beta / Pro, Stripe) — the build spec omits it entirely and the prototype only has static markup for it. Deferred; the `Account` surface ships profile + tutorial + sign-out only.*
- *Multi-tenant, roles/permissions, mobile — build spec scope: single user, desktop-first (~1280px target, 1024px floor).*
- *Backward compatibility with the Loki add-in — Dashboard 2 replaces it; old enums and endpoints may break.*
- *The dashboard frontend itself — this repo is the Overseer backend; the dashboard is a new client consuming this API (same rule as the add-in: any client uses the same API + `/ws`).*
- *Microservices, unneeded complexity, optimizing past the first working solution.*

# Current State vs. Dashboard 2 (delta overview)

What the backend implements today (verified against `overseer/` on branch `Dashboard-migration`), against what each module needs:

| Area | Today | Dashboard 2 needs |
|---|---|---|
| Orders | Flat single-item row keyed `(user_id, conversation_id)`; status `in_transit/confirmed/delivered/issue`; `GET /orders`, `POST /sync` | **[CHANGE]** Multi-line `items[]`, `requested{}`/`confirmed{}`, `documents[]`, `timeline[]`, stage enum `ordered→delivered` + `stage_source`, `stated_total`, `is_new`, archive lifecycle, PO-based matching, order detail + edit endpoints, board metrics |
| Vendors | Nothing — `company_name` string on rows | **[NEW]** `vendors` + `vendor_emails` + unknown-sender workflow; identity gate for Otto/Checkups |
| Needs Review | Nothing | **[NEW]** Unmatched-email entity, assign/create/dismiss/reopen, recoverable dismissed list |
| Otto | User-initiated scan (`POST /otto/scans`) → LLM ranks destinations → LLM plans append → Excel write | **[CHANGE]** Pipeline-fed extraction queue, issues engine (3 blocking + 3 warning), per-extraction destination, per-workbook column mappings, deterministic mapped writes, insert-vs-update with write-time row re-check, sync error/retry, reject with undo + synced-row removal |
| Checkups | One row per precomputed message; cadence only; statuses `pending/paused/sending/sent/cancelled`; reply cancels via `cancel_if_reply` | **[CHANGE]** Rule entity with 3 trigger types, send window, multi-PO coverage, send cap (2) + `awaiting_manual_check`, reply detection resets counter, dispute pause/resume, per-checkup history, coverage gaps |
| Audit | `discrepancies` with `open/resolved/dismissed`, priority, `price/date/unit` detail; found on webhook path | **[CHANGE]** Full flag lifecycle `new/accepted/disputed/resolved/dismissed`, type vocabulary `price/quantity/date/document_mismatch/other`, accept fan-out (order + doc + timeline + Otto row), 3-step dispute with checkup pausing, impact/exposure math, vendor pattern, notes, recoverable dismissals |
| Auth & onboarding | `AUTH_MODE=mvp\|aad`; AAD JWT validation + OBO exchange; seeded single user; `POST /sync` (202) | **[CHANGE]** User keyed on `aad_oid` (returning-account check), profile fields, grant state (`Mail.ReadWrite`, `Files.ReadWrite`) gating sync, sync progress reporting, tutorial state |
| Settings/Connections | Nothing (sheet chosen per scan; no connection state) | **[NEW]** Inbox + sheet connection state, disconnect/reconnect with backfill, workbook registry + per-workbook column mapping wizard, create-tracking-sheet path |
| Realtime | `/ws` hub broadcasting `order.updated`, `discrepancy.found`, `sync.completed` | **[CHANGE]** Event set grows (extraction, needs-review, checkup, flag, sync progress, connection); envelope unchanged |
| Workers | 1-min checkup ticker + 1-h subscription-renewal ticker as `main.go` goroutines | **[CHANGE]** Checkup tick evaluates trigger rules; **[NEW]** daily auto-archive job; webhook job becomes the matching pipeline |

# Initial Questions:

Answers

- [All] The prototype, the build spec, and `dashboard_difference.md` disagree in places — which wins?

**Spec the intent, not the prototype's dead code.** The build spec states the rules; the prototype demonstrates the UI but stubs several behaviors. Where they diverge, this spec implements the stated rule: auto-archive Delivered+30d (prototype defines `archiveDaysLeft` but never calls it), real reply detection (prototype has seed data only), per-workbook column mappings (prototype has one global mapping), real trigger semantics for silent/preship (prototype computes all three identically), and send-window enforcement (prototype stores but ignores it). Prototype-only quirks this spec deliberately does **not** reproduce: the hardcoded 1.09 FX rate (rate comes from the document), the `assignEmail` bug that drops `target_order`, the multi-line compare bug (mixing `items[0]` fields with all-line totals), and PO-rename dangling references (children reference `orders.id`, so a PO rename can't strand flags/checkups/extractions).

- [Orders] Does matching stay conversation-keyed?

No — this is the deepest pipeline change. Today an order **is** a conversation (`UNIQUE (user_id, conversation_id)`, upsert per webhook). Dashboard 2 orders span threads: a vendor invoice may arrive in a fresh thread that still belongs to PO-2417. Matching becomes: extract PO number + vendor → match by `(user_id, order_number)` → else match by vendor + line-item similarity → else **Needs Review**. `conversation_id` stays as a non-unique column (the primary thread, used for deep links and reply detection), and the unique key moves to `(user_id, order_number)`.

- [All] How does 10-second undo work server-side?

It doesn't — the window is a client affordance. Server-side, every "destructive" action is a reversible status transition (`rejected`, `dismissed` are soft states) with an explicit reverse endpoint (`restore`, `reopen`). The one hard side effect — removing an already-synced sheet row on reject — only happens when the client explicitly confirms it, after the undo window has passed on the client.

- [Otto] Is the LLM still deciding sheet writes?

No. With explicit per-workbook column mappings, the mapped write is **deterministic**: mapping says which ThirdEye field lands in which column; the write-time row re-check finds the PO's existing row (update) or the next free row (insert). `PlanSheetAppend` and `RankDestinations` are retired. The LLM's job shrinks to what it's good at: extraction, stage inference, document parsing, discrepancy detection.

- [Auth] How does the account chooser know an identity "already has ThirdEye"?

Keyed on the AAD object id (`oid`), not email. The AAD validator already surfaces `oid` in claims; users gain an `aad_oid` column, and first authenticated call resolves returning-vs-new. (The chooser UI itself is Microsoft's; the backend's job is answering the question after token exchange and resuming abandoned onboarding.)

- [Connections] What is the "queued emails" count while the inbox is down?

Best-effort. If the Graph token is still valid but the subscription lapsed, a delta query since `disconnected_at` gives a real count. If the token is dead (revoked grant), no count is knowable until reconnect — the banner shows a generic state and the reconnect backfill reports what it processed.

# **Proposed Solution**

### Reminders when Designing:

- Naming: name for functionality, not the current integration or the current UI label — `extractions`, not `otto_queue`; `flags`, not `audit_cards`.
- Every surface below is served by the same REST + `/ws` API; the dashboard is just the first client. Nothing in this spec is dashboard-private.
- The layering rule from the predecessor spec is unchanged and non-negotiable: `controller / worker → usecase → gateway / repo`.

### API Conventions (applies to everything below)

- **[EXISTS]** REST, resource-oriented URLs; server mints all IDs; `userId`/`accountId` from auth context, never the body; `camelCase` JSON / `snake_case` DB / `PascalCase` Go.
- **[EXISTS]** One realtime channel: `GET /ws` pushes `{ "type": ..., "data": {...} }`. New features add event **types**, never new sockets. Full event inventory in Cross-Cutting Rules below.
- **[EXISTS]** Long work is async: `202 { id }` + completion over `/ws` (initial sync already works this way; sheet writes stay synchronous — they're one Graph call).
- **[NEW]** **Sub-action POSTs** follow the existing `/otto/scans/{id}/record` precedent: state-machine transitions that carry semantics beyond a field patch (`/accept`, `/reject`, `/restore`, `/dispute`) are `POST /resource/{id}/<action>`. Plain field edits stay `PATCH`.
- **[NEW]** **Soft-delete + restore everywhere**: nothing user-visible is hard-deleted. Dismissed flags, dismissed needs-review emails, rejected extractions are statuses with reverse transitions.
- **[NEW]** **Timeline append rule**: every mutation that changes an order lands exactly one `order_timeline` row (`kind ∈ email|stage|flag|checkup|edit`), written in the same transaction as the mutation. A multi-field Save = one `edit` entry, not one per field.
- **[NEW]** **One order, one truth**: an accepted change writes the order row, its items, the document status, the timeline entry, and the queued extraction row in **one DB transaction**, then publishes one event. The Excel write is the only step outside the transaction (it still requires its own confirmation in Otto).

# LLM Gateway (shared)

**[CHANGE]** `gateway/llm` keeps its contract style — task-shaped methods, one model call each, validation and retries at the boundary, cost logging — but the method set changes:

```go
type LLMGateway interface {
	// [CHANGE] was FilterOrderMessages([]msg) []conversationID — now per-message triage for the
	// matching pipeline AND the sync batch: is it order-related, what PO/vendor does it reference?
	TriageMessage(msg, knownVendors, knownOrderNumbers) TriageResult
	//   TriageResult{ IsOrderRelated bool, OrderNumber string|"", VendorName string|"",
	//                 Confidence high|medium|low }

	// [CHANGE] extraction grows: multi-line items, TEXT order numbers, the new stage enum,
	// stated totals, evidence provenance, per-extraction confidence
	ExtractOrderFields(thread) OrderExtraction
	//   OrderExtraction{ OrderNumber string, VendorName string, OrderDate *date,
	//     Items []{ItemDescription, Quantity *int, UnitPrice *decimal, ExpectedShipDate *date},
	//     Stage ordered|confirmed|in_production|shipped|delivered, StatedTotal *decimal,
	//     Currency string|"", Confidence high|medium|low,
	//     EvidenceSnippet string, EvidenceWhere string, ActivitySummary string }

	// [NEW] attachment → structured document (the documents[] compare view + OCR extractions)
	ParseDocument(filename, content) DocumentParse
	//   DocumentParse{ DocType invoice|packing_list|bol, Parsed{Quantity, UnitPrice, Total,
	//     ExpectedShipDate}, Currency string|"", Confidence, EvidenceSnippet, EvidenceWhere }

	// [CHANGE] new type vocabulary + numeric values for impact math + evidence source
	FindDiscrepancies(newMsg|doc, orderState) []FlagFinding
	//   FlagFinding{ Type price|quantity|date|document_mismatch|other,
	//     OriginalValue, NewValue string, OriginalNum, NewNum *decimal,
	//     EvidenceSource email_text|invoice|packing_list|bol, EvidenceSnippet string,
	//     Confidence high|medium|low }

	// [DROP] RankDestinations — destination is an explicit user choice + default workbook
	// [DROP] PlanSheetAppend — mapped writes are deterministic from workbook_mappings
	// [DROP] FilterOrderMessages — subsumed by TriageMessage (sync loops it over the batch)
}
```

- Discrepancy detection now compares against **order state** (items, requested/confirmed, documents), not just message history — the flag must know the original value it's contradicting.
- Dispute email bodies are **not** LLM work: composed deterministically from the flag (your figure, theirs, the difference, their words quoted), user-editable before send.
- Reply detection is **not** LLM work: a message from a vendor email on a conversation/vendor with an active checkup is a reply.

# Order Board & Order Detail

The board is the read model; ingestion is the write model. Sync seeds it once **[EXISTS]**, the webhook pipeline keeps it live **[CHANGE]**, and the user edits it through drafts **[NEW]**.

## Ingestion: First Phase (Sync)

### `POST controller/sync` — **[CHANGE]**

- Keeps the async contract: `202 { syncId }`, runs the backfill job. Changes:
    - Gated on onboarding: rejected with `409` until both grants are recorded (`grantMail`, `grantFiles`) — "Sync cannot start until both are granted."
    - Writes a `sync_runs` row and pushes **[NEW]** `sync.progress` events (`{ syncId, pct, statusLine }`) so the five-line progress UI is real; finishes with the existing `sync.completed`.
    - Seeds **vendors** (from sender addresses + LLM vendor names), **orders** (multi-line, via the matching pipeline below), **extractions** (one per order-bearing email, `pending_review`), and **unmatched_emails** — the board appears fully populated, which is the onboarding contract (no skeleton).

### `usecase/orders/BuildActiveOrderList` — **[CHANGE]**

- Same skeleton (ListMessages → triage → extract → upsert → publish), rebuilt on the matching pipeline: per thread, `TriageMessage` → `ExtractOrderFields` → `MatchAndApply` (below). Progress reporting hooks after each thread.

## Ingestion: Second Phase (Post-Sync) — the matching pipeline

### `usecase/messages/HandleChangeMessage` — **[CHANGE]** (the biggest single change in this spec)

Today: resolve subscription → cancel-on-reply → classify (new conversations only) → extract → upsert by conversation → publish → audit. Becomes:

1. Resolve subscription → Graph token → fetch message **[EXISTS]**.
2. **[NEW] Vendor identity gate**: sender → `vendor_emails` lookup. Unknown sender → upsert `unknown_senders` row (bump `message_count`); the message still flows to Needs Review, but **no extraction row is created and no checkup reply is counted** until the sender is attached to a vendor.
3. **[CHANGE] Reply detection** (replaces `cancel_if_reply`): if the sender belongs to a vendor with checkups, record the reply — `checkup_status = reply_received`, `sends_since_reply = 0`, cancel the pending send (clear `next_send_date`, reschedule from the reply), store `last_reply_date/text/link`, append a `checkup_events` row. Runs before the order gate, like today.
4. **[CHANGE] Match**: `TriageMessage` with the user's known vendors + open order numbers. Not order-related → done (`skipped_not_order`). Order-related:
    - PO number matches an order → apply to that order.
    - No PO but vendor + line-item similarity gives one confident candidate → apply.
    - Otherwise → **[NEW]** insert `unmatched_emails` (best-effort `parsed{}` from the extraction), publish `needs_review.created`.
5. **[CHANGE] Apply** (`usecase/orders/ApplyMessageToOrder`, one transaction): merge extraction into items/`confirmed{}` (fill-don't-overwrite rules survive from today's `mergeExtraction`), infer stage — **never downgrade, never override `stage_source = manual_override`** — set `last_activity_date/summary`, set `is_new = true`, append one `email` timeline entry, create an `extractions` row (`pending_review`, `target_order_id` set) so Otto sees it.
6. **[NEW] Attachments**: `ParseDocument` per attachment → `order_documents` row (`reconciliation_status = pending`; `mismatch` when parsed fields contradict the order); OCR extractions get `source_type = document_ocr`.
7. **[CHANGE] Audit**: `CatchDiscrepancies` now compares message + documents against **order state** and writes `flags` rows (new vocabulary, numeric values, evidence source).
8. Publish `order.updated` (or `order.created`), `extraction.created`, `flag.created` as applicable. Idempotency stays keyed on message id — Graph re-delivers.

## Reading the board

### `GET controller/orders` — **[CHANGE]**

- `?stage=&vendorId=&stat=stalled|overdue|issues&q=&sort=last_update|expected_ship|order_value|vendor&archived=&cursor=&limit=`
- `q` matches order_number + vendor + item descriptions. Default sort `last_activity_date` desc; **`is_new` rows pin above the sort** (server-side, so every client agrees).
- Response envelope gains the stat tiles so the board renders from one call:

```json
{
  "orders": [ {
    "id": "…", "orderNumber": "PO-2417", "vendorId": "…", "vendorName": "Delmar Tooling",
    "itemDescription": "Steel brackets",          // items[0] denormalized for the card
    "quantity": 500, "unitPrice": 2.40, "orderTotal": 1200.00,
    "orderDate": "2026-07-01", "expectedShipDate": "2026-08-12",
    "orderStage": "in_production", "stageSource": "auto_inferred",
    "lastActivityDate": "2026-07-30", "lastActivitySummary": "Vendor confirmed ship date",
    "sourceEmailLink": "…", "statedTotal": null,
    "isNew": true, "archived": false, "deliveredDate": null, "archiveDaysLeft": null,
    "contested": false,                            // derived: any flag with status=disputed
    "shipFigure": { "kind": "days_late", "value": 4 },   // server-computed card rule
    "atStake": 125.00,                             // Σ money impact of this order's new flags
    "openFlagCount": 1
  }, … ],
  "stats": {
    "activeOrderCount": 12, "totalOpenValue": 48200.00,
    "stalledOrderCount": 2, "overdueOrderCount": 3, "openFlagCountTotal": 5
  },
  "nextCursor": null
}
```

### `GET controller/orders/{orderId}` — **[NEW]**

- Full detail: summary + `items[]` + `requested{}`/`confirmed{}` + `documents[]` (with per-field parsed-vs-order compare) + open/closed `flags` + `timeline[]`.
- Fetching detail **clears `is_new`** (per-user read state; single-user MVP stores it on the row) — the one intentional read side effect, mirroring "opening the card clears it".

### `PATCH controller/orders/{orderId}` — **[NEW]**

- The draft-save: any subset of summary fields, `requested`/`confirmed` sides, and the full `items[]` array (add/remove/edit lines). One transaction, **one `edit` timeline entry** regardless of field count. `expectedShipDate` recomputes as the earliest item date; order total is always `Σ quantity × unit_price` (never stored).
- `orderStage` in the patch = manual override: stamps `stage_source = manual_override` + one `stage` timeline entry instead.
- `archived: true|false` = manual archive/unarchive (confirmation is the client's job).
- PO renames are safe: children reference `orders.id`; `order_number` is display + matching key only.

### `usecase/orders/ListOrders` / `GetOrderDetail` / `SaveOrderDraft` / `ApplyMessageToOrder` — **[CHANGE]/[NEW]**

- `ListOrders` **[CHANGE]** grows filters/sorts/stats; `GetOrderDetail`, `SaveOrderDraft`, `ApplyMessageToOrder` **[NEW]** as above.

### Board rules (backend-computed, one implementation)

- **Stalled**: `stage ≠ delivered`, not archived, `today − last_activity_date ≥ 21` (`STALL_DAYS = 21`).
- **Overdue**: `expected_ship_date < today`, stage ∉ {shipped, delivered}.
- **Ship figure priority**: delivered → `N days late` → `Nd silent` → `in transit` → `today` → `N days` → `TBD` (server sends `{kind, value}`; the client only formats).
- **Stat tiles**: `activeOrderCount` = non-archived ∧ stage ≠ delivered; `totalOpenValue` = Σ their totals; `openFlagCountTotal` = flags `new` **on active orders** (build-spec intent; the prototype counted globally — spec follows the doc).
- **[NEW] Auto-archive job**: daily worker tick — `stage = delivered ∧ delivered_date ≤ today − 30d → archived = true` + timeline entry. `archiveDaysLeft` derived in the list response for the countdown.

# Needs Review

**[NEW]** — the module has no backend today. Unmatched order-related emails land here from the pipeline; nothing is destroyed.

### `GET controller/needs-review` — **[NEW]**

- `?status=pending|dismissed` (dismissed = the recoverable list). Rows: sender, subject, received, deep link, best-effort `parsed{ vendorName, itemDescription, quantity, unitPrice }`.

### `POST controller/needs-review/{id}/assign` — **[NEW]**

- Body `{ "orderId": "…" }`. One transaction: `email` timeline entry on the target order (dated the email's `received_date` — but `last_activity_date` only moves **forward**), a `pending_review` extraction row **with `target_order_id` set** (fixing the prototype's dropped-target bug), row status → `assigned`. Publishes `extraction.created`.

### `POST controller/needs-review/{id}/create-order` — **[NEW]**

- Body: prefilled-from-email draft; vendor + item + quantity required (vendor by id or new-vendor name — creating one attaches the sender address). Order starts `ordered` / `stage_source = manual_override`, `requested{}` from the draft, `confirmed = null`; the email becomes the first timeline entry. Row status → `converted`.

### `POST controller/needs-review/{id}/dismiss` / `POST …/{id}/reopen` — **[NEW]**

- Status `pending ↔ dismissed`. Dismiss confirmation is client-side; reopen is the recovery path.

### `usecase/review/ListUnmatched` / `AssignToOrder` / `CreateOrderFromEmail` / `SetDismissed` — **[NEW]**

# Otto (Extraction Queue → Excel)

Today Otto is request-shaped: the user clicks "Ask Otto" on an email, `POST /otto/scans` extracts + ranks destinations, `…/record` writes. Dashboard 2 inverts it: **the pipeline feeds a persistent queue**; the user reviews, corrects, and approves; writes are deterministic from column mappings.

**[DROP]** `POST /otto/scans`, `POST /otto/scans/{scanId}/record`, `usecase/otto/ResolveDestinations`, `usecase/otto/RecordOrder`, the `otto_scans` table, `llm.RankDestinations`, `llm.PlanSheetAppend`. (The drive-search gateway survives — the destination picker uses it.)

### `GET controller/extractions` — **[NEW]**

- `?status=pending_review|accepted|corrected|rejected&syncStatus=…`. Each row carries its fields, confidence, evidence (`evidenceSnippet`, `evidenceWhere`), sync state (`erpSyncStatus`, `syncError`, `syncedAt`, `sheetRow`, `retries`), `targetOrderId` (null → will create a new order), destination override, and its **computed `issues[]`**:
    - **Blocking**: `sheet_disconnected` · `unknown_vendor` (sender not attached to a vendor) · `missing_field` (quantity or unit price null). Blocked rows cannot be accepted.
    - **Warning**: `foreign_currency` (non-USD; conversion uses the document's rate, not a constant) · `conflict` (another pending extraction targets the same order) · `duplicate` (a synced row already has this item + quantity).
- Issues are computed in `usecase/extractions/ComputeIssues` — one implementation the queue list, the badge count, and the accept gate all share.

### `PATCH controller/extractions/{id}` — **[NEW]**

- Correct fields (marks the row `corrected` on accept), set the per-extraction destination override `{ workbookId, tab }`, resolve the currency warning.

### `POST controller/extractions/{id}/accept` — **[NEW]**

- Body: `{ "destination": { "workbookId", "tab" }?, "splitMode": "row_per_item"|"single_row"?, "confirmedRow": { "mode": "insert"|"update", "row": 43 } }`.
- Preconditions: no blocking issues; the destination workbook+tab **has a mapping** (else `409 mapping_required` — the unmapped-workbook gate).
- Order of operations: **(1)** re-check the sheet — find the PO's row via the mapped `order_number` column (update) or the next free row (insert); if reality disagrees with `confirmedRow`, `409` back with the fresh row so the client re-confirms (row numbers are advisory). **(2)** apply to the board in one transaction (`ApplyExtractionToOrder`: update the matched order's items + `confirmed{}`, or create a new order at `ordered`/`auto_inferred`, vendor inferred from sender domain if needed; timeline entry; `is_new = true`). **(3)** write the mapped columns — never other columns, formatting, or rows. **(4)** update the row: `accepted|corrected`, `erp_sync_status = synced`, `synced_at`, `sheet_row`. Excel failure → `sync_failed` + provider error in `sync_error` (e.g. `423 Locked`), the board apply stands, retry picks it up.
- Response names what happened: `{ "row": 43, "mode": "insert", "orderId": "…", "orderCreated": false }`.

### `POST controller/extractions/{id}/reject` / `…/restore` / `…/retry` — **[NEW]**

- `reject`: `{ "removeSheetRow": bool }` — rejecting a **synced** row may delete its workbook row (`excel.DeleteTableRow`); status → `rejected` (soft; `restore` reverses it within/after the client's 10-s undo).
- `retry`: re-attempts the sheet write for `sync_failed`, increments `retries`, surfaces the provider error verbatim.

### `usecase/extractions/ListQueue` / `ComputeIssues` / `CorrectExtraction` / `AcceptAndWrite` / `ApplyExtractionToOrder` / `RejectExtraction` / `RetrySync` — **[NEW]**

### `gateway/excel` — **[CHANGE]**

- Keeps `GetWorkbookStructure`, `AppendTableRow`. Adds: `ReadRows(itemID, tab, range)` (write-time re-check + the 3-row mapping preview), `UpdateRow`, `DeleteTableRow`, `CreateWorkbook(name, headers)` (the "create a tracking sheet for me" path), `ListTabs`.

### `gateway/drive` — **[EXISTS]**

- `Search`/`Recent`/`GetItem` unchanged — now serving the destination picker and Settings file picker instead of LLM ranking.

# Checkups

Today: `POST /checkups` precomputes N message rows; the scheduler sends due rows; a reply cancels rows flagged `cancel_if_reply`. Dashboard 2 checkups are **standing rules** — "a schedule of what will send, not an outbox of drafts."

**[CHANGE]** The `checkup_messages` table is replaced by `checkups` (the rule) + `checkup_orders` (PO coverage) + `checkup_events` (history). The scheduler's claim-due discipline (`send_date <= now`, `FOR UPDATE SKIP LOCKED`, release-on-failure) carries over to the new evaluation.

### `POST controller/checkups` / `PATCH controller/checkups/{checkupId}` / `DELETE controller/checkups/{checkupId}` — **[CHANGE]**

```json
{
  "vendorId": "…",
  "messageTemplate": "Hi {{vendor}}, checking in on {{po}} …",
  "attachmentFilename": null,
  "trigger": { "type": "cadence|silent|preship", "value": 7 },
  "sendWindow": "weekdays|any_day",
  "orderIds": ["…", "…"]
}
```

- Create computes `next_send_date` from the trigger; edit recomputes it (the prototype forgot to — spec does). Delete is real deletion of the **rule** (history rows go with it); the confirmation is client-side. `PATCH { "status": "paused"|"active" }` pauses/resumes.

### `GET controller/checkups` — **[CHANGE]**

- The schedule: ordered by `next_send_date`, paused last, `awaiting_manual_check` pinned first ("STOPPED — NEEDS YOU"). Each row: rule fields, `nextSendDate`, `lastSentDate`, `lastReply{date,text,link}`, `sendsSinceReply`, `vendorSends` (max across the vendor — the 3-segment cap bar), `pausedByDispute`, `history[]`.
- Envelope carries **[NEW]** `coverageGaps`: open orders covered by no checkup, grouped by vendor — the "Not covered → Set up checkup" prompts (prefill: cadence/7/weekdays, the vendor's uncovered POs).

### `POST controller/checkups/{id}/send` / `…/skip` — **[NEW]**

- `send`: send now. Body `{ "overrideCap": true }` for "Send one more" past the cap (confirmation client-side); an override send increments `sends_since_reply` and logs a `sent` history entry noting the bypass.
- `skip`: advance `next_send_date` one interval without sending; undo = a second call with `{ "undo": true }` restoring the prior date.

### `usecase/checkups/EvaluateDueCheckups` — **[CHANGE]** (replaces `SendDueCheckups`)

Scheduler tick (1-min ticker survives). Per active rule, in order:

1. **Trigger due?** `cadence`: `next_send_date ≤ today`. `silent`: vendor's `last_activity_date ≤ today − N`. `preship`: any covered order has `expected_ship_date − N ≤ today` (not yet shipped).
2. **Send window**: `weekdays` skips Sat/Sun (send rolls to Monday).
3. **Send cap**: `sends_since_reply ≥ 2` → `checkup_status = awaiting_manual_check`, halt, publish `checkup.updated`. No automatic send ever exceeds the cap.
4. **Send**: one email per vendor covering its POs (multi-PO: one message, POs listed) via `gateway/graph.SendMail` — claim-then-send exactly like today's `ClaimDue`/`Release`. On success: `sends_since_reply++`, `last_sent_date`, next `next_send_date`, `checkup_events` row, `checkup` timeline entries on covered orders.

- **Reply detection** lives in `HandleChangeMessage` (pipeline step 3 above) — still the only place replies are seen.
- **Dispute linkage**: `sendDispute` pauses the vendor's checkups (`paused_by_dispute = true`, `next_send_date = null`); closing the dispute (any outcome) resumes them (`active`, reschedule). Owned by the Audit usecases below.

# **Audit**

Today: `discrepancies` rows (`open/resolved/dismissed`) found on the webhook path, listed and patched. Dashboard 2 gives flags a full lifecycle and makes **accept** a transactional fan-out.

**[CHANGE]** Table remodeled as `flags` (new status + type vocabularies, numeric values for impact, evidence provenance, notes). `GET /discrepancies`, `PATCH /discrepancies/{id}` are **[DROP]**, replaced below.

### `GET controller/flags` — **[NEW]**

- `?tab=all|new|disputed|accepted|closed&groupByOrder=true`. `closed` = resolved + dismissed. Dismissed rows appear only under `closed`/the recoverable list.
- Envelope: `flags[]` (each with computed `impact: { kind: "money"|"days", value }`), `totalExposure` (Σ money impact over **new** flags only), per-order groups whose subtotals reconcile with the header by construction (one implementation computes both), `vendorPatterns[]` ("≥2 same-type flags for a vendor this quarter" — counting **other** flags, so the threshold means 2 distinct flags).
- Impact math (`usecase/flags/ComputeImpact`): price → `|Δ| × order qty`; quantity → `|Δ| × (order total ÷ order qty)`; document_mismatch/other → `|Δ|`; date → day delta, no dollar value, sorts last in exposure lists.

### `POST controller/flags/{flagId}/accept` — **[NEW]**

- One transaction: apply `new_value` to the order by type (price → unit price; quantity → quantity; date → item ship date; document_mismatch → back out unit price from the stated total), update `confirmed{}`, mark matching documents (`doc_type = evidence_source`) `reconciled`, clear `stated_total`, `flag` timeline entry, `is_new = true`, queue a `pending_review` extraction row targeting the order (Excel still requires its own confirmation in Otto), `flag_status = accepted`. Publish `order.updated` + `flag.updated` + `extraction.created`.

### `POST controller/flags/{flagId}/dispute` / `…/dispute/close` — **[NEW]**

- `GET controller/flags/{flagId}/dispute-draft` returns the composed email (your figure, theirs, the difference, the quote) — deterministic, fully editable.
- `dispute` body `{ "to", "subject", "body" }`: sends via `gateway/graph.SendMail`, `flag_status = disputed`, `disputed_since = today`, order shows **contested** (derived), original value kept, nothing written to any sheet, vendor's checkups paused (`paused_by_dispute`). Timeline entry.
- `dispute/close` body `{ "outcome": "backed_down"|"held_firm"|"withdraw" }`: `backed_down` → `resolved`, order unchanged; `held_firm` → runs the accept fan-out; `withdraw` → back to `new`. All three resume the vendor's checkups.

### `POST controller/flags/{flagId}/dismiss` / `…/restore` — **[NEW]**

- `dismissed` is soft and recoverable (`restore` → `new`); accepted/resolved are reopenable the same way.

### `PATCH controller/flags/{flagId}` — **[NEW]**

- `{ "note": "…" }` — the user note on a flag (the prototype displays notes but never saves them; spec does).

### `usecase/flags/ListFlags` / `ComputeImpact` / `AcceptFlag` / `SendDispute` / `CloseDispute` / `SetFlagStatus` / `SaveNote` — **[NEW]** · `usecase/orders/CatchDiscrepancies` — **[CHANGE]** (writes `flags` with the new shape, compares against order state)

# Contact List (Vendors & Unknown Senders)

**[NEW]** — vendors are the identity spine: Otto's extraction gate and Checkups' reply attribution both key on `vendor_emails`.

### `GET controller/vendors` — **[NEW]**

- Rows with derived counts: `activeOrders`, `openFlagCount`, `checkupStatus`, `lastActivityDate`. Plus `unknownSenders[]` (`email`, `messageCount`) — addresses seen by ingestion that belong to no vendor.

### `POST controller/vendors` / `POST controller/vendors/{vendorId}/emails` — **[NEW]**

- Create vendor (name, contact, first email); attach additional addresses.

### `POST controller/unknown-senders/{id}/attach` / `…/ignore` — **[NEW]**

- `attach` body `{ "vendorId" }` or `{ "newVendorName" }` — attaching **unblocks** any extractions held on `unknown_vendor` and future replies count for checkups. `ignore` silences the prompt (recoverable).

### `usecase/vendors/ListVendors` / `CreateVendor` / `AttachEmail` / `ResolveSender` — **[NEW]**

- `ResolveSender(email) → vendor|unknown` is the one lookup the pipeline, Otto issues engine, and checkup reply detection all share.

# Onboarding & Account

Auth transport **[EXISTS]**: `AUTH_MODE=aad` already validates AAD JWTs (JWKS, `oid`/`tid`/`scp` checks), performs OBO exchange for Graph tokens, and handles `interaction_required` re-auth (`403`). What's missing is the **product state around it** — today every request resolves to one seeded user.

### `GET controller/me` — **[NEW]**

- The session bootstrap: `{ profile: { firstName, lastName, businessName, industry, phoneNumber }, returning, onboarding: { profileDone, grantMail, grantFiles, syncDone }, tutorial: { step, done } }`. First authenticated call upserts the user by `aad_oid` — this is the "already has ThirdEye" answer and the abandoned-onboarding resume point. Returning users: profile prefilled, grants assumed, tutorial marked done.

### `PATCH controller/me` — **[NEW]**

- Profile fields (Step 2), `tutorial` state (advance / finish / **replay** resets), and grant recording (Step 3: the client reports consent outcomes; the backend verifies scopes off the token where possible). Both grants required before `POST /sync` unlocks.

### `usecase/account/GetMe` / `UpdateProfile` / `RecordGrants` / `SetTutorial` — **[NEW]** · `repo/users` — **[CHANGE]** (upsert-by-oid, profile/grant/tutorial columns)

- Subscription/billing: **out of scope** (Non-goals). The Account screen's security section is display-only (Microsoft's domain).

# Settings / Connections

**[NEW]** — connection state, the workbook registry, and column mapping. This is what makes Otto's "sheet disconnected" blocking issue and the unmapped-workbook gate real.

### `GET controller/connections` — **[NEW]**

- `{ inbox: { status, accountEmail, queuedEmails }, sheet: { status, workbookId, filename, tab } }`. `queuedEmails` is best-effort (see Initial Questions).

### `DELETE controller/connections/{inbox|sheet}` / `POST controller/connections/inbox` — **[NEW]**

- Disconnect (client confirms) → banners; **inbox down**: webhook processing pauses (subscription dropped); **sheet down**: Otto accepts are blocked (`sheet_disconnected` issue). Reconnect inbox → re-subscribe + **backfill job** processes mail since `disconnected_at` through the normal pipeline, then reports the count.

### `POST controller/connections/sheet` — **[NEW]**

- Body `{ "driveItemId" }` (picked from `GET controller/workbooks/recent` below) **or** `{ "create": true }` — `excel.CreateWorkbook` with ThirdEye's own headers, mapping auto-written (the skip-mapping path). Sets the default destination workbook.

### `GET controller/workbooks` / `GET controller/workbooks/recent` / `PUT controller/workbooks/{workbookId}/mapping` — **[NEW]**

- `GET /workbooks` — registry of known workbooks with tabs + `mapped` flag. `GET /workbooks/recent` — recent OneDrive workbooks via the drive gateway (the Settings file picker). Mapping payload: `{ "tab": "Sheet1", "mapping": { "order_number": "PO Number", "vendor_name": "Supplier", …, "order_stage": null } }` — 9 mappable fields, `null` = don't sync this field. `GET …/mapping-preview` reads the sheet's headers + 3 live rows (guess-prefill + preview). A workbook/tab without a mapping row rejects writes (`mapping_required`).

### `usecase/connections/GetStatus` / `SetConnection` / `BackfillInbox` · `usecase/workbooks/ListWorkbooks` / `SaveMapping` / `PreviewMapping` — **[NEW]**

# Cross-Cutting Rules

- **One order, one truth** — the accept/apply fan-outs (Audit accept, Otto accept, order save, pipeline apply) are single DB transactions over `orders` + `order_items` + `order_documents` + `order_timeline` + `extractions`, each followed by one `/ws` event per changed entity. No screen can read a figure another screen hasn't seen.
- **Confirmations** (client-side, the API just makes them possible): writing to the spreadsheet, rejecting a synced row, sending a dispute, archiving, deleting a checkup, overriding the send cap, disconnecting an integration, dismissing an unmatched email.
- **Undo & recoverability** (server-side soft states + reverse endpoints): reject extraction ↔ restore, dismiss flag ↔ restore, dismiss needs-review ↔ reopen, skip send ↔ un-skip. Nothing user-visible is hard-deleted except a checkup rule.
- **Realtime event inventory** (envelope unchanged): **[EXISTS]** `order.updated`, `sync.completed` · **[CHANGE]** `discrepancy.found` → `flag.created` · **[NEW]** `order.created`, `extraction.created`, `extraction.updated`, `needs_review.created`, `needs_review.updated`, `checkup.updated`, `flag.updated`, `sync.progress`, `connection.updated`.
- **Workers** — the two `main.go` tickers survive; the checkup tick calls `EvaluateDueCheckups`; **[NEW]** a daily tick runs auto-archive. (Worth extracting to `internal/worker/` while touching this — the `.ai` docs already claim that layout.)

**Dependencies**

- **Microsoft Graph** — mail read/send, change notifications **[EXISTS]**; attachments (documents/OCR) **[NEW]**; OneDrive search **[EXISTS]**; Excel: append **[EXISTS]**, read/update/delete rows + create workbook **[NEW]**. Scopes: `Mail.ReadWrite`, `Files.ReadWrite` (recorded per user at onboarding).
- **AAD** — validation + OBO **[EXISTS]**; user-by-`oid` upsert **[NEW]**.
- **LLM provider** — behind `gateway/llm` as always; method set per the LLM Gateway section.
- **Build order that de-risks fastest**: ① schema + vendors + order remodel (everything reads it) → ② matching pipeline + Needs Review (the board fills itself — the product's core claim) → ③ Otto queue + mappings + writes → ④ Checkups remodel → ⑤ Audit lifecycle → ⑥ Onboarding/Connections polish. Each step keeps `go test ./...` green with the existing two-tier test strategy (usecase unit tests + controller E2E over fakes).

## **Schema Updates**

Conventions unchanged: Postgres, `snake_case`, every table carries `account_id` + `user_id`, every query filters on them from auth context. Breaking migrations are allowed (no production data); each remodel below is a new numbered dbmate migration in `overseer/db/migrations/`.

**[CHANGE]** `users` — add identity, profile, grants, tutorial:

```sql
ALTER TABLE users
    ADD COLUMN aad_oid        TEXT UNIQUE,              -- returning-account key (chooser answer)
    ADD COLUMN first_name     TEXT, ADD COLUMN last_name TEXT,
    ADD COLUMN business_name  TEXT, ADD COLUMN industry  TEXT, ADD COLUMN phone_number TEXT,
    ADD COLUMN grant_mail     BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN grant_files    BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN tutorial_step  INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN tutorial_done  BOOLEAN NOT NULL DEFAULT false;
-- graph_refresh_token_enc is dead in both auth modes today; drop it when convenient
```

**[NEW]** Vendors — the identity spine:

```sql
CREATE TABLE vendors (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id         UUID NOT NULL REFERENCES accounts(id),
    user_id            UUID NOT NULL REFERENCES users(id),
    vendor_name        TEXT NOT NULL,
    contact_name       TEXT,
    last_activity_date DATE,                             -- silent-trigger + contact list read
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_vendors_name ON vendors (user_id, lower(vendor_name));

CREATE TABLE vendor_emails (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id),
    vendor_id  UUID NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
    email      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_vendor_emails_lookup ON vendor_emails (user_id, lower(email)); -- ResolveSender

CREATE TABLE unknown_senders (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id    UUID NOT NULL REFERENCES accounts(id),
    user_id       UUID NOT NULL REFERENCES users(id),
    email         TEXT NOT NULL,
    message_count INTEGER NOT NULL DEFAULT 1,
    status        TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','attached','ignored')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, email)
);
```

**[CHANGE]** Orders — rebuilt (breaking migration replaces the old table):

```sql
CREATE TABLE orders (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id            UUID NOT NULL REFERENCES accounts(id),
    user_id               UUID NOT NULL REFERENCES users(id),
    vendor_id             UUID REFERENCES vendors(id),
    order_number          TEXT NOT NULL,                 -- 'PO-2417' — business/matching key (was INTEGER)
    conversation_id       TEXT,                          -- primary thread; NO LONGER the unique key; null for manual orders
    order_date            DATE,
    order_stage           TEXT NOT NULL DEFAULT 'ordered'
                          CHECK (order_stage IN ('ordered','confirmed','in_production','shipped','delivered')),
    stage_source          TEXT NOT NULL DEFAULT 'auto_inferred'
                          CHECK (stage_source IN ('auto_inferred','manual_override')),
    expected_ship_date    DATE,                          -- derived: earliest item date; denormalized for sort/filter
    last_activity_date    DATE,
    last_activity_summary TEXT,
    source_email_link     TEXT,
    stated_total          NUMERIC(12,2),                 -- vendor-claimed total vs computed; cleared on accept
    requested             JSONB,                         -- { quantity, unit_price, expected_ship_date }
    confirmed             JSONB,                         -- same shape; null until vendor confirms
    is_new                BOOLEAN NOT NULL DEFAULT false, -- per-user read state; cleared on detail fetch
    archived              BOOLEAN NOT NULL DEFAULT false,
    delivered_date        DATE,                          -- drives the 30-day auto-archive
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, order_number)                       -- the matching key
);
CREATE INDEX idx_orders_conversation ON orders (user_id, conversation_id); -- deep links + reply context (non-unique now)
CREATE INDEX idx_orders_board        ON orders (user_id, archived, order_stage);

CREATE TABLE order_items (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id           UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    position           INTEGER NOT NULL DEFAULT 0,
    item_description   TEXT NOT NULL,
    quantity           INTEGER,
    unit_price         NUMERIC(12,2),
    expected_ship_date DATE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);                                                       -- line_total & order total always derived, never stored

CREATE TABLE order_documents (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id              UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    doc_type              TEXT NOT NULL CHECK (doc_type IN ('invoice','packing_list','bol')),
    filename              TEXT NOT NULL,
    received              DATE NOT NULL,
    reconciliation_status TEXT NOT NULL DEFAULT 'pending'
                          CHECK (reconciliation_status IN ('pending','mismatch','reconciled')),
    parsed                JSONB,                         -- { quantity, unit_price, total, expected_ship_date }
    source_email_link     TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE order_timeline (                            -- append-only: no updated_at, no UPDATE path
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id   UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    entry_date DATE NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('email','stage','flag','checkup','edit')),
    text       TEXT NOT NULL,
    link       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_timeline_order ON order_timeline (order_id, entry_date DESC);
```

**[NEW]** Needs Review:

```sql
CREATE TABLE unmatched_emails (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id           UUID NOT NULL REFERENCES accounts(id),
    user_id              UUID NOT NULL REFERENCES users(id),
    message_id           TEXT NOT NULL,
    conversation_id      TEXT,
    source_email_sender  TEXT NOT NULL,
    source_email_subject TEXT,
    received_date        TIMESTAMPTZ NOT NULL,
    source_email_link    TEXT,
    parsed               JSONB,                          -- best-effort { vendor_name, item_description, quantity, unit_price }
    status               TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending','assigned','converted','dismissed')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, message_id)                         -- webhook idempotency
);
```

**[CHANGE]** Extractions — replaces `otto_scans`:

```sql
CREATE TABLE extractions (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id           UUID NOT NULL REFERENCES accounts(id),
    user_id              UUID NOT NULL REFERENCES users(id),
    message_id           TEXT,
    conversation_id      TEXT,
    source_email_sender  TEXT,
    source_email_subject TEXT,
    received_date        TIMESTAMPTZ,
    source_email_link    TEXT,
    source_type          TEXT NOT NULL DEFAULT 'email_text'
                         CHECK (source_type IN ('email_text','document_ocr')),
    item_description     TEXT,
    quantity             INTEGER,                        -- null → blocking 'missing_field' issue
    unit_price           NUMERIC(12,2),                  -- null → blocking 'missing_field' issue
    expected_ship_date   DATE,
    currency             TEXT,                           -- non-USD → 'foreign_currency' warning
    confidence_level     TEXT CHECK (confidence_level IN ('high','medium','low')),
    extraction_status    TEXT NOT NULL DEFAULT 'pending_review'
                         CHECK (extraction_status IN ('pending_review','accepted','corrected','rejected')),
    erp_sync_status      TEXT NOT NULL DEFAULT 'not_synced'
                         CHECK (erp_sync_status IN ('not_synced','retrying','synced','sync_failed')),
    sync_error           TEXT,                           -- provider error verbatim ('423 Locked …')
    synced_at            TIMESTAMPTZ,
    sheet_row            INTEGER,                        -- row actually written (advisory before write)
    retries              INTEGER NOT NULL DEFAULT 0,
    target_order_id      UUID REFERENCES orders(id),     -- null → accept creates a new order
    evidence_snippet     TEXT,
    evidence_where       TEXT,                           -- 'Email body, paragraph 2' / 'file.pdf · page 1, line 14'
    dest_workbook_id     UUID,                           -- per-extraction destination override (FK added below)
    dest_tab             TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_extractions_queue ON extractions (user_id, extraction_status);
```

**[NEW]** Workbooks, mappings, connections:

```sql
CREATE TABLE workbooks (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id    UUID NOT NULL REFERENCES accounts(id),
    user_id       UUID NOT NULL REFERENCES users(id),
    drive_item_id TEXT NOT NULL,
    filename      TEXT NOT NULL,
    web_url       TEXT,
    tabs          JSONB,                                 -- cached tab names
    is_default    BOOLEAN NOT NULL DEFAULT false,        -- the Settings-connected workbook
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, drive_item_id)
);
ALTER TABLE extractions ADD CONSTRAINT fk_extractions_workbook
    FOREIGN KEY (dest_workbook_id) REFERENCES workbooks(id);

CREATE TABLE workbook_mappings (                         -- absence of a row = the unmapped-workbook gate
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workbook_id UUID NOT NULL REFERENCES workbooks(id) ON DELETE CASCADE,
    tab         TEXT NOT NULL,
    mapping     JSONB NOT NULL,                          -- { order_number: 'PO Number' | null, … } null = don't sync
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workbook_id, tab)
);

CREATE TABLE connections (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES accounts(id),
    user_id         UUID NOT NULL REFERENCES users(id),
    kind            TEXT NOT NULL CHECK (kind IN ('inbox','sheet')),
    status          TEXT NOT NULL DEFAULT 'connected' CHECK (status IN ('connected','disconnected')),
    connected_email TEXT,                                -- inbox: the mailbox
    disconnected_at TIMESTAMPTZ,                         -- reconnect backfill starts here
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, kind)
);
```

**[CHANGE]** Checkups — rule + coverage + history replace `checkup_messages`:

```sql
CREATE TABLE checkups (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id          UUID NOT NULL REFERENCES accounts(id),
    user_id             UUID NOT NULL REFERENCES users(id),
    vendor_id           UUID NOT NULL REFERENCES vendors(id),
    message_template    TEXT NOT NULL,
    attachment_filename TEXT,
    trigger_type        TEXT NOT NULL CHECK (trigger_type IN ('cadence','silent','preship')),
    trigger_value       INTEGER NOT NULL,                -- the N for the trigger
    send_window         TEXT NOT NULL DEFAULT 'weekdays' CHECK (send_window IN ('weekdays','any_day')),
    checkup_status      TEXT NOT NULL DEFAULT 'active'
                        CHECK (checkup_status IN ('active','reply_received','paused','awaiting_manual_check')),
    paused_by_dispute   BOOLEAN NOT NULL DEFAULT false,  -- dispute pauses; close resumes
    next_send_date      DATE,                            -- null while paused/halted
    last_sent_date      DATE,
    last_reply_date     DATE,
    last_reply_text     TEXT,
    last_reply_link     TEXT,
    sends_since_reply   INTEGER NOT NULL DEFAULT 0,      -- cap = 2; vendor bar = MAX across the vendor's checkups
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_checkups_due ON checkups (next_send_date) WHERE checkup_status = 'active';

CREATE TABLE checkup_orders (                            -- multi-PO coverage; coverage gaps = open orders not here
    checkup_id UUID NOT NULL REFERENCES checkups(id) ON DELETE CASCADE,
    order_id   UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    PRIMARY KEY (checkup_id, order_id)
);

CREATE TABLE checkup_events (                            -- per-checkup history log
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    checkup_id UUID NOT NULL REFERENCES checkups(id) ON DELETE CASCADE,
    event_date TIMESTAMPTZ NOT NULL,
    dir        TEXT NOT NULL CHECK (dir IN ('sent','reply','note')),
    text       TEXT NOT NULL,
    body       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**[CHANGE]** Flags — replaces `discrepancies`:

```sql
CREATE TABLE flags (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id         UUID NOT NULL REFERENCES accounts(id),
    user_id            UUID NOT NULL REFERENCES users(id),
    order_id           UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    vendor_id          UUID REFERENCES vendors(id),
    discrepancy_type   TEXT NOT NULL
                       CHECK (discrepancy_type IN ('price','quantity','date','document_mismatch','other')),
    original_value     TEXT NOT NULL,                    -- display string: '$2.40'
    new_value          TEXT NOT NULL,                    -- display string: '$2.65'
    original_num       NUMERIC(12,2),                    -- for impact math; null for date flags
    new_num            NUMERIC(12,2),
    evidence_source    TEXT CHECK (evidence_source IN ('email_text','invoice','packing_list','bol')),
    evidence_snippet   TEXT,                             -- verbatim quote
    evidence_link      TEXT,
    flagged_date       DATE NOT NULL,
    flag_status        TEXT NOT NULL DEFAULT 'new'
                       CHECK (flag_status IN ('new','accepted','disputed','resolved','dismissed')),
    disputed_since     DATE,
    confidence_level   TEXT CHECK (confidence_level IN ('high','medium','low')),
    note               TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_flags_feed  ON flags (user_id, flag_status, flagged_date DESC);
CREATE INDEX idx_flags_order ON flags (order_id);
```

**[NEW]** Sync runs (progress reporting):

```sql
CREATE TABLE sync_runs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id          UUID NOT NULL REFERENCES accounts(id),
    user_id             UUID NOT NULL REFERENCES users(id),
    status              TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','failed')),
    progress_pct        INTEGER NOT NULL DEFAULT 0,
    status_line         TEXT,
    orders_created      INTEGER NOT NULL DEFAULT 0,
    extractions_created INTEGER NOT NULL DEFAULT 0,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at         TIMESTAMPTZ
);
```

**[EXISTS]** Unchanged: `accounts`, `graph_subscriptions` (+ its unique indexes). **[DROP]**: `otto_scans`, `checkup_messages`, `discrepancies`, old `orders` (each dropped by the migration that creates its replacement).
