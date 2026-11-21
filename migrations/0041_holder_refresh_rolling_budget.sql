-- One bounded 15-minute ledger on the existing scheduling row. Pre-migration
-- admissions have no per-page timestamps. Stop old workers before starting the
-- new workers. A quiet 15-minute interval lets all untracked admissions age out
-- if the ceiling must cover both versions during the transition.
ALTER TABLE market_holder_refresh_scheduler
    ADD COLUMN IF NOT EXISTS recent_page_admissions TIMESTAMPTZ[] NOT NULL DEFAULT '{}'
    CHECK (cardinality(recent_page_admissions) <= 120 AND array_position(recent_page_admissions, NULL) IS NULL);
