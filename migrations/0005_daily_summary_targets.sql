CREATE TABLE IF NOT EXISTS daily_summary_targets (
    id BIGSERIAL PRIMARY KEY,
    subscription_id BIGINT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    chat_type TEXT NOT NULL CHECK (chat_type IN ('private', 'group')),
    chat_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (subscription_id, chat_type, chat_id)
);

CREATE INDEX IF NOT EXISTS idx_daily_summary_targets_subscription
    ON daily_summary_targets(subscription_id);

CREATE TABLE IF NOT EXISTS daily_summary_deliveries (
    id BIGSERIAL PRIMARY KEY,
    subscription_id BIGINT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    period_end_at TIMESTAMPTZ NOT NULL,
    chat_type TEXT NOT NULL CHECK (chat_type IN ('private', 'group')),
    chat_id TEXT NOT NULL,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (subscription_id, period_end_at, chat_type, chat_id)
);

CREATE INDEX IF NOT EXISTS idx_daily_summary_deliveries_subscription_period
    ON daily_summary_deliveries(subscription_id, period_end_at);

INSERT INTO daily_summary_targets (subscription_id, chat_type, chat_id)
SELECT
    id,
    CASE WHEN daily_summary_chat_type = 'group' THEN 'group' ELSE 'private' END,
    CASE
        WHEN daily_summary_chat_type = 'group' THEN daily_summary_chat_id
        ELSE debox_user_id
    END
FROM subscriptions
WHERE status = 'active'
  AND expires_at > NOW()
  AND daily_summary_enabled = 1
  AND COALESCE(
      CASE
          WHEN daily_summary_chat_type = 'group' THEN daily_summary_chat_id
          ELSE debox_user_id
      END,
      ''
  ) <> ''
ON CONFLICT (subscription_id, chat_type, chat_id) DO NOTHING;
