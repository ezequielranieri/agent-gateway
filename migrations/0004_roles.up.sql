-- +goose Up
-- Create roles table (global catalog, NO RLS per AD-002)

CREATE TABLE IF NOT EXISTS public.roles (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE,
    description  text,
    created_at   timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.roles IS 'Global role catalog (shared across all tenants, no tenant_id, no RLS)';
COMMENT ON COLUMN public.roles.name IS 'Role name (e.g., admin, operator, viewer)';


