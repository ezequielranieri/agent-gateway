-- +goose Up
-- Create audit_events table (tenanted, RLS FORCE, append-only with hash chain)

CREATE TABLE IF NOT EXISTS public.audit_events (
    id              uuid NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    seq             bigint NOT NULL, -- Per-tenant monotonic sequence number
    actor_type      text NOT NULL CHECK (actor_type IN ('user', 'system', 'super_admin')),
    actor_id        uuid, -- user_id or super_admin_id
    action          text NOT NULL, -- e.g., "model.call", "tool.exec", "auth.login", "review.approve"
    entity_type     text, -- e.g., "user", "role", "review", "chat_completion"
    entity_id       uuid,
    payload         jsonb NOT NULL, -- Canonical JSON payload
    severity        text NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'warn', 'critical')),
    prev_hash       bytea, -- SHA-256 hash of previous event in chain (NULL for first)
    hash            bytea NOT NULL, -- SHA-256(prev_hash || canonical(payload))
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, tenant_id)
);

-- Unique constraint: one event per tenant/seq (enforces no gaps)
CREATE UNIQUE INDEX IF NOT EXISTS uq_audit_events_tenant_seq
    ON public.audit_events (tenant_id, seq);

-- Index for tenant queries
CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_created ON public.audit_events (tenant_id, created_at DESC);
-- Index for actor queries
CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON public.audit_events (tenant_id, actor_type, actor_id);
-- Index for action queries
CREATE INDEX IF NOT EXISTS idx_audit_events_action ON public.audit_events (tenant_id, action);
-- Index for entity queries
CREATE INDEX IF NOT EXISTS idx_audit_events_entity ON public.audit_events (tenant_id, entity_type, entity_id);

-- Enable Row Level Security
ALTER TABLE public.audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.audit_events FORCE ROW LEVEL SECURITY;

-- RLS Policy: audit_events can only be seen within their tenant
CREATE POLICY audit_events_tenant_isolation ON public.audit_events
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Revoke UPDATE and DELETE to enforce append-only
REVOKE UPDATE, DELETE ON public.audit_events FROM PUBLIC;
-- (Application role will be granted SELECT, INSERT only)

COMMENT ON TABLE public.audit_events IS 'Append-only audit log with hash chain per tenant (tenanted, RLS FORCE)';
COMMENT ON COLUMN public.audit_events.seq IS 'Per-tenant monotonic sequence number (dense, no gaps)';
COMMENT ON COLUMN public.audit_events.prev_hash IS 'SHA-256 hash of previous event; NULL for first event in chain';
COMMENT ON COLUMN public.audit_events.hash IS 'SHA-256(prev_hash || canonical_json(payload))';
COMMENT ON COLUMN public.audit_events.payload IS 'Canonical JSON (sorted keys, no whitespace) for deterministic hashing';


