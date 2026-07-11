-- migrate:up

CREATE TABLE IF NOT EXISTS otto_scans (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(), -- this is the scanId the controllers use
    account_id        UUID NOT NULL REFERENCES accounts(id),
    user_id           UUID NOT NULL REFERENCES users(id),
    message_id        TEXT NOT NULL,                          -- Graph message id
    conversation_id   TEXT NOT NULL,
    company_name      TEXT,
    email_address     TEXT,
    order_id          INTEGER,
    email_url         TEXT,
    extracted_fields  JSONB,                                  -- what the LLM pulled out — the confirm card edits these; RecordOrder writes the approved version
    selected_resource TEXT,                                   -- set at record time
    status            TEXT NOT NULL DEFAULT 'scanned' CHECK (status IN ('scanned','recorded','abandoned')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- migrate:down

DROP TABLE IF EXISTS otto_scans;
