ALTER TABLE market_address_labels
    DROP CONSTRAINT IF EXISTS market_address_labels_market_project_id_address_key;

ALTER TABLE market_address_labels
    ADD CONSTRAINT market_address_labels_project_chain_address_key
        UNIQUE (market_project_id, chain_id, address);

CREATE INDEX IF NOT EXISTS idx_market_address_labels_project_chain
    ON market_address_labels (market_project_id, chain_id, address);
