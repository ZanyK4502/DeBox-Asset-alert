-- Only pending/retryable summaries participate in fast queue-only wakeups.
-- Unknown and terminal records remain outside the automatic delivery queue.
CREATE INDEX IF NOT EXISTS idx_daily_summary_dispatches_retry_due
    ON daily_summary_dispatches(COALESCE(retry_at,created_at),id)
    WHERE status IN ('pending','failed') AND attempts<5
      AND (status='pending' OR retry_at IS NOT NULL);
