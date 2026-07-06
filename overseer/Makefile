.PHONY: run test vet db-up db-down migrate migrate-down migrate-status

# Sources .env (if present) so the server sees ANTHROPIC_API_KEY etc. —
# the binary itself reads only real env vars (12-factor), it never parses .env.
run:
	@if [ -f .env ]; then set -a; . ./.env; set +a; fi; go run ./cmd/overseer

test:
	go test ./...

vet:
	go vet ./...

db-up:
	docker compose up -d postgres

db-down:
	docker compose down

# dbmate CLI targets (reads DATABASE_URL from .env; migrations in ./db/migrations)
migrate:
	dbmate up

migrate-down:
	dbmate down

migrate-status:
	dbmate status
