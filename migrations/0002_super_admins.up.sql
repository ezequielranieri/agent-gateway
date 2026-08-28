-- +goose Up
-- Create super_admins table (global, NO RLS)

CREATE TABLE IF NOT EXISTS public.super_admins (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email        citext NOT NULL UNIQUE,
    password_hash text   NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Index for email lookups
CREATE INDEX IF NOT EXISTS idx_super_admins_email ON public.super_admins (email);

COMMENT ON TABLE public.super_admins IS 'Global Super Admin accounts (platform level, no tenant_id)';
COMMENT ON COLUMN public.super_admins.password_hash IS 'Argon2id PHC-encoded hash';


