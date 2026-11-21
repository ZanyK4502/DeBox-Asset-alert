-- Collection lifecycle is independent of notification recovery cutoffs.
-- Legacy history starts unproven. Only a scanner observation of committed
-- zero demand can open a proven idle interval.
CREATE TABLE IF NOT EXISTS market_chain_collection_states (
    chain_id BIGINT PRIMARY KEY,
    chain_key TEXT NOT NULL UNIQUE,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    active_target_count BIGINT NOT NULL DEFAULT 0 CHECK (active_target_count >= 0),
    phase TEXT NOT NULL CHECK (phase IN ('unobserved','active','idle')),
    acknowledged_idle_interval_id BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS market_collection_idle_intervals (
    id BIGSERIAL PRIMARY KEY,
    chain_id BIGINT NOT NULL REFERENCES market_chain_collection_states(chain_id),
    chain_key TEXT NOT NULL,
    from_at TIMESTAMPTZ NOT NULL,
    through_at TIMESTAMPTZ,
    opened_revision BIGINT NOT NULL,
    CHECK (through_at IS NULL OR through_at >= from_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_market_collection_one_open_idle
    ON market_collection_idle_intervals(chain_id) WHERE through_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_market_collection_pending_idle
    ON market_collection_idle_intervals(chain_id,id);
CREATE TABLE IF NOT EXISTS market_collection_gaps (
    id BIGSERIAL PRIMARY KEY,
    chain_id BIGINT NOT NULL REFERENCES market_chain_collection_states(chain_id),
    chain_key TEXT NOT NULL,
    cursor_key TEXT NOT NULL,
    idle_interval_id BIGINT NOT NULL REFERENCES market_collection_idle_intervals(id),
    from_block BIGINT NOT NULL CHECK (from_block > 0),
    through_block BIGINT NOT NULL CHECK (through_block >= from_block),
    from_at TIMESTAMPTZ NOT NULL,
    through_at TIMESTAMPTZ NOT NULL CHECK (through_at >= from_at),
    reason TEXT NOT NULL DEFAULT 'proven_zero_collection_targets',
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(chain_id,cursor_key,idle_interval_id,from_block,through_block)
);
CREATE INDEX IF NOT EXISTS idx_market_collection_gaps_time ON market_collection_gaps(chain_id,through_at,from_at);

CREATE OR REPLACE FUNCTION debox_market_collection_target_count(requested_chain BIGINT)
RETURNS BIGINT LANGUAGE SQL STABLE AS $$
    SELECT COUNT(*) FROM market_projects mp
    JOIN market_project_deployments mpd ON mpd.market_project_id=mp.id AND mpd.status='active'
    JOIN market_asset_deployments mad ON mad.id=mpd.market_asset_deployment_id
    WHERE mp.status='active' AND mad.chain_id=requested_chain
$$;

-- Hold the same writer exclusion needed to install the triggers while taking
-- the initial counts, so a commit cannot slip between baseline and tracking.
LOCK TABLE market_projects, market_project_deployments, market_asset_deployments
    IN SHARE ROW EXCLUSIVE MODE;

INSERT INTO market_chain_collection_states(chain_id,chain_key,active_target_count,phase)
SELECT chain_id,chain_key,debox_market_collection_target_count(chain_id),
       CASE WHEN debox_market_collection_target_count(chain_id)>0 THEN 'active' ELSE 'unobserved' END
FROM (VALUES (1::bigint,'ethereum'),(10,'optimism'),(56,'bsc'),(137,'polygon'),(8453,'base'),(42161,'arbitrum')) c(chain_id,chain_key)
ON CONFLICT(chain_id) DO NOTHING;

CREATE OR REPLACE FUNCTION debox_reconcile_market_collection_chain(requested_chain BIGINT)
RETURNS VOID LANGUAGE plpgsql AS $$
DECLARE previous market_chain_collection_states%ROWTYPE; targets BIGINT; observed TIMESTAMPTZ;
BEGIN
    SELECT * INTO previous FROM market_chain_collection_states WHERE chain_id=requested_chain FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    targets := debox_market_collection_target_count(requested_chain);
    IF targets=previous.active_target_count THEN RETURN; END IF;
    observed := clock_timestamp();
    IF targets>0 AND previous.phase='idle' THEN
        UPDATE market_collection_idle_intervals SET through_at=GREATEST(from_at,observed)
        WHERE chain_id=requested_chain AND through_at IS NULL;
    ELSIF targets>0 AND previous.active_target_count=0 THEN
        -- Activation can win the race with the scanner's first zero-demand
        -- observation. Preserve its timestamp without certifying any idle time.
        INSERT INTO market_collection_idle_intervals(chain_id,chain_key,from_at,through_at,opened_revision)
        VALUES(requested_chain,previous.chain_key,observed,observed,previous.revision+1);
    END IF;
    -- A transaction's zero count is not yet a committed idle proof. The
    -- scanner will open that interval only after seeing zero after commit.
    UPDATE market_chain_collection_states
       SET active_target_count=targets, revision=revision+1,
           phase=CASE WHEN targets>0 THEN 'active' ELSE 'unobserved' END,
           updated_at=observed
     WHERE chain_id=requested_chain;
END
$$;

CREATE OR REPLACE FUNCTION debox_market_collection_lifecycle_changed()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE affected BIGINT[]; target_chain BIGINT; old_id BIGINT; new_id BIGINT;
BEGIN
    -- Writers can touch multiple deployments in different orders. Serialize
    -- their deferred reconciliation after the project edits, at commit.
    -- Ordinary scanner reads never take this transaction-scoped writer lock.
    PERFORM pg_advisory_xact_lock(hashtextextended('debox-market-collection-lifecycle-writers',0));
    IF TG_TABLE_NAME='market_projects' THEN
        IF TG_OP<>'INSERT' THEN old_id:=OLD.chain_id; END IF;
        IF TG_OP<>'DELETE' THEN new_id:=NEW.chain_id; END IF;
        SELECT ARRAY_AGG(DISTINCT id ORDER BY id) INTO affected FROM (
            SELECT old_id AS id UNION SELECT new_id UNION
            SELECT mad.chain_id FROM market_project_deployments mpd
            JOIN market_asset_deployments mad ON mad.id=mpd.market_asset_deployment_id
            WHERE mpd.market_project_id=CASE WHEN TG_OP='DELETE' THEN OLD.id ELSE NEW.id END
        ) chains WHERE id IS NOT NULL;
    ELSIF TG_TABLE_NAME='market_project_deployments' THEN
        IF TG_OP<>'INSERT' THEN old_id:=OLD.market_asset_deployment_id; END IF;
        IF TG_OP<>'DELETE' THEN new_id:=NEW.market_asset_deployment_id; END IF;
        SELECT ARRAY_AGG(DISTINCT chain_id ORDER BY chain_id) INTO affected
        FROM market_asset_deployments WHERE id IN(old_id,new_id);
    ELSE
        IF TG_OP<>'INSERT' THEN old_id:=OLD.chain_id; END IF;
        IF TG_OP<>'DELETE' THEN new_id:=NEW.chain_id; END IF;
        SELECT ARRAY_AGG(DISTINCT id ORDER BY id) INTO affected
        FROM UNNEST(ARRAY[old_id,new_id]) id WHERE id IS NOT NULL;
    END IF;
    FOREACH target_chain IN ARRAY COALESCE(affected,ARRAY[]::bigint[]) LOOP
        PERFORM debox_reconcile_market_collection_chain(target_chain);
    END LOOP;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER market_projects_collection_lifecycle
AFTER INSERT OR DELETE OR UPDATE OF status,chain_id ON market_projects
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION debox_market_collection_lifecycle_changed();
CREATE CONSTRAINT TRIGGER market_project_deployments_collection_lifecycle
AFTER INSERT OR DELETE OR UPDATE OF status,market_project_id,market_asset_deployment_id ON market_project_deployments
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION debox_market_collection_lifecycle_changed();
CREATE CONSTRAINT TRIGGER market_asset_deployments_collection_lifecycle
AFTER DELETE OR UPDATE OF chain_id ON market_asset_deployments
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION debox_market_collection_lifecycle_changed();

-- A legacy cursor can precede all surviving proof of target membership. Keep
-- that prefix and its independent review position when live collection resumes.
CREATE TABLE IF NOT EXISTS market_collection_history_ranges (
    id BIGSERIAL PRIMARY KEY,
    chain_id BIGINT NOT NULL REFERENCES market_chain_collection_states(chain_id),
    cursor_key TEXT NOT NULL,
    idle_interval_id BIGINT NOT NULL REFERENCES market_collection_idle_intervals(id),
    from_block BIGINT NOT NULL CHECK (from_block > 0),
    through_block BIGINT NOT NULL CHECK (through_block >= from_block),
    from_at TIMESTAMPTZ NOT NULL,
    through_at TIMESTAMPTZ NOT NULL CHECK (through_at >= from_at),
    predecessor_hash TEXT NOT NULL CHECK (predecessor_hash ~ '^0x[0-9a-f]{64}$'),
    through_hash TEXT NOT NULL CHECK (through_hash ~ '^0x[0-9a-f]{64}$'),
    next_review_block BIGINT NOT NULL CHECK (next_review_block BETWEEN from_block AND through_block+1),
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(chain_id,cursor_key,idle_interval_id)
);

CREATE TABLE IF NOT EXISTS market_collection_history_reviews (
    range_id BIGINT NOT NULL REFERENCES market_collection_history_ranges(id),
    from_block BIGINT NOT NULL,
    through_block BIGINT NOT NULL CHECK (through_block >= from_block),
    scope_id BIGINT NOT NULL REFERENCES market_trade_scopes(id),
    scope_evidence TEXT NOT NULL CHECK (length(btrim(scope_evidence)) BETWEEN 1 AND 1000),
    -- Coverage records can coalesce or expire. Keep the checked anchors.
    predecessor_hash TEXT NOT NULL,
    through_hash TEXT NOT NULL,
    reviewed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(range_id,from_block)
);

-- Operator-only historical review. The caller establishes the actual
-- historical token and pool scope for this slice independently and identifies
-- its evidence. A current project, a high live cursor, or any individual
-- receipt is insufficient. A verified scan must already cover the range.
-- Callers can read scopes in market_trade_scopes, open work in
-- market_collection_history_ranges and prior receipts of review below.
CREATE OR REPLACE FUNCTION debox_review_collection_history(
    requested_range BIGINT, expected_from BIGINT, requested_through BIGINT,
    historical_scope BIGINT, evidence TEXT
) RETURNS BIGINT LANGUAGE plpgsql AS $$
DECLARE
    work market_collection_history_ranges%ROWTYPE;
    old_review market_collection_history_reviews%ROWTYPE;
    expected_hash TEXT;
    observed_predecessor TEXT;
    observed_end TEXT;
BEGIN
    IF requested_range IS NULL OR expected_from IS NULL OR requested_through IS NULL
       OR historical_scope IS NULL OR evidence IS NULL OR length(btrim(evidence)) NOT BETWEEN 1 AND 1000 THEN
        RAISE EXCEPTION 'historical range, scope and evidence must be supplied';
    END IF;
    SELECT * INTO STRICT work FROM market_collection_history_ranges WHERE id=requested_range FOR UPDATE;
    IF expected_from < work.next_review_block THEN
        SELECT * INTO old_review FROM market_collection_history_reviews WHERE range_id=requested_range AND from_block=expected_from;
        IF FOUND AND old_review.through_block=requested_through AND old_review.scope_id=historical_scope AND old_review.scope_evidence=btrim(evidence) THEN
            RETURN work.next_review_block;
        END IF;
        RAISE EXCEPTION 'conflicting historical review';
    END IF;
    IF expected_from<>work.next_review_block OR requested_through NOT BETWEEN expected_from AND work.through_block
       OR requested_through-expected_from >= 1000 THEN
        RAISE EXCEPTION 'review must start at the open position and cover at most 1000 blocks';
    END IF;
    IF expected_from=work.from_block THEN
        expected_hash:=work.predecessor_hash;
    ELSE
        SELECT through_hash INTO STRICT expected_hash FROM market_collection_history_reviews
        WHERE range_id=requested_range AND through_block=expected_from-1;
    END IF;

    -- The original live or an explicitly reviewed replay cursor can supply
    -- coverage, but only for the verified historical filter and canonical chain.
    SELECT pb.block_hash,eb.block_hash INTO observed_predecessor,observed_end
    FROM market_scan_coverage c
    JOIN market_trade_scopes scope ON scope.id=c.scope_id AND scope.parser_version=1
    JOIN market_scanned_blocks ca ON ca.chain_id=c.chain_id AND ca.cursor_key=c.cursor_key AND ca.canonical=1
        AND ca.block_number=c.from_block-1 AND ca.block_hash=c.after_hash
    JOIN market_scanned_blocks ce ON ce.chain_id=c.chain_id AND ce.cursor_key=c.cursor_key AND ce.canonical=1
        AND ce.block_number=c.through_block AND ce.block_hash=c.through_hash
    JOIN market_scanned_blocks pb ON pb.chain_id=c.chain_id AND pb.cursor_key=c.cursor_key AND pb.canonical=1
        AND pb.block_number=expected_from-1 AND pb.block_hash=expected_hash
    JOIN market_scanned_blocks eb ON eb.chain_id=c.chain_id AND eb.cursor_key=c.cursor_key AND eb.canonical=1
        AND eb.block_number=requested_through
    WHERE c.chain_id=work.chain_id AND c.scope_id=historical_scope AND c.from_block<=expected_from
      AND c.through_block>=requested_through AND pb.block_timestamp>=c.facts_retained_after
      AND (requested_through<work.through_block OR eb.block_hash=work.through_hash)
    LIMIT 1 FOR SHARE OF c,ca,ce,pb,eb;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'missing retained canonical scan evidence for the historical scope and range';
    END IF;
    INSERT INTO market_collection_history_reviews(range_id,from_block,through_block,scope_id,scope_evidence,predecessor_hash,through_hash)
    VALUES(work.id,expected_from,requested_through,historical_scope,btrim(evidence),observed_predecessor,observed_end);
    UPDATE market_collection_history_ranges SET next_review_block=requested_through+1 WHERE id=work.id;
    RETURN requested_through+1;
END
$$;
CREATE UNIQUE INDEX IF NOT EXISTS idx_market_history_review_end ON market_collection_history_reviews(range_id,through_block);
