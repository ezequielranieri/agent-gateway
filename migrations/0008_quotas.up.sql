-- +goose Up
-- Create quotas table (tenanted, RLS FORCE)

CREATE TABLE IF NOT EXISTS public.quotas (
    id                uuid NOT NULL DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    scope             text NOT NULL CHECK (scope IN ('tenant', 'user', 'role')),
    scope_id          uuid NOT NULL, -- user_id or role_id when scope is 'user' or 'role'; for 'tenant' scope, equals tenant_id
    requests_per_min  int NOT NULL DEFAULT 60,
    tokens_per_min    int NOT NULL DEFAULT 10000,
    tool_execs_per_min int NOT NULL DEFAULT 30,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, tenant_id)
);

-- Unique constraint: one quota per tenant/scope/scope_id
CREATE UNIQUE INDEX IF NOT EXISTS uq_quotas_tenant_scope_scope_id
    ON public.quotas (tenant_id, scope, scope_id);

-- Index for quota lookups by tenant
CREATE INDEX IF NOT EXISTS idx_quotas_tenant ON public.quotas (tenant_id);

-- Enable Row Level Security
ALTER TABLE public.quotas ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.quotas FORCE ROW LEVEL SECURITY;

-- RLS Policy: quotas can only be seen/modified within their tenant
CREATE POLICY quotas_tenant_isolation ON public.quotas
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMENT ON TABLE public.quotas IS 'Rate limit quotas per tenant/user/role (tenanted, RLS FORCE)';
COMMENT ON COLUMN public.quotas.scope IS 'Quota scope: tenant, user, or role';
COMMENT ON COLUMN public.quotas.scope_id IS 'Reference ID: user_id for user scope, role_id for role scope, tenant_id for tenant scope';


