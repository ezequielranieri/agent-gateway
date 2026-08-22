-- 0003_tenants.up.sql
-- Create tenants table (global registry, NO RLS on this table)
-- RLS is applied on tenanted tables, not on the tenant registry itself

CREATE TABLE IF NOT EXISTS public.tenants (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE,
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Index for status queries
CREATE INDEX IF NOT EXISTS idx_tenants_status ON public.tenants (status);

COMMENT ON TABLE public.tenants IS 'Global tenant registry (platform level, no tenant_id, no RLS)';
COMMENT ON COLUMN public.tenants.status IS 'Tenant status: active or suspended';