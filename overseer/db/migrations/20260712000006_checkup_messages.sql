-- migrate:up

CREATE TABLE IF NOT EXISTS checkup_messages (                 -- one row per scheduled MESSAGE, not per flow
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(), -- this IS the checkupId that PATCH /checkups/{checkupId} targets — no separate column
    account_id       UUID NOT NULL REFERENCES accounts(id),
    user_id          UUID NOT NULL REFERENCES users(id),
    flow_id          UUID NOT NULL,                           -- groups the messages minted together by one POST /checkups
    conversation_id  TEXT,                                    -- lets HandleChangeMessage find pending rows to cancel on reply
    company_name     TEXT NOT NULL,
    email_address    TEXT NOT NULL,
    order_id         INTEGER,
    -- 'sending' is the scheduler's transient claim state (claim = UPDATE pending → sending),
    -- so it must be a legal column value even though clients never set it.
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paused','sending','sent','cancelled')),
    subject          TEXT NOT NULL,
    body             TEXT NOT NULL,
    send_date        TIMESTAMPTZ NOT NULL,                    -- when the message goes out; scheduler queries send_date <= now
    current_est_date DATE,
    cancel_if_reply  BOOLEAN NOT NULL DEFAULT false,
    repeat_flow      INTEGER,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_checkups_due ON checkup_messages (send_date) WHERE status = 'pending'; -- the scheduler's due query
CREATE INDEX IF NOT EXISTS idx_checkups_conversation ON checkup_messages (user_id, conversation_id) WHERE status = 'pending'; -- reply-cancel lookup

-- migrate:down

DROP TABLE IF EXISTS checkup_messages;
