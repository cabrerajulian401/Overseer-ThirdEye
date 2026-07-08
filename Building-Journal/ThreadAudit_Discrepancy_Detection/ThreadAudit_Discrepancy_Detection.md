# 🔍 Scout Thread Audit (Discrepancy Detection) — High-Level Overview

> **What this is** — the condensed walkthrough for the **Audit** feature from `tech_spec.md`: catching buyer/seller discrepancies (price, date, quantity) the moment a supplier's reply contradicts what the thread previously agreed. Spec calls it Phase 5; it was built early because it needs **no new infrastructure** — it rides the Phase 2 webhook pipeline. Companion docs: `SupplierList_Sync_Onboarding/`, `OrderList_PostSync_Webhooks/`.
>
> 📄 *Local-only (gitignored posture, same as the others) — never pushed.*
>
> 👀 **To read rendered:** open in VS Code, press <kbd>⇧⌘V</kbd>.

---

## ⚡ What was built

Phase 2 made the order list live; Audit makes it **vigilant**. Every new message in a known order conversation is now compared — by the LLM — against the thread's history. When the new message contradicts something previously agreed ("$41.00/unit" → "$42.50/unit"), a discrepancy row is stored and `discrepancy.found` is pushed over the existing `/ws` channel, with verbatim quotes deep-linking back to the evidence.

Two new endpoints + one new leg on the existing webhook job:

| Piece | What it does | Contract |
|-------|--------------|----------|
| audit leg on `HandleChangeMessage` | after the order upsert: re-fetch thread → `FindDiscrepancies` → store → publish | nil-able `Audit` field; failure logs, never undoes the order work |
| `GET /discrepancies` | list findings (`?status=open\|resolved\|dismissed`) | `200 {"discrepancies":[…]}` camelCase, owner-filtered |
| `PATCH /discrepancies/{discrepancyId}` | resolve / dismiss / reopen | `200 {…row}` · `400` bad status · `404` unknown/foreign id |

**The usecase at the center** — `usecase/orders/CatchDiscrepancies` (spec: `usecase/orders/CatchDiscrepancies`):
token + order row + new message → `gateway/graph.ListConversationMessages` → history (new message excluded) → `gateway/llm.FindDiscrepancies(newMsg, history)` → insert per finding (status `open`) → `discrepancy.found` per row.

---

## 🎓 The design decisions (one line each)

