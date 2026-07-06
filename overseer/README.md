# Overseer — Scout backend (Phase 1: Sync)

Go backend for Scout. This phase implements the Order List **First Phase (Sync)**
from [tech_spec.md](../tech_spec.md): onboarding inbox sync, the order list
read API, and the single realtime channel.

## Endpoints

| Endpoint | Purpose |
|---|---|
| `POST /sync` | One-time onboarding inbox sync. Async: returns `202 {syncId}`; completion arrives as a `sync.completed` event on `/ws`. |
| `GET /orders` | Order list. `?status=&cursor=&limit=` for filtering/pagination. Returns `{orders: [...], nextCursor}`. |
| `GET /ws` | The single realtime channel. Typed events: `{"type": "...", "data": {...}}`. |

All routes require `Authorization: Bearer <Graph token>` — the add-in's
Graph-scoped token. MVP is single-user: any token resolves to the seeded user,
and the token is passed through to Microsoft Graph.

## Layout

```
controller / worker → usecase → gateway / repo     (the one rule that never breaks)
```

- `internal/entity` — domain types, no tags, imports nothing
- `internal/usecase/orders` — `BuildActiveOrderList`, `FormatInitialOrderData`, `ListOrders` + ports
- `internal/gateway/graph` — Microsoft Graph mail (ListMessages)
- `internal/gateway/llm` — Anthropic (FilterOrderMessages, ExtractOrderFields); prompts, retries, validation, cost logging live here
- `internal/repo` — Postgres via pgx; dbmate migrations in `db/migrations`
- `internal/controller/http` — Echo router, auth middleware, DTOs
- `internal/controller/ws` — the realtime hub
- `cmd/overseer` — composition root

## Run

```sh
cp .env.example .env         # fill in ANTHROPIC_API_KEY
make db-up                   # postgres via docker compose
make run                     # migrations auto-apply at startup, then serve :8080
```

Migrations also run standalone with the dbmate CLI (`make migrate`,
`make migrate-status`) — same files, `db/migrations/`, dbmate format.

## Test

```sh
make test    # usecase unit tests + controller E2E tests; no DB, no network
```
