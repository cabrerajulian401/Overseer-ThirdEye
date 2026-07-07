# 📡 Scout Phase 2 (Post-Sync) — High-Level Overview

> **What this is** — the condensed walkthrough for the **Order List "Second Phase (Post-Sync)"** from `tech_spec.md`: how new supplier mail keeps the order list live after the one-time onboarding sync. Companion to the Phase 1 doc in `SupplierList_Sync_Onboarding/`.
>
> 📄 *Local-only (gitignored posture, same as Phase 1) — never pushed.*
>
> 👀 **To read rendered:** open in VS Code, press <kbd>⇧⌘V</kbd>.

---

## ⚡ What was built

Phase 1 seeded the `orders` table once. Phase 2 keeps it **alive**: Microsoft Graph pushes a change notification whenever mail arrives, Overseer fetches that one message, runs it through the LLM, merge-upserts the order row, and pushes `order.updated` over the existing `/ws` channel — the UI updates in real time without polling.

Two new endpoints + one background worker:

| Piece | What it does | Contract |
|-------|--------------|----------|
| `POST /webhooks/graph` | receives Graph change notifications | echo `validationToken` as `text/plain 200` (handshake); otherwise enqueue + `202` instantly — **no Graph/LLM work in the handler** |
| `POST /subscriptions` | registers the Graph webhook subscription for the authed user | `201 {"subscriptionId","expiresAt"}` |
| renewal worker | hourly pass; Graph message subscriptions die in ~3 days | renews anything expiring within 12h, using cached tokens |

**The job at the center** — `usecase/messages/HandleChangeMessage` (spec: `jobs/HandleChangeMessage`):
notification → resolve subscription→user → cached Graph token → `gateway/graph.GetMessage` → LLM → merge-upsert → re-read row → publish `order.updated`.

---

## 🎓 The design decisions (one line each)

1. **Webhook is the only unauthenticated route** — Graph sends no bearer token; a `clientState` shared secret (set at subscription creation, echoed in every notification) authenticates instead. Mismatches are dropped with a warn but still `202`'d (Graph wants an ack regardless).
2. **Token cache bridges the auth gap** — notifications carry no user token, but the job must call Graph. The auth middleware now `Put`s every request's bearer token into an in-memory `tokencache` keyed by user; background jobs `GraphToken(userID)` it back out. MVP stand-in for the real refresh-token flow (`users.graph_refresh_token_enc`, still unused).
3. **Filter-gate only NEW conversations** — the spec's webhook flow never filters, but without it every newsletter becomes an order row. Unknown conversation → `FilterOrderMessages` first; known order conversation → straight to extraction (it was classified at sync).
4. **Merge, don't clobber** — one new message rarely mentions every field. Existing rows merge field-by-field: `OriginalEstDate` **never moves once set**, `CurrentEstDate`/`Status`/quote update when the extraction has them, identity fields (company, PO#) survive empty extractions, `LastMessenger`/`EmailURL` always refresh from the new message.
5. **Idempotency is the same upsert** — Graph delivers at-least-once; re-delivery re-runs the job into the same `ON CONFLICT (user_id, conversation_id)` row. No dedup table needed (accepted cost: a duplicate LLM call on redelivery).
6. **Outcome enum instead of logs in the usecase** — `Execute` returns `created | updated | skipped_not_order | skipped_unknown_subscription`; the controller logs it. Usecases stay log-free, skips are normal operation, not errors.
7. **`order.updated` is an event type, not a socket** — same `/ws` hub, new `case entity.Order` in its DTO switch (each edge owns its own DTOs, so the ws hub has its own `orderDTO` mirroring the HTTP one).
8. **Subscription lifecycle in two usecases** — `EnsureSubscription` (create/replace, one row per user via new unique index) and `RenewDueSubscriptions` (list-expiring → renew → update expiry; per-row failures log-and-skip so one dead token can't stall the fleet).
9. **Same ports discipline** — new packages declare the interfaces they consume (`MessageGetter`, `SubscriptionResolver`, `TokenSource`…); `main.go` grew 10 more compile-time asserts.
10. **Spec deviations, deliberate:** Checkups' `CancelIfReply` reply-detection is a marked extension-point comment (Checkups doesn't exist yet — YAGNI); `GetByConversation` reworked to `(uuid.UUID) → (Order, bool, error)` killing the Phase-1 gap of a `string` userID + leaked `pgx.ErrNoRows`.

