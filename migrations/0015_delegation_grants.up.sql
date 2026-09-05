-- +goose Up
-- Create delegation_grants table (tenanted, RLS FORCE, composite PK)
-- Stores delegation grant envelopes with full lifecycle tracking.

CREATE TABLE IF NOT EXISTS public.delegation_grants (
    id                    uuid NOT NULL DEFAULT gen_random_uuid(),
    tenant_id             uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    parent_grant_id       uuid, -- NULL for root grants
    chain_id              uuid NOT NULL, -- Groups all grants in a delegation tree
    delegate_identity     text NOT NULL, -- Agent or identity receiving the delegation
    granted_scope         jsonb NOT NULL, -- Sorted []string of scope strings
    root_intent           text NOT NULL, -- Immutable intent from the root action
    hitl_classification   text NOT NULL DEFAULT 'none' CHECK (hitl_classification IN ('none', 'optional', 'required')),
    depth                 integer NOT NULL DEFAULT 0 CHECK (depth >= 0),
    expires_at            timestamptz NOT NULL,
    budget_remaining      integer NOT NULL DEFAULT 1000 CHECK (budget_remaining >= 0),
    status                text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued', 'active', 'revoked', 'expired', 'consumed')),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, tenant_id)
);

-- Index for chain lookups (revocation, tree reconstruction)
CREATE INDEX IF NOT EXISTS idx_delegation_grants_chain ON public.delegation_grants (tenant_id, chain_id);

-- Index for active grants by delegate
CREATE INDEX IF NOT EXISTS idx_delegation_grants_delegate ON public.delegation_grants (tenant_id, delegate_identity, status);

-- Index for parent grant lookups (child enumeration)
CREATE INDEX IF NOT EXISTS idx_delegation_grants_parent ON public.delegation_grants (tenant_id, parent_grant_id) WHERE parent_grant_id IS NOT NULL;

-- Index for TTL expiry checks
CREATE INDEX IF NOT EXISTS idx_delegation_grants_expires ON public.delegation_grants (tenant_id, expires_at) WHERE status IN ('issued', 'active');

-- Enable Row Level Security
ALTER TABLE public.delegation_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.delegation_grants FORCE ROW LEVEL SECURITY;

-- RLS Policy: delegation_grants can only be accessed within their tenant
CREATE POLICY delegation_grants_tenant_isolation ON public.delegation_grants
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Comments
COMMENT ON TABLE public.delegation_grants IS 'Delegation grant envelopes with lifecycle tracking (tenanted, RLS FORCE)';
COMMENT ON COLUMN public.delegation_grants.chain_id IS 'UUID grouping all grants in a single delegation tree';
COMMENT ON COLUMN public.delegation_grants.parent_grant_id IS 'Parent grant UUID; NULL for root grants';
COMMENT ON COLUMN public.delegation_grants.granted_scope IS 'JSON array of sorted scope strings granted to delegate';
COMMENT ON COLUMN public.delegation_grants.root_intent IS 'Immutable intent string from the root action (never repackaged)';
COMMENT ON COLUMN public.delegation_grants.hitl_classification IS 'HITL classification inherited from root: none, optional, or required';
COMMENT ON COLUMN public.delegation_grants.depth IS 'Delegation depth from root (0 = root grant)';
COMMENT ON COLUMN public.delegation_grants.budget_remaining IS 'Inherited budget pool, decremented per hop';
COMMENT ON COLUMN public.delegation_grants.status IS 'Grant lifecycle: issued, active, revoked, expired, or consumed';

-- +goose Down
DROP POLICY IF EXISTS delegation_grants_tenant_isolation ON public.delegation_grants;
ALTER TABLE public.delegation_grants DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS public.delegation_grants;
