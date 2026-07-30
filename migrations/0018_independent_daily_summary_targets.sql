ALTER TABLE daily_summary_targets
    ADD COLUMN IF NOT EXISTS enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    ADD COLUMN IF NOT EXISTS push_time TEXT NOT NULL DEFAULT '20:00',
    ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS language TEXT NOT NULL DEFAULT 'zh',
    ADD COLUMN IF NOT EXISTS last_sent_date TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_period_end_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_sent_at TIMESTAMPTZ;

UPDATE daily_summary_targets AS target
SET enabled = subscription.daily_summary_enabled,
    push_time = COALESCE(NULLIF(subscription.daily_summary_time, ''), '20:00'),
    timezone = COALESCE(NULLIF(subscription.daily_summary_timezone, ''), 'Asia/Shanghai'),
    label = COALESCE(subscription.daily_summary_label, ''),
    language = COALESCE(NULLIF(subscription.daily_summary_language, ''), 'zh'),
    last_sent_date = COALESCE(subscription.daily_summary_last_sent_date, ''),
    last_period_end_at = subscription.daily_summary_last_period_end_at,
    last_sent_at = subscription.scheduled_push_last_sent_at
FROM subscriptions AS subscription
WHERE target.subscription_id = subscription.id;

CREATE INDEX IF NOT EXISTS idx_daily_summary_targets_enabled
    ON daily_summary_targets(subscription_id, enabled);
