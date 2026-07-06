# 🛰️ Scout Phase 1 (Sync) — High-Level Overview

> **What this is** — the condensed walkthrough: what Phase 1 built, the ideas behind it, what's proven, what's left. The full file-by-file deep-dive (with code) lives in **`PHASE1_WALKTHROUGH_DETAILED.MD`**.
>
> 📄 *Both files are local-only (gitignored) — never pushed.*
>
> 👀 **To read rendered:** open in VS Code, press <kbd>⇧⌘V</kbd>.

---

## ⚡ What was built

Phase 1 implements the **Order List "First Phase (Sync)"** from `tech_spec.md`: a one-time onboarding sync that reads a month of the operator's inbox, uses an LLM to find and structure supplier order conversations, and seeds the `orders` table — plus the read API and the realtime channel that reports completion.

Everything lives in a new Go module at **`overseer/`**. Three endpoints, all requiring `Authorization: Bearer <Microsoft Graph token>`:

| Endpoint | What it does | Returns |
|----------|--------------|---------|
| `POST /sync` | kicks off the inbox sync as a background job | `202 {"syncId"}` immediately |
| `GET /orders` | reads the list with filtering + cursor pagination | `200 {"orders", "nextCursor"}` |
| `GET /ws` | the single realtime WebSocket channel | pushes `{"type": "sync.completed", …}` |

**Stack:** Echo (HTTP) → usecases (business logic) → Microsoft Graph (mail) · Anthropic (LLM) · Postgres via pgx, with dbmate migrations.

---

## 🎓 The design decisions (one line each)

*Each has a full section in the detailed file (§2 there).*

1. **Clean architecture** — `controller → usecase → gateway/repo`; the arrow never reverses. Payoff: sub-second tests with tiny fakes, one-package service swaps.
2. **Ports & adapters** — usecases declare the interfaces they need; adapters satisfy them structurally; `main.go` is the composition root and pins contracts at compile time.
3. **Tag-free entities** — no `json:`/`db:` tags in the domain; controllers map to camelCase DTOs, repos map to snake_case columns.
4. **Async jobs** — slow work returns `202 {"syncId"}` in milliseconds, runs in a goroutine, announces completion over the WebSocket.
5. **One realtime channel** — a single `/ws` socket with typed events; features add event *types*, never new sockets.
6. **Idempotency by upsert** — `ON CONFLICT (user_id, conversation_id)`; running the sync twice is harmless. (Spec deviation: that index had to be made `UNIQUE`.)
7. **Cursor pagination** — keyset on `created_at`, opaque base64 cursor, fetch `limit+1` to detect the next page. No `OFFSET`.
8. **dbmate two ways** — same migration files run via CLI in dev and via `go:embed` at every server boot, so the schema can't be stale.
9. **LLM gateway discipline** — task-shaped methods (`FilterOrderMessages`, `ExtractOrderFields`), structured outputs with a JSON Schema, boundary validation + one retry, token-count logging from day one. The *usecase* is the agent loop.
10. **MVP auth** — any bearer token resolves to the single seeded user and passes through to Graph; identity always comes from auth context, never the request body. Schema is already multi-user (`account_id`/`user_id` everywhere).
11. **Two-tier testing** — usecase unit tests with hand-written fakes + controller E2E over `httptest`; no test touches a real service.

---

## 🌳 Where things live

```
overseer/
├── cmd/overseer/main.go     composition root: config → migrate → seed → wire → serve
├── db/migrations/           accounts/users · orders (dbmate, also embedded via embed.go)
└── internal/
    ├── entity/              plain shared structs (Order, EmailMessage, Event, …)
    ├── usecase/orders/      ports.go + BuildActiveOrderList · FormatInitialOrderData · ListOrders
    ├── gateway/             graph/ (ListMessages) · llm/ (filter + extract, Anthropic SDK)
    ├── repo/                pgx pool · dbmate-at-boot · orders/users SQL
    ├── controller/          http/ (auth, handlers, DTOs, E2E tests) · ws/hub.go
    └── testutil/fakes.go    FakeGraph · FakeLLM · InMemOrders · EventRecorder
```

---

## 🔄 How a sync flows

`POST /sync` → auth middleware resolves the user → server mints `syncId` → **202 returned immediately** → background job: Graph `ListMessages` (last 30 days) → LLM filters order threads → LLM extracts fields per thread → upsert all rows → publish `sync.completed` over `/ws` → client re-fetches `GET /orders`.

> 💡 Every failure-prone step (Graph, LLM) runs **before** the first DB write — a failure leaves the table untouched (spec: no partial writes).

---

## ✅ State of the system

- **Proven:** builds/vets clean · **22/22 tests** in <1s with no network · migrations apply via CLI *and* embedded at boot · seed user created · auth gate returns 401 · read path returns 200 · upsert idempotency confirmed on real Postgres.
- **Not yet proven:** a sync against a *real* mailbox (real Graph token) and real extraction quality — the highest-value manual test (§7 Tier 1 in the detailed file: Graph Explorer token → `curl POST /sync` → eyeball results).
- 📤 Pushed as branch `order_list` to `github.com/cabrerajulian401/Overseer-ThirdEye`.

---

## 🚧 Known gaps (priority order)

1. Auth trusts any token (fine single-user; validate before multi-user)
2. `graph_refresh_token_enc` unused — Phase 2 background jobs will need it
3. Sync failures only logged — no `sync.failed` event yet
4. Sync jobs not persisted across restarts
5. WS hub broadcasts to everyone (correct with one user)
6. No graceful shutdown / rate limiting / WS origin check
7. `GetByConversation` takes `string` userID instead of `uuid.UUID`
8. LLM extraction is sequential (add a bounded worker pool if slow)

---

## 🗺 What's next

- **Phase 2** — Graph webhook (`POST /webhooks/graph`, enqueue + 202), `order.updated` events, subscription-renewal scheduler. Most building blocks already exist.
- **Phase 3** — Otto: drive/excel gateways, scan endpoints, same async + LLM patterns.
- **Phase 4** — Check-ups: scheduler with `FOR UPDATE SKIP LOCKED` (add testcontainers here).
- **Phase 5** — Audit: rides inside the webhook pipeline, no new infrastructure.
- **Soon regardless:** the live smoke test · an architecture-enforcement test (fail CI if inner layers import outer) · a real `/ws` test.

---

## 🔑 Git/SSH fix (TL;DR)

`git push` kept prompting for an SSH passphrase even after switching the remote to HTTPS, because a global `insteadOf` rule was silently rewriting every HTTPS GitHub URL back to SSH. Fixed one-time: remote set to HTTPS, `gh auth setup-git` registered as credential helper, global rewrite removed. Side effect: *all* repos with HTTPS remotes now genuinely use HTTPS + the gh token. Full story in the detailed file, §10.

---

*Condensed 2026-07-05 from the original walkthrough (covers the `order_list` branch as pushed, `3f7e590`). Details: `PHASE1_WALKTHROUGH_DETAILED.MD`.*
