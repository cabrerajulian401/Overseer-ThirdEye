# ⏰ Scout Check-ups (Scheduled Follow-ups) — High-Level Overview

> **What this is** — the condensed walkthrough for the **Check-ups** feature from `tech_spec.md`: `POST /checkups` expands a follow-up cadence into one DB row per message, a 1-minute scheduler sends due rows through Graph, `PATCH /checkups/{checkupId}` pauses/resumes/cancels, and a supplier reply auto-cancels pending `cancel_if_reply` rows through the webhook job's long-marked extension point. Companion docs: `SupplierList_Sync_Onboarding/`, `OrderList_PostSync_Webhooks/`, `ThreadAudit_Discrepancy_Detection/`, `Otto_Email_To_ERP/`.
>
> 📄 *Committed with the `checkups-impl` branch as the feature's guide.*
>
> 👀 **To read rendered:** open in VS Code, press <kbd>⇧⌘V</kbd>.

---

## ⚡ What was built

The order list watches email and Otto acts on it; Check-ups make Scout **initiate**. One request schedules a whole nag-cadence ("chase this PO every 7 days, 3 times, stop if they reply"); a background ticker sends whatever's due; a supplier reply anywhere in the system silently cancels the rest of the flow. This is the last of the four spec features and the first with a write-side scheduler.

Three new endpoints, one new gateway method, one new worker loop:

| Piece | What it does | Contract |
|-------|--------------|----------|
| `POST /checkups` | create a flow — cadence expands to one row per **message** | `201 {flowId, checkups[]}` — every id server-minted; smuggled `id`/`userId` ignored · `400` missing fields/bad dates/repeat-without-interval |
| `GET /checkups` | the reload path — FE repopulates its list from the DB | `200 {checkups[]}` soonest `sendDate` first · `?status=` filter · `400` unknown status |
| `PATCH /checkups/{checkupId}` | pause / resume / cancel one row — one endpoint, no toggle verbs | `200` updated row · `404` unknown/foreign id · `409` sent/cancelled/mid-send · `400` client tries `sent`/`sending` |
| `gateway/graph.SendMail` | Graph `POST /me/sendMail`, Text body, save-to-sent | `202`-checked; any other status is an error |
| scheduler goroutine (`main.go`) | 1-min ticker → `SendDueCheckups.Execute` | logs failures and **keeps looping**; exits on ctx cancel |
| `HandleChangeMessage` hook | reply detection — the only place that sees replies | cancels pending `cancel_if_reply` rows on that conversation, idempotently |

**The usecases own all the logic** (the ticker just says "it's time, go"):
- `CreateCheckupMessages` — mint one `flowId` → compute each row's `sendDate` off the cadence (`repeatFlow` messages, `repeatEvery` apart) → transactional `InsertFlow` (all rows or none).
- `SendDueCheckups` — `ClaimDue` (`pending AND send_date <= now`, atomically flipped to `sending` under `FOR UPDATE SKIP LOCKED`) → per row: token from cache → `SendMail` → `MarkSent`; any failure releases the row back to `pending` and **never stops the batch**.
- `UpdateStatus` — `CanTransitionCheckup` gate (entity-owned) → repo update; `ListCheckups` — user-scoped read.

---

## 🎓 The design decisions (one line each)

