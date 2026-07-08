-- migrate:up

CREATE TABLE IF NOT EXISTS discrepancies (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id         UUID NOT NULL REFERENCES accounts(id),
    user_id            UUID NOT NULL REFERENCES users(id),
    conversation_id    TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')), -- the UI needs to clear findings, not show them forever
    company_name       TEXT NOT NULL,
    email_address      TEXT,
    email_deeplink_url TEXT,                                  -- so the UI can open a draft to the supplier
    order_id           INTEGER,
    priority           TEXT NOT NULL CHECK (priority IN ('high','medium','low')),
    discrepancy_title  TEXT NOT NULL,
    discrepancy_detail JSONB,                                 -- { "previously_agreed": ..., "new_metric": ..., "type": "price" | "date" | "unit" }
    discrepancy_quotes JSONB,                                 -- [{ "quote": ..., "message_link": ... }] — old conversation history w/ link to message
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The read path: GET /discrepancies filters by owner (+ status), newest first.
CREATE INDEX IF NOT EXISTS idx_discrepancies_user ON discrepancies (user_id, status);

-- migrate:down

DROP TABLE IF EXISTS discrepancies;
