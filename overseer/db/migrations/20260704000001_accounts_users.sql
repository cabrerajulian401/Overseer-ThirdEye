-- migrate:up

-- DDL copied from tech_spec.md "Schema Updates" — that section stays the
-- source of truth; these migrations are its executable form.

CREATE TABLE IF NOT EXISTS accounts (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_name TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id              UUID NOT NULL REFERENCES accounts(id),
    email                   TEXT NOT NULL UNIQUE,
    graph_refresh_token_enc TEXT,                             -- encrypted at rest; the plumbing every feature depends on
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS graph_subscriptions (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id               UUID NOT NULL REFERENCES users(id),
    graph_subscription_id TEXT NOT NULL,                      -- Microsoft's id for the webhook subscription
    expires_at            TIMESTAMPTZ NOT NULL,               -- renewal job reads this (~3-day max lifetime)
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- migrate:down

DROP TABLE IF EXISTS graph_subscriptions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS accounts;
