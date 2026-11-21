-- Derived facts for complete windows. Only newly hydrated transactions write
-- these summaries; absent legacy summaries remain unknown, never zero.
CREATE TABLE IF NOT EXISTS market_trade_parent_summaries (
    receipt_id BIGINT NOT NULL,
    parent_event_id BIGINT NOT NULL,
    summary_version INTEGER NOT NULL CHECK (summary_version = 1),
    pool_ids BIGINT[] NOT NULL
        CHECK (cardinality(pool_ids) > 0 AND 0 < ALL(pool_ids)
            AND array_position(pool_ids, NULL) IS NULL),
    PRIMARY KEY (receipt_id, parent_event_id),
    FOREIGN KEY (receipt_id, parent_event_id)
        REFERENCES market_transaction_events(receipt_id, market_event_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS market_trade_pool_summaries (
    receipt_id BIGINT NOT NULL,
    parent_event_id BIGINT NOT NULL,
    pool_id BIGINT NOT NULL REFERENCES market_pools(id),
    summary_version INTEGER NOT NULL CHECK (summary_version = 1),
    leg_count INTEGER NOT NULL CHECK (leg_count > 0),
    token_amount NUMERIC CHECK (token_amount >= 0),
    usd_value NUMERIC CHECK (usd_value >= 0),
    usd_valuation_complete BOOLEAN NOT NULL,
    valuation_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (receipt_id, parent_event_id, pool_id),
    FOREIGN KEY (receipt_id, parent_event_id)
        REFERENCES market_trade_parent_summaries(receipt_id, parent_event_id) ON DELETE CASCADE
);

-- Window/recommendation readers often enter from event time, then look up the
-- valid published revision by parent. The existing chain/token index does not
-- supply a leading parent lookup path.
CREATE INDEX IF NOT EXISTS idx_market_trade_ready_parent_valid
    ON market_trade_ready_events (parent_event_id, id) WHERE valid;
