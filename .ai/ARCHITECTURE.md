# Scout — Architecture

Scout is ThirdEye's procurement AI assistant for wholesale/reseller distributors. It lives inside Outlook, watches supplier email conversations, and turns them into structured order data: a live order list, one-click ERP entry (**Otto**), scheduled follow-ups (**Check-ups**), and buyer/seller discrepancy detection (**Audit**).

This document is the structural map. The [tech spec](tech_spec.md) holds the endpoint-by-endpoint detail.

---

## 1. System Context

```
┌──────────────────┐        ┌─────────────────────────────────────┐
│  Loki            │        │  Overseer (Go backend)              │
│  Outlook add-in  │  REST  │                                     │
│  (React)         │───────▶│  controllers ─▶ usecases ─▶ gateways│
│                  │◀───────│       ▲              │         │    │
│  future: web     │  /ws   │       │              ▼         │    │
│  dashboard       │ events │    workers        Postgres     │    │
└──────────────────┘        └───────┼───────────────────────┼────┘
                                    │ webhooks              │ API calls
                            ┌───────┴────────┐    ┌─────────┴─────────┐
                            │ Microsoft Graph │    │ LLM provider      │
                            │ mail, OneDrive, │    │ (behind           │
                            │ Excel, webhooks │    │  gateway/llm)     │
                            └────────────────┘    └───────────────────┘
```

**Components**

| Component | Role |
|---|---|
| **Loki** (Outlook add-in, React) | The UI. Reads the open email via Office.js, calls Overseer's REST API, listens on one realtime channel. Any future client (web dashboard) uses the same API. |
| **Overseer** (Go backend) | All logic. Serves REST, receives Graph webhooks, runs background workers, orchestrates LLM calls. |
| **Microsoft Graph** | Source of truth for mail. Read threads, send mail, change notifications (webhooks), OneDrive search, Excel workbook writes. |
| **LLM provider** | Extraction, filtering, ranking, discrepancy detection. Only ever called through `gateway/llm`. |
| **Postgres** | Derived/product state: orders, checkups, discrepancies, otto scans, users/accounts, subscriptions. Mail itself is *not* stored — threads are re-fetched from Graph when needed. |

---

## 2. Layering (the one rule that never breaks)

```
controller / worker  ─▶  usecase  ─▶  gateway / repo
```

- **Controllers** are HTTP entrypoints only. **Workers** (scheduler ticks, webhook jobs) are the non-HTTP entrypoints. Both call usecases; nothing calls back up.
- **Usecases** hold the business logic and own all orchestration — including agent loops. A multi-step LLM flow (extract → search → rank) is a usecase calling gateways in sequence, never a "master agent" gateway calling other gateways.
- **Gateways** are dumb adapters to external services. They never touch the DB and never contain business decisions.

**Gateways:**

| Gateway | Wraps | Used by |
|---|---|---|
| `gateway/graph` (`ListMessages`, `GetMessage`, `SendMail`) | Graph mail API | sync, webhook job, checkups |
| `gateway/drive_gateway` | OneDrive search | Otto destination resolution |
| `gateway/excel_gateway` | Excel table-row append | Otto RecordOrder |
| `gateway/llm` (`FilterOrderMessages`, `ExtractOrderFields`, `RankDestinations`, `PlanSheetAppend`, `FindDiscrepancies`) | LLM provider | every feature |

`gateway/llm` owns prompts, model choice, retries, structured-output validation, and token/cost logging — swapping models is a one-file change, and usecase tests stub it.

---

## 3. API Surface

REST, resource-oriented, no verbs in paths. Server mints all IDs. `userId`/`accountId` always come from auth context, never the request body.

| Endpoint | Purpose |
|---|---|
| `POST /sync` | One-time onboarding inbox sync (async, `202 { syncId }`) |
| `GET /orders` | Order list; `?status&cursor&limit` for filtering/pagination |
| `POST /otto/scans` | Extract fields + top-3 destinations from an email |
| `POST /otto/scans/{scanId}/record` | Human-in-the-loop approval → write to ERP |
| `POST /checkups` | Create a follow-up flow (expands to per-message rows) |
| `PATCH /checkups/{checkupId}` | Pause / resume / cancel |
| `POST /webhooks/graph` | Graph change notifications (echo validationToken; enqueue + 202) |
| `GET /ws` | The single realtime channel |

**Realtime:** one connection, typed events — `{ "type": "order.updated" | "discrepancy.found", "data": {...} }`. New features add event types, never new sockets.

**Async rule:** anything that fans out to Graph + LLM (initial sync, possibly Otto scans) returns fast with an id and delivers results via the realtime channel.

---

## 4. Background Work

| Worker | Trigger | Does |
|---|---|---|
| `jobs/HandleChangeMessage` | Graph webhook (enqueued) | Fetch message → LLM extract → upsert order → push `order.updated` → run Audit → cancel pending checkups on reply (`CancelIfReply`). **The only place replies are detected.** Must be idempotent (Graph re-delivers). |
| `workers/scheduler` | 1-min ticker | Calls `SendDueCheckups` (`send_date <= now AND status='pending'`, claim rows to prevent double-send) and renews Graph subscriptions (they expire ~every 3 days). |
| Sync job | `POST /sync` | Month of inbox history → LLM filter/extract → seed `orders`. |

