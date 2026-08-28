-- +goose Up
-- Create user_roles join table (tenanted, RLS FORCE)

CREATE TABLE IF NOT EXISTS public.user_roles (
    user_id       uuid NOT NULL,
    role_id       uuid NOT NULL REFERENCES public.roles(id) ON DELETE CASCADE,
    tenant_id     uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id, tenant_id)
);

-- Foreign key to users (composite PK reference)
ALTER TABLE public.user_roles
    ADD CONSTRAINT fk_user_roles_user
    FOREIGN KEY (user_id, tenant_id)
    REFERENCES public.users (id, tenant_id)
    ON DELETE CASCADE;

-- Index for role lookups by tenant
CREATE INDEX IF NOT EXISTS idx_user_roles_tenant ON public.user_roles (tenant_id, user_id);
-- Index for user lookups by role
CREATE INDEX IF NOT EXISTS idx_user_roles_role ON public.user_roles (tenant_id, role_id);

-- Enable Row Level Security
ALTER TABLE public.user_roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_roles FORCE ROW LEVEL SECURITY;

-- RLS Policy: user_roles can only be seen/modified within their tenant
CREATE POLICY user_roles_tenant_isolation ON public.user_roles
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMENT ON TABLE public.user_roles IS 'User-role assignments per tenant (tenanted, RLS FORCE)';


