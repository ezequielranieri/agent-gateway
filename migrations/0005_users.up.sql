-- +goose Up
-- Create users table (tenanted, RLS FORCE)

CREATE TABLE IF NOT EXISTS public.users (
    id           uuid NOT NULL DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    email        citext NOT NULL,
    password_hash text   NOT NULL,
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'deleted')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, tenant_id)
);

-- Index for email lookups within a tenant
CREATE INDEX IF NOT EXISTS idx_users_tenant_email ON public.users (tenant_id, email);
-- Index for status queries
CREATE INDEX IF NOT EXISTS idx_users_tenant_status ON public.users (tenant_id, status);

-- Enable Row Level Security
ALTER TABLE public.users ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.users FORCE ROW LEVEL SECURITY;

-- RLS Policy: users can only see/modify rows for their tenant
CREATE POLICY users_tenant_isolation ON public.users
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMENT ON TABLE public.users IS 'Users within a tenant (tenanted, RLS FORCE)';
COMMENT ON COLUMN public.users.password_hash IS 'Argon2id PHC-encoded hash';
COMMENT ON COLUMN public.users.status IS 'User status: active, suspended, or deleted';


