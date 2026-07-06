-- migrate:up

CREATE TABLE IF NOT EXISTS orders (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id        UUID NOT NULL REFERENCES accounts(id),
    user_id           UUID NOT NULL REFERENCES users(id),     -- whose mailbox this order came from
    conversation_id   TEXT NOT NULL,                          -- Graph conversation id — HandleChangeMessage and Audit look rows up by this
    company_name      TEXT NOT NULL,                          -- "Delmar Tooling"
    email_address     TEXT,
    order_id          INTEGER,                                -- supplier's PO number (business identifier), NOT the row PK
    email_url         TEXT,
    status            TEXT NOT NULL,                          -- Status Enum, e.g. 'in_transit'
    original_est_date DATE,
    current_est_date  DATE,
    last_messenger    TEXT,
    supporting_quote  TEXT,                                   -- "Following up on the bracket quote"
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- UNIQUE (spec has a plain index): the sync/webhook upsert is
-- ON CONFLICT (user_id, conversation_id), which requires a unique index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_conversation ON orders (user_id, conversation_id);

-- migrate:down

DROP TABLE IF EXISTS orders;
