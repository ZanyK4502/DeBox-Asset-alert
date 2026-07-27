ALTER TABLE webhook_inbox
    ADD COLUMN IF NOT EXISTS chain_key TEXT NOT NULL DEFAULT 'bsc',
    ADD COLUMN IF NOT EXISTS chain_id BIGINT NOT NULL DEFAULT 56;

UPDATE webhook_inbox inbox
SET chain_key = subscription.chain_key,
    chain_id = subscription.chain_id
FROM nodit_webhook_subscriptions subscription
WHERE inbox.webhook_subscription_id = subscription.id
  AND (
      inbox.chain_key IS DISTINCT FROM subscription.chain_key
      OR inbox.chain_id IS DISTINCT FROM subscription.chain_id
  );

ALTER TABLE webhook_inbox
    DROP CONSTRAINT IF EXISTS webhook_inbox_chain_key_check,
    DROP CONSTRAINT IF EXISTS webhook_inbox_chain_id_check;

ALTER TABLE webhook_inbox
    ADD CONSTRAINT webhook_inbox_chain_key_check CHECK (chain_key <> ''),
    ADD CONSTRAINT webhook_inbox_chain_id_check CHECK (chain_id > 0);

CREATE INDEX IF NOT EXISTS idx_webhook_inbox_chain_pending
    ON webhook_inbox (chain_id, processing_status, next_attempt_at, id);

CREATE INDEX IF NOT EXISTS idx_webhook_inbox_chain_processing
    ON webhook_inbox (chain_id, processing_status, locked_at)
    WHERE processing_status = 'processing';
