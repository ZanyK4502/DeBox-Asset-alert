CREATE INDEX IF NOT EXISTS idx_rule_trigger_events_cleanup
ON rule_trigger_events (created_at);

CREATE INDEX IF NOT EXISTS idx_aggregate_notifications_cleanup
ON aggregate_notifications (created_at);

CREATE INDEX IF NOT EXISTS idx_aggregation_windows_cleanup
ON aggregation_windows (ends_at);
