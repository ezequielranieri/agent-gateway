-- +goose Up
-- Create audit_chain_locks table for serializing chain appends per tenant
-- This replaces advisory locks with a table-based lock that works correctly
-- with connection pooling (using SELECT ... FOR UPDATE)

CREATE TABLE IF NOT EXISTS public.audit_chain_locks (
    tenant_id   uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    locked_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id)
);

-- Enable Row Level Security
ALTER TABLE public.audit_chain_locks ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.audit_chain_locks FORCE ROW LEVEL SECURITY;

-- RLS Policy: locks can only be accessed within their tenant
CREATE POLICY audit_chain_locks_tenant_isolation ON public.audit_chain_locks
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

COMMENT ON TABLE public.audit_chain_locks IS 'Per-tenant locks for serializing audit chain appends (tenanted, RLS FORCE)';

-- +goose Down
-- Drop policy and table
DROP POLICY IF EXISTS audit_chain_locks_tenant_isolation ON public.audit_chain_locks;
DROP TABLE IF EXISTS public.audit_chain_locks;