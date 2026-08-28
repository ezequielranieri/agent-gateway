-- +goose Up
-- Create role_permissions join table (tenanted, RLS FORCE)

CREATE TABLE IF NOT EXISTS public.role_permissions (
    role_id       uuid NOT NULL REFERENCES public.roles(id) ON DELETE CASCADE,
    tenant_id     uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    permission    text NOT NULL, -- Format: recurso:accion (e.g., "users:read", "chat:write")
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, tenant_id, permission)
);

-- Index for permission lookups by tenant
CREATE INDEX IF NOT EXISTS idx_role_permissions_tenant ON public.role_permissions (tenant_id, role_id);
-- Index for permission lookups
CREATE INDEX IF NOT EXISTS idx_role_permissions_permission ON public.role_permissions (tenant_id, permission);

-- Enable Row Level Security
ALTER TABLE public.role_permissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.role_permissions FORCE ROW LEVEL SECURITY;

-- RLS Policy: role_permissions can only be seen/modified within their tenant
CREATE POLICY role_permissions_tenant_isolation ON public.role_permissions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMENT ON TABLE public.role_permissions IS 'Role-permission assignments per tenant (tenanted, RLS FORCE)';
COMMENT ON COLUMN public.role_permissions.permission IS 'Permission in format recurso:accion (e.g., users:read, chat:write)';