---

## 🌳 Where things live (new/changed only)

```
overseer/
├── cmd/overseer/main.go                    +config (PUBLIC_BASE_URL, GRAPH_CLIENT_STATE), wiring, renewal ticker
├── db/migrations/20260705000003_…​.sql      unique indexes: graph_subscriptions(user_id), (graph_subscription_id)
└── internal/
    ├── tokencache/                         in-memory userID → last-seen Graph token
    ├── gateway/graph/                      +GetMessage · +CreateSubscription · +RenewSubscription (+httptest tests)
    ├── repo/subscriptions.go               upsert-per-user · resolve (joins users) · list-expiring · update-expiry
    ├── usecase/messages/                   GetLatestMessage · HandleChangeMessage (the webhook job)
    ├── usecase/subscriptions/              EnsureSubscription · RenewDueSubscriptions
    ├── controller/http/                    webhooks.go · subscriptions.go · TokenSink hook in auth middleware
    ├── controller/ws/hub.go                +orderDTO mapping for order.updated
    └── testutil/fakes.go                   +InMemSubscriptions · +FakeTokens · FakeGraph grew 3 methods
```

---

## 🔄 How a change notification flows

New supplier email lands → Graph `POST /webhooks/graph` `{subscriptionId, clientState, resourceData:{id}}` → clientState checked → **202 returned immediately**, job dispatched → subscription row → user → token cache → `GetMessage` (one message, same `$select` as the sync) → known conversation? merge-extract : filter-then-extract → upsert → re-read authoritative row → `{"type":"order.updated","data":{…orderDTO}}` to every `/ws` client → UI updates the card in place.

> 💡 The subscription handshake is the other half: `POST /subscriptions` → Graph calls back `?validationToken=…` → we echo it text/plain within 10s → Graph activates the subscription → row stored with expiry → hourly worker keeps it alive.

---

## ✅ State of the system

- **Proven (automated):** builds/vets clean · **40/40 tests** across 10 packages in <2s, no network — merge semantics, idempotent redelivery, junk-mail gate, unknown-subscription & missing-token paths, handshake echo, spoofed-clientState drop, 401 on unauthed `/subscriptions`, ws DTO shape.
- **Proven (live, local):** migration applies at boot · handshake echoes decoded token as text/plain · subscription upsert works on real Postgres · spoofed clientState → 202 + warn + dropped.
- **Not yet proven:** the real-mailbox leg (simulated notification with a *real* message id → real Graph fetch → real LLM extraction → row + ws event) — needs a fresh Graph token; and a *true* end-to-end Graph subscription (needs a public HTTPS URL, e.g. dev tunnel).

---

## 🚧 Known gaps (priority order)

1. Token cache is in-memory and access tokens die in ~1h — a quiet mailbox after a server restart means webhook jobs fail until the next authed request. The real fix is the refresh-token flow.
2. Real subscription creation needs `PUBLIC_BASE_URL` to be public HTTPS (ngrok/dev tunnel for dev; localhost default only supports simulated notifications).
3. Redelivered notifications re-pay the LLM call (idempotent but not free).
4. Renewal worker starts on a ticker only — no renewal pass at boot, so a subscription can lapse during long downtime.
5. Extraction sees only the single new message, not the whole thread — merge compensates, but thread-refetch would extract richer context.
6. Checkups' `CancelIfReply` hook still pending (extension point marked in `HandleChangeMessage`).

---

## 🗺 What's next

- **Live smoke test** of the real-mail leg (runbook is Task 11 in `docs/superpowers/plans/2026-07-05-order-list-post-sync.md`).
- **Phase 3 — Otto:** drive/excel gateways + scan endpoints; reuses this async-job + LLM pattern.
- **Phase 4 — Check-ups:** the scheduler worker exists now (renewal ticker) — checkups add the `send_due` query and wire `CancelIfReply` into `HandleChangeMessage`.
- **Phase 5 — Audit:** `FindDiscrepancies` rides *inside* this webhook pipeline — `HandleChangeMessage` is where every new supplier message already flows.

---

*Written 2026-07-05, covering the `order_list` branch commits `e06ca27…de06870` (10 TDD commits). Plan + live-test runbook: `docs/superpowers/plans/2026-07-05-order-list-post-sync.md`.*