---

## 5. Feature Flows

**Order List (sync, then live)**
1. Onboarding: `POST /sync` → Graph `ListMessages` → `llm.FilterOrderMessages` → `llm.ExtractOrderFields` → upsert `orders`.
2. Steady state: new mail → Graph webhook → `HandleChangeMessage` → order upserted → `order.updated` event → UI updates in place. `GET /orders` serves reloads.

**Otto (email → ERP entry, human-approved)**
1. User clicks "Ask Otto" → `POST /otto/scans { messageId, conversationId }`.
2. `ResolveDestinations` (the agent loop): `llm.ExtractOrderFields(thread)` → `drive_gateway` searches → `llm.RankDestinations` → store scan, return fields + top-3 destinations.
3. UI shows **one confirm card** (editable fields + destination). Nothing is written yet.
4. `POST /otto/scans/{scanId}/record` with the user-**edited** fields → `RecordOrder`: `llm.PlanSheetAppend` decides *where/how* (never re-decides values) → `excel_gateway` appends → return `webUrl` → "Recorded ✓".

**Check-ups (scheduled follow-ups)**
1. `POST /checkups` → `CreateCheckupMessages` expands the cadence into one row per message (`flow_id` groups them).
2. Scheduler sends due rows via `gateway/graph/SendMail`; status: `pending → paused/sent/cancelled` (one enum everywhere).
3. If the supplier replies and `cancel_if_reply` is set, `HandleChangeMessage` cancels the remaining rows.

**Audit (discrepancy detection)**
1. Rides the webhook job — no infrastructure of its own.
2. `CatchDiscrepancies`: re-fetch thread from Graph (no messages table; Graph is source of truth) → `llm.FindDiscrepancies(newMessage, history)` → store in `discrepancies` → push `discrepancy.found`.
3. Findings have a status (`open/resolved/dismissed`) so the UI can clear them; DB is the source of truth for clients that connect later.

---

## 6. Data Model

Postgres, `snake_case`. Every domain table carries `account_id` (tenant: the distributor company) **and** `user_id` (operator whose mailbox the data came from); every query filters on them from auth context.

```
accounts ──< users ──< graph_subscriptions
                │
                ├──< orders            (conversation_id ← webhook/audit lookup key)
                ├──< discrepancies     (status: open/resolved/dismissed)
                ├──< checkup_messages  (one row per MESSAGE; flow_id groups a flow;
                │                       partial index on send_date WHERE pending)
                └──< otto_scans        (extracted_fields JSONB; scanned/recorded/abandoned)
```

Notes:
- `orders.order_id` is the supplier's PO number (business identifier), not the row PK.
- No messages table — email content is re-fetched from Graph on demand.
- Full DDL lives in the tech spec's Schema Updates section.

## 7. Auth

- Add-in acquires a Graph-scoped token via Office.js SSO (MSAL fallback) and hands it to Overseer; Overseer calls Graph on the user's behalf and stores the **refresh token encrypted** on `users`.
- Graph mailbox subscription is created at onboarding; the scheduler renews it before the ~3-day expiry.
- **MVP: single-user mode** — seed one account + one user row. The `account_id`/`user_id` columns exist everywhere now, so multi-user later is a login page, not a migration.

## 8. Testing Strategy

Two tiers, matching the two rings that contain logic. The layering rule is what makes both cheap: every external dependency (Graph, LLM, Postgres, the clock) enters a usecase through an interface, so tests replace it. **No test ever calls a real external service.**

| Tier | Scope | Real | Faked |
|---|---|---|---|
| **Usecase unit tests** — one per usecase | that usecase's business rules (scheduler `<= now` logic, status transitions, cancel-on-reply, "user-approved fields only") | the usecase | its ports, as tiny hand-written stubs |
| **Controller E2E tests** — one per endpoint | full HTTP request → response, plus recorded side effects | router, middleware, controllers, DTOs, usecases | only the outermost adapters: fake Graph/LLM/Drive/Excel gateways, in-memory repos, an event recorder for `/ws` pushes |

E2E tests assert three things per endpoint: status code, JSON shape (camelCase, server-minted IDs), and the calls the fakes recorded — i.e. what *would* have hit Graph or the LLM. Auth is covered at this tier too: a `userId` smuggled into a request body must be ignored in favor of the token.

Upgrade path: swap the in-memory repos for a real Postgres via testcontainers to also exercise the repo layer's SQL and migrations — nothing else changes. Details, fixtures, and the per-usecase must-cover checklist live in [.ai/go-code-organization.md](.ai/go-code-organization.md).

## 9. Build Order (de-risk fastest)

1. **Auth + sync + `GET /orders`** — forces every layer to exist once; proves token exchange and LLM extraction quality (the two things that can kill the project).
2. **Webhook + `order.updated`** — the realtime spine.
3. **Otto** — hero feature; reuses everything above, no new infra.
4. **Check-ups** — adds the scheduler.
5. **Audit** — pure add-on to the webhook job.

Non-goals for the MVP: microservices, multi-ERP (Excel first), account configuration UX, optimizing past the first working solution.