1. **Every row is a MESSAGE, not a flow** (spec's bolded rule) — `flow_id` groups the rows one `POST /checkups` minted; PATCH targets a single row, so pausing message 3-of-3 doesn't touch 1 and 2.
2. **A transient `sending` status joined the enum** — the spec's own claim query (`UPDATE … SET status='sending'`) contradicted its CHECK constraint, so the migration adds it as a scheduler-owned state clients can never set and PATCH refuses to touch.
3. **Claim-then-send, race-safe** — `FOR UPDATE SKIP LOCKED` means two worker instances can't double-send the same email; the **usecase** (never the gateway) marks the row `sent` after the send lands.
4. **`send_date <= now`, never `==`** — the ticker will never land exactly on a send date; equality would silently send nothing (spec calls this out, the unit test locks it in).
5. **Failure releases, retry is free** — a failed send (or missing token) flips the row back to `pending` so the next tick retries; one bad recipient can't stall the batch (unit-tested with a per-recipient failing fake).
6. **Reply-cancel runs *before* the order-classification gate** — a bare "thanks, will do!" doesn't classify as an order update but it IS supplier activity, so `HandleChangeMessage` cancels first and classifies second (both paths unit-tested).
7. **Cancel is idempotent by construction** — `UPDATE … WHERE status='pending' AND cancel_if_reply`; a Graph redelivery finds nothing left to cancel, keeping the whole webhook path safely re-runnable.
8. **The cadence ambiguity got two fields** — the spec payload had only `RepeatFlow: Int` but described "repeat every XYZ, stop after _"; the request now carries `repeatFlow` (total messages) + `repeatEveryDays` (gap), only `repeat_flow` is stored, and send dates are computed once at create time.
9. **Transitions live in the entity** — `CanTransitionCheckup`: `paused ↔ pending`, either → `cancelled`, same-status PATCH is a no-op success; `sent`/`cancelled` terminal, `sending` untouchable — one vocabulary from the CHECK constraint to the 409.
10. **`GET /checkups` exists even though the spec's API table omits it** — "populate them on refresh on the List" needs a read path; it mirrors `GET /discrepancies` (`?status=`, owner-scoped, DB as source of truth for late-connecting clients).
11. **The scheduler is deliberately dumb** — a `main.go` goroutine like the renewal worker: tick → `Execute(ctx)` → log error → keep looping → exit on `ctx.Done()`; it doesn't know what a checkup is.
12. **Flow insert is transactional** — `InsertFlow` wraps the N rows in one tx, so a mid-flow failure leaves zero rows, not a half-cadence.
13. **Same ports discipline** — `MailSender`/`TokenSource`/`CheckupRepo` in `usecase/checkups/ports.go` + `CheckupCanceller` in `usecase/messages/ports.go`, all satisfied structurally; 4 new compile-time asserts in `main.go`.

---

## 🌳 Where things live (new/changed only)

```
overseer/
├── cmd/overseer/main.go                    +checkupRepo · SendDueCheckups · Checkups controller · 1-min scheduler · 4 asserts
├── db/migrations/20260712000006_…​.sql      checkup_messages (spec DDL + 'sending' in CHECK) · idx_checkups_due · idx_checkups_conversation
└── internal/
    ├── entity/checkup.go                   CheckupMessage · CheckupStatus (pending/paused/sending/sent/cancelled) · CanTransitionCheckup
    ├── gateway/graph/graph.go              +SendMail (POST /me/sendMail, 202-checked)
    ├── repo/checkups.go                    InsertFlow (tx) · ClaimDue (SKIP LOCKED) · MarkSent/Release · GetByID · UpdateStatus · ListByUser · CancelPendingByConversation
    ├── usecase/checkups/                   ports.go · create_checkup_messages.go · send_due_checkups.go · update_status.go · list_checkups.go (+tests)
    ├── usecase/messages/                   +CheckupCanceller port · reply-cancel call in handle_change_message.go (+tests)
    ├── controller/http/                    checkups.go (3 endpoints) · checkup DTOs · routes · checkups_e2e_test.go
    └── testutil/fakes.go                   +FakeGraph.SendMail (per-recipient failure) · +InMemCheckups (claim/release semantics)
```

---

## 🔄 How a checkup flows

**Create:** operator fills the Add Check-up card → `POST /checkups {companyName, emailAddress, subject, body, sendDate, cancelIfReply, repeatFlow: 3, repeatEveryDays: 7, conversationId}` → usecase mints one `flowId`, expands to 3 `pending` rows at day 0 / 7 / 14, inserts them in one tx → `201` with every server-minted id → FE list shows the scheduled messages (and repopulates any time via `GET /checkups`).

**Send:** every minute the scheduler calls `SendDueCheckups` → due `pending` rows are atomically claimed to `sending` → for each: Graph token from the cache → `SendMail` → row marked `sent` (log line: `checkups sent count=N`). A failed send or missing token releases the row to `pending` — next tick, next try.

**Reply:** the supplier answers anything on that conversation → Graph webhook → `HandleChangeMessage` → *before* order classification, `CancelPendingByConversation` flips the remaining `pending AND cancel_if_reply` rows to `cancelled` → the flow goes quiet, exactly as promised by the "only if no reply" toggle.

> 💡 Pause is not cancel: `paused` rows are skipped by the claim query (it only takes `pending`) but resume with a one-line PATCH — `sent` and `cancelled` are the only dead ends.

---

## ✅ State of the system

- **Proven (automated):** builds/vets clean · **130/130 test runs** across 14 packages, no network (1 env-gated live-LLM test skipped as always) — cadence expansion (N rows, shared flow, stepped dates), repeat-without-interval rejected with zero leaked rows, due-only claiming (future + paused rows untouched), one-failure-doesn't-stop-the-batch, no-token releases the claim, transition table (legal moves, terminal 409s, foreign-row 404s), reply-cancel scope (flagged+pending+same-conversation only) and its runs-even-for-non-order-messages guarantee, SendMail request shape (recipient/body/saveToSentItems/202), plus the full E2E contract: 201/400/401 on create, server-minted-ids-smuggled-ids-ignored, list ordering + status filter, and the pause→resume→cancel→409 lifecycle.
- **Proven (live, local):** booted against real Postgres — migration applied, a 3-message flow created through the real HTTP stack, pause/resume/cancel exercised, terminal 409 confirmed, status filter and 401 checked, test rows cancelled after.
- **Not yet proven:** the real Graph send leg — needs a token with **`Mail.Send`** (broader than the `Mail.Read` used so far); then: create a checkup due now → watch the next tick send it → check the Sent folder → reply to it and watch `cancel_if_reply` fire end-to-end.
- 📤 Local `checkups-impl` branch — not pushed / no PR yet.

---

## 🚧 Known gaps (priority order)

1. **The scheduler starves without a warm token cache** — sends need a Graph token, and the cache only fills when an authed request arrives after startup; a restart at 3am means due checkups skip-release-retry until the operator opens the add-in. The standing refresh-token gap is now blocking a *write* path, not just liveness.
2. **Release-forever = retry-forever** — a permanently failing send (dead address, revoked scope) re-claims every minute with no backoff, cap, or dead-letter status; needs an attempt counter or a `failed` state.
3. **Crash between claim and send strands the row** — a `sending` row survives a process death and nothing sweeps it back; needs a startup/interval reclaim of `sending` rows older than a few minutes.
4. **No per-flow PATCH** — pausing a whole flow is N requests from the FE; a `PATCH /checkups?flowId=` batch (or flow-level resource) is the natural follow-up once the UI wants it.
5. **Text-only bodies** — `SendMail` sends `contentType: Text`; HTML follow-ups (signatures, formatting) need a one-line gateway extension when the FE cares.
6. **Repo layer still untested** — `checkups.go` joins `discrepancies.go`/`ottoscans.go` in the testcontainers backlog; the claim query's SKIP LOCKED semantics especially deserve a real-Postgres test.

---

## 🗺 What's next

- **Live send smoke test** with a `Mail.Send` token (the runbook above) — the last unproven leg of the last feature.
- **Refresh-token flow** (gap #1) — now the single highest-leverage item across all four features: sync re-runs, webhook liveness, Otto's drive scope, and checkup sends all queue behind it.
- **`sending` sweep + retry cap** (gaps #2–3) — small repo additions that make the scheduler production-honest.
- **Feature-complete milestone:** with Check-ups merged, every `tech_spec.md` feature (Order List, Otto, Check-ups, Audit) exists end-to-end; what remains is hardening (tokens, retries, testcontainers) and the FE.

---

*Written 2026-07-12, covering the local `checkups-impl` branch (12 commits: plan `8be0297` + 11 TDD commits `d3521c5…ed918b8`), built from `docs/superpowers/plans/2026-07-12-checkups.md`. Not yet pushed / no PR.*
