-- Runtime activation is explicit lifecycle evidence, never a guessed MAX cursor.
ALTER TABLE market_rules ADD COLUMN IF NOT EXISTS trade_activation_after TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS market_project_trade_scope_changes (
 project_id BIGINT PRIMARY KEY REFERENCES market_projects(id) ON DELETE CASCADE,
 scope_fingerprint TEXT NOT NULL CHECK(length(scope_fingerprint)=64),
 selected_state JSONB NOT NULL DEFAULT '{}'::jsonb,
 pool_boundaries JSONB NOT NULL DEFAULT '{}'::jsonb,
 changed_at TIMESTAMPTZ NOT NULL,
 revision BIGINT NOT NULL DEFAULT 1 CHECK(revision>0)
);
CREATE TABLE IF NOT EXISTS market_trade_rule_readiness (
 rule_id BIGINT NOT NULL REFERENCES market_rules(id) ON DELETE CASCADE,
 target_key TEXT NOT NULL,
 scope_fingerprint TEXT NOT NULL CHECK(length(scope_fingerprint)=64),
 scope_source_fingerprint TEXT NOT NULL CHECK(length(scope_source_fingerprint)=64),
 scope_revision BIGINT NOT NULL DEFAULT 1 CHECK(scope_revision>0),
 eligibility_after TIMESTAMPTZ,
 activation_origin TEXT NOT NULL CHECK(activation_origin IN ('lifecycle','recovery_boundary','explicit_review')),
 latest_event_at TIMESTAMPTZ,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','warming','ready','blocked')),
 reason TEXT NOT NULL DEFAULT '',
 version BIGINT NOT NULL DEFAULT 1 CHECK(version>0),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(rule_id,target_key),
 FOREIGN KEY(rule_id,target_key) REFERENCES market_rule_target_states(market_rule_id,target_key) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS market_trade_warmup_periods (
 id BIGSERIAL PRIMARY KEY,
 rule_id BIGINT NOT NULL,
 target_key TEXT NOT NULL,
 scope_revision BIGINT NOT NULL CHECK(scope_revision>0),
 from_at TIMESTAMPTZ NOT NULL,
 through_at TIMESTAMPTZ NOT NULL,
 ready_from_at TIMESTAMPTZ,
 reason TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 CHECK(through_at>=from_at),
 CHECK(ready_from_at IS NULL OR ready_from_at>through_at),
 FOREIGN KEY(rule_id,target_key) REFERENCES market_trade_rule_readiness(rule_id,target_key) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS market_trade_warmup_lookup ON market_trade_warmup_periods(rule_id,target_key,scope_revision,from_at);
CREATE INDEX IF NOT EXISTS market_trade_warmup_open ON market_trade_warmup_periods(rule_id,target_key,scope_revision) WHERE ready_from_at IS NULL;