1. **Audit rides the webhook job** — `HandleChangeMessage` grew a nil-able `Audit DiscrepancyCatcher` field; no new worker, no new socket, no new entrypoint (exactly the spec's "pure add-on to the webhook job").
2. **The order leg is never hostage to the audit leg** — audit runs *after* upsert + `order.updated`; an audit failure returns an error for the caller's log but cannot un-upsert the order, and a Graph redelivery retries it.
3. **Thread history is re-fetched from Graph, never stored** — the spec's own gap-closure: no messages table; `ListConversationMessages` filters by `conversationId` and sorts oldest→newest **client-side** because Graph rejects `$orderby` combined with that `$filter` (InefficientFilter).
4. **The model quotes by index, the gateway resolves links** — findings carry `messageIndex` per quote; the gateway validates the index into the history it actually showed the model and maps it to the message's `webLink`. Enums (priority/type), titles, and indexes are all validated at the boundary with one retry — usecases only ever see clean structs.
5. **Conservative prompt, explanation-first** — same trick as the filter: the model must explain before it decides, only flags values the history actually *agreed on* (no first-time values, no restatements), and an **empty findings list is the correct output** for a clean thread.
6. **First message never audits** — no history → nothing to contradict → no LLM call (and the filter gate upstream means junk mail never reaches the audit at all).
7. **Findings are stamped from the order row** — account/user/company/PO come from the DB row, the deeplink from the new message; the LLM supplies *only* the finding (title, priority, detail, quotes). Ownership never originates from model output.
8. **`discrepancy.found` is an event type, not a socket** — new `case entity.Discrepancy` in the hub's DTO switch; the HTTP layer has its own identical DTO copy (each edge owns its DTOs).
9. **DB is the source of truth for late clients** — `/ws` only reaches whoever is connected at that moment; `GET /discrepancies` serves reloads, and the `open/resolved/dismissed` status (PATCH) lets the UI clear findings instead of showing them forever.
10. **Insert returns the stored row** — server-minted id + timestamps come back from `RETURNING`, so the published event carries the authoritative row (same re-read discipline as the order leg).
11. **Same ports pattern, zero import bending** — `messages` never imports `usecase/orders`; `DiscrepancyCatcher` is satisfied structurally by `*orders.CatchDiscrepancies`, pinned by a compile-time assert in `main.go` (4 new asserts total).
12. **Accepted deviation:** the audit is **not idempotent** — a redelivered notification re-runs it and can insert a duplicate finding. The order upsert stays idempotent; finding-dedup is the top fast-follow (see gaps).

---

## 🌳 Where things live (new/changed only)

```
overseer/
├── cmd/overseer/main.go                    +discRepo · Audit wired into handleChange · discrepancies controller · 4 asserts
├── db/migrations/20260707000004_…​.sql      discrepancies table (spec DDL) + idx_discrepancies_user (user_id, status)
└── internal/
    ├── entity/discrepancy.go               Discrepancy · DiscrepancyFinding · Detail/Quote · 3 validated enums
    ├── gateway/graph/                      +ListConversationMessages (filter-by-conversation, paginated, client-sorted)
    ├── gateway/llm/                        +FindDiscrepancies (prompt · schema · toFindings boundary validation)
    ├── repo/discrepancies.go               insert-returning · list-by-user(+status) · update-status · JSONB detail/quotes
    ├── usecase/orders/                     +CatchDiscrepancies · ListDiscrepancies · UpdateDiscrepancyStatus · 3 new ports
    ├── usecase/messages/                   HandleChangeMessage +Audit field · DiscrepancyCatcher port
    ├── controller/http/                    discrepancies.go (GET/PATCH) · DTOs · routes · E2E files
    ├── controller/ws/hub.go                +discrepancyDTO mapping for discrepancy.found
    └── testutil/fakes.go                   FakeGraph +ListConversationMessages · FakeLLM +FindDiscrepancies · +InMemDiscrepancies
```

---

## 🔄 How an audit flows

Supplier replies on a known order thread → Graph notification → `HandleChangeMessage` (order leg: fetch → extract → merge-upsert → `order.updated`) → **audit leg**: `ListConversationMessages(conversationId)` → drop the new message from history → history empty? stop : `FindDiscrepancies(newMsg, history)` → model returns findings (or none) → per finding: insert row stamped from the order (status `open`, deeplink = new message's webLink, quotes linked to their history messages) → `{"type":"discrepancy.found","data":{…discrepancyDTO}}` to every `/ws` client → UI raises the card; later, `PATCH /discrepancies/{id} {"status":"resolved"}` clears it.

> 💡 A clean reply ("thanks, sounds good") flows through the same path and produces *nothing*: no row, no event — silence is the happy path.

---

## ✅ State of the system

- **Proven (automated):** builds/vets clean · **72/72 test runs** across 10 packages in <2s, no network — finding stored+published with ownership stamped, clean thread stores nothing, first-message skip, new-message-excluded-from-history, LLM/Graph failure paths, audit-can't-undo-order, non-order skip, `toFindings` rejection table (bad enum/index/title), OData quote-escaping + pagination + client-side sort, ws DTO shape, GET filter/auth/400s, PATCH resolve/404/400, and the capstone E2E: one webhook POST → order row + `order.updated` + discrepancy row + `discrepancy.found`.
- **Not yet proven:** the live leg — real Graph thread fetch + real `FindDiscrepancies` judgment on a mocked supplier thread (same blocker as Phase 2: fresh Graph token; runbook = the testing steps from 2026-07-08, tunnel variant included).
- 📤 Pushed as branch `thread-audit` → **PR #3** (base `order_list`, merges after PR #2).

---

## 🚧 Known gaps (priority order)

1. **Findings don't dedupe** — redelivery or a supplier restating the change inserts a duplicate row. Fix: `source_message_id` column + unique index `(user_id, conversation_id, discrepancy_title)` + `ON CONFLICT DO NOTHING`, publish only on real insert.
2. **Token cache still in-memory** (inherited from Phase 2) — a restart silently breaks the audit until the next authed request; the refresh-token flow (`users.graph_refresh_token_enc`, still unused) is the real fix.
3. **LLM-side dedup** — pass the conversation's open findings into the prompt so re-worded repeats aren't re-reported (needs #1's key to be meaningful).
4. **`GET /discrepancies` has no pagination** — copy the orders cursor pattern; consider defaulting to `status=open`.
5. **No live-tagged LLM test** for `FindDiscrepancies` (mirror `filter_live_test.go`) — prompt quality (priority calibration, "no first-time values") is only provable live.
6. **Repo layer still untested** — `discrepancies.go` has the gnarliest SQL yet (JSONB both directions); the testcontainers upgrade path would cover it plus migrations.
7. **Misleading log on audit failure** — the webhook controller logs "handle change message failed" even when only the audit leg failed; split the log path.

---

## 🗺 What's next

- **Fast-follows:** #1 (finding dedup) and #2 (refresh tokens) close the correctness and reliability holes a real deployment hits first.
- **Live smoke test** of the audit leg (mock supplier thread → simulated or tunneled notification → eyeball the finding + priority).
- **Phase 3 — Otto:** drive/excel gateways + scan endpoints; the extract-and-validate LLM pattern here is the template.
- **Phase 4 — Check-ups:** scheduler + `send_due` query; wires `CancelIfReply` into the same `HandleChangeMessage` extension point the audit just used.

---

*Written 2026-07-08, covering the `thread-audit` branch (12 commits: plan `a773b7b` + 11 TDD commits `2c31bba…fafdced`), built from `docs/superpowers/plans/2026-07-07-thread-audit.md`. PR #3 → `order_list`.*
