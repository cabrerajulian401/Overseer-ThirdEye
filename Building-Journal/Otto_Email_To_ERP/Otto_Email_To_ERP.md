# 🤖 Scout Otto (Email → ERP Entry, Human-Approved) — High-Level Overview

> **What this is** — the condensed walkthrough for the **Otto** feature from `tech_spec.md`: "Ask Otto" turns an open supplier email into structured order fields plus the top-3 spreadsheet destinations, and — only after the operator edits and approves them on ONE confirm card — appends the row to the chosen Excel workbook. Companion docs: `SupplierList_Sync_Onboarding/`, `OrderList_PostSync_Webhooks/`, `ThreadAudit_Discrepancy_Detection/`.
>
> 📄 *Committed with the `otto-mode-impl` branch (PR #7) as the feature's guide.*
>
> 👀 **To read rendered:** open in VS Code, press <kbd>⇧⌘V</kbd>.

---

## ⚡ What was built

The order list watches email; Otto **acts** on it. One click fans out to Graph + LLM and comes back with a pre-filled confirm card; one approval writes the row to the ERP (Excel first, per the spec's MVP call) and hands back the sheet link. Nothing touches the ERP until a human approves — and what gets written is what the human *edited*, never the raw extraction.

Two new endpoints, two new gateways, two new LLM methods:

| Piece | What it does | Contract |
|-------|--------------|----------|
| `POST /otto/scans` | "Ask Otto" — extract + find + rank destinations | `200 {scanId, extractedFields, destinations[≤3]}`; upstream failure → `502`, **no scan row leaked** |
| `POST /otto/scans/{scanId}/record` | the human-in-the-loop approval | `200 {webUrl, status:"recorded"}` · `404` unknown scan · `409` already recorded · `400` missing resource/bad dates |
| `gateway/drive` | OneDrive search / recent / get-item | the destination-resolution read side |
| `gateway/excel` | workbook tables+columns read · table-row append | the write side — usecase writes, LLM only plans |

**The two usecases own the agent loops** (spec: the usecase is the agent loop; a gateway that calls gateways is an agent, not an adapter):
- `ResolveDestinations` — `ListConversationMessages` (reused from Audit) → `ExtractOrderFields` (reused from Phase 1) → drive searches (supplier · PO# · "orders" · "PO" · `/recent`, deduped by item id) → `RankDestinations` → store scan (`scanned`).
- `RecordOrder` — load scan → `GetWorkbookStructure` → `PlanSheetAppend(structure, approvedFields)` → `AppendTableRow` → `GetItem` for webUrl → mark `recorded`.

---

## 🎓 The design decisions (one line each)

1. **The confirm step is never theater** — `RecordOrder` plans from the request body's user-edited fields, provably not the stored extraction (the unit test seeds different values in each and asserts which one the plan-LLM saw).
2. **The LLM plans *where*, never *what*** — `PlanSheetAppend` sees table names + column headers and the approved fields; it maps values to columns and is forbidden (prompt + boundary validation) from inventing or re-deciding a value.
3. **Model output resolves by index, at the gateway** — same trick as Audit's quotes: `RankDestinations` shows the model `{index, name}` only and resolves indexes back to real drive items, so the output is structurally guaranteed to be a subset of the candidates (bad index → validation error → one retry).
4. **Failed scans leave no rows** — the scan row inserts *last*, after extract/search/rank all succeed, and *before* returning, so the record call can always find it and a 502 leaks nothing.
5. **Reuse over duplication** — no new extraction type or Graph call: the confirm card edits `entity.OrderExtraction`, threads come from `ListConversationMessages` (built for Audit), extraction from `ExtractOrderFields` (built for Phase 1). This is why `otto-mode` stacks on `thread-audit`.
6. **Named for functionality, not integration** — `ResolveDestinations`/`RecordOrder`, not `findExcelResources`; when QuickBooks/NetSuite arrive, the ports get new adapters and the usecases don't change.
7. **Two gateways, not one** — the searches are OneDrive (`/me/drive/...`), the append is the workbook API; splitting them means `drive` serves any future destination-resolution and `excel` is swappable per ERP.
8. **Cold-start search built in** — supplier name and PO number queries hit named files *and file content*; generic "orders"/"PO" plus `/me/drive/recent` cover mailboxes where nothing matches; all candidates dedupe before ranking.
9. **Double-approval is a 409, not a double row** — `recorded` scans refuse a second record; an append failure leaves the scan `scanned` so the user can retry.
10. **Synchronous 200 for now** — the spec's "202 + realtime event" escape hatch stays documented but unused until the sync call proves slow (one Graph fetch + 2 LLM calls + ~5 drive searches).
11. **Structure read, never data** — `GetWorkbookStructure` fetches tables and column names only; the LLM never sees existing spreadsheet rows.
12. **Same ports discipline** — seven small interfaces in `usecase/otto/ports.go`, satisfied structurally by `graph`/`drive`/`excel`/`llm`/`repo`; 7 new compile-time asserts in `main.go`.

---

## 🌳 Where things live (new/changed only)

```
overseer/
├── cmd/overseer/main.go                    +drive/excel clients · ottoRepo · Otto controller · 7 asserts
├── db/migrations/20260708000005_…​.sql      otto_scans table (spec DDL verbatim)
└── internal/
    ├── entity/ottoscan.go                  OttoScan · ScanStatus (scanned/recorded/abandoned)
    ├── entity/drive.go                     DriveItem · WorkbookTable/Structure · SheetAppendPlan
    ├── gateway/drive/                      Search (OData-quoted) · Recent · GetItem
    ├── gateway/excel/                      GetWorkbookStructure ($expand=columns) · AppendTableRow (rows/add)
    ├── gateway/llm/                        +RankDestinations · +PlanSheetAppend · shared extractionPayload
    ├── repo/ottoscans.go                   insert-returning · get-by-id (owner) · mark-recorded · fields JSONB
    ├── usecase/otto/                       ports.go · resolve_destinations.go · record_order.go (+tests)
    ├── controller/http/                    otto.go (2 endpoints) · confirm-card DTOs · routes · otto_e2e_test.go
    └── testutil/fakes.go                   +FakeDrive · +FakeExcel · FakeLLM rank/plan · +InMemOttoScans
```

---

## 🔄 How an Otto flow runs

**Scan:** user clicks "Ask Otto" → `POST /otto/scans {messageId, conversationId}` → fetch the whole thread from Graph → LLM extracts the order fields → 4 drive searches + recent, deduped → LLM ranks the top 3 plausible order logs → scan row stored (`scanned`) → `200 {scanId, extractedFields, destinations}` → FE renders **one confirm card** (editable fields + destination + change link). Nothing has been written.

**Record:** user edits fields, picks the sheet, approves → `POST /otto/scans/{scanId}/record {selectedResource, fields}` → workbook structure read (tables + columns, no data) → LLM plans the append (which table, which value per column — verbatim from the approved fields, empty string for unmapped columns) → gateway appends the row → scan marked `recorded` → `200 {webUrl}` → card flips to **"Recorded ✓ — open sheet"**.

> 💡 The `fields` in the record call are the user's edits, not a scanId-echo of the extraction — the whole point of the confirm card. The stored raw extraction stays in `otto_scans.extracted_fields` for comparison/audit later.

---

## ✅ State of the system

- **Proven (automated):** builds/vets clean · **105/105 test runs** across 12 packages in <2s, no network — loop order (extract→search→rank), candidate dedupe across searches, PO-query skip when no order id, zero-candidate rank skip, no-row-on-failure, approved-fields-not-stored-ones, append-failure leaves scan retryable, 404/409/400/401/502 table, OData single-quote doubling, workbook column expansion, rows/add body shape, rank index validation (range/dupe/cap-3), plan validation (unknown table, column-count mismatch), plus the full confirm-card E2E contract.
- **Not yet proven:** the live OneDrive/Excel leg — needs a Graph token with **`Files.ReadWrite`** (broader than the `Mail.Read` used for sync/audit testing); then: `POST /otto/scans` against a real supplier thread → eyeball the destinations → record into a scratch workbook → open the returned webUrl.
- 📤 Pushed as `otto-mode-impl` → **PR #7** (base `thread-audit`). Merge order: PR #3 → PR #7. (PR #2 is already merged; the pre-existing remote `otto-mode` branch is an untouched placeholder at main's tip.)

---

## 🚧 Known gaps (priority order)

1. **No supplier→resource memory** — every scan re-searches the drive; the spec's "mapping lookup; cold start → search → user picks → save" wants the user's pick persisted per supplier and tried first. Natural fast-follow, needs one small table.
2. **Token scope jumps** — Otto needs `Files.ReadWrite` on the Graph token; the token cache stores whatever the last request carried, so a mail-scoped token silently 403s on drive calls. Ties into the standing refresh-token gap.
3. **`abandoned` is never set** — scans that are never recorded sit as `scanned` forever; needs a UX decision (TTL sweep? explicit dismiss?).
4. **Rank quality is untested live** — a build-tagged live test (mirror `filter_live_test.go`) for `RankDestinations`/`PlanSheetAppend` would tune the prompts against a real drive before the demo.
5. **No pagination/dedup on repeated scans** — scanning the same email twice makes two scan rows (harmless, but a `GET /otto/scans` listing surface doesn't exist yet either).
6. **Repo layer still untested** — `ottoscans.go` joins `discrepancies.go` in the testcontainers backlog.

---

## 🗺 What's next

- **Live smoke test** with a `Files.ReadWrite` token against a scratch workbook (the runbook above).
- **Supplier→destination mapping table** (gap #1) — turns the cold-start search into a one-hit lookup for repeat suppliers.
- **Phase 4 — Check-ups:** the last unbuilt feature; adds the scheduler's `send_due` claim query and wires `CancelIfReply` into `HandleChangeMessage`'s marked extension point.
- **Cross-cutting:** refresh-token flow (every feature's background/live reliability now depends on it) and discrepancy dedup from the Audit gaps list.

---

*Written 2026-07-08, covering the local `otto-mode` branch (12 commits: plan `e8e9804` + 11 TDD commits `5e18972…f3a924f`), built from `docs/superpowers/plans/2026-07-08-otto-mode.md`. PR #7 (`otto-mode-impl` → `thread-audit`).*
