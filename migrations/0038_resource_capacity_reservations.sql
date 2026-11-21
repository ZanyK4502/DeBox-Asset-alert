-- Holds follow durable order/subscription state. In particular a pending
-- order's wall-clock expiry alone does not release a promised reservation.
CREATE TABLE IF NOT EXISTS resource_capacity_holds (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL UNIQUE REFERENCES orders(id) ON DELETE CASCADE,
    subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE CASCADE,
    debox_user_id TEXT NOT NULL CHECK (debox_user_id<>''),
    plan_code TEXT NOT NULL CHECK (plan_code IN ('standard','professional')),
    monthly_cu BIGINT NOT NULL CHECK (monthly_cu>0),
    plan_snapshot JSONB NOT NULL CHECK (jsonb_typeof(plan_snapshot)='object' AND octet_length(plan_snapshot::text)<=8192),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    linked_at TIMESTAMPTZ,
    CHECK ((subscription_id IS NULL AND linked_at IS NULL) OR (subscription_id IS NOT NULL AND linked_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS idx_resource_capacity_holds_subscription
    ON resource_capacity_holds(subscription_id) WHERE subscription_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_resource_capacity_holds_owner
    ON resource_capacity_holds(debox_user_id);
