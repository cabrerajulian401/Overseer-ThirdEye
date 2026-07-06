-- migrate:up

-- One subscription per user (MVP): UpsertForUser is ON CONFLICT (user_id),
-- which requires a unique index. graph_subscription_id is unique because the
-- webhook resolves notifications by it.
CREATE UNIQUE INDEX IF NOT EXISTS idx_graph_subscriptions_user
    ON graph_subscriptions (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_graph_subscriptions_graph_id
    ON graph_subscriptions (graph_subscription_id);

-- migrate:down

DROP INDEX IF EXISTS idx_graph_subscriptions_graph_id;
DROP INDEX IF EXISTS idx_graph_subscriptions_user;
