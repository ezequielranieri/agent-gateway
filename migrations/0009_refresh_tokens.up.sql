-- 0009_refresh_tokens.up.sql
-- Create refresh_tokens table (GLOBAL, NO RLS per AD-009)
-- Refresh tokens are global to support cross-tenant operations and family tracking

CREATE TABLE IF NOT EXISTS public.refresh_tokens (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    tenant_id       uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    token_hash      bytea NOT NULL, -- SHA-256 hash of the raw refresh token
    family_id       uuid NOT NULL, -- Groups related tokens for rotation/reuse detection
    revoked         boolean NOT NULL DEFAULT false,
    expires_at      timestamptz NOT NULL,
    user_agent      text,
    ip              inet,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_used_at    timestamptz,
    rotated_from    uuid REFERENCES public.refresh_tokens(id) ON DELETE SET NULL
);

-- Index for token hash lookups (used for rotation)
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_hash ON public.refresh_tokens (token_hash);
-- Index for family lookups (used for reuse detection)
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_family ON public.refresh_tokens (family_id);
-- Index for user's active tokens
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_active ON public.refresh_tokens (user_id, revoked, expires_at)
    WHERE revoked = false AND expires_at > now();
-- Index for tenant's tokens (admin queries)
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_tenant ON public.refresh_tokens (tenant_id, user_id);

COMMENT ON TABLE public.refresh_tokens IS 'Refresh tokens (global, NO RLS) - family_id enables rotation & reuse detection';
COMMENT ON COLUMN public.refresh_tokens.token_hash IS 'SHA-256 hash of the raw refresh token (never stored in plaintext)';
COMMENT ON COLUMN public.refresh_tokens.family_id IS 'Groups rotated tokens; reuse detection revokes entire family';
COMMENT ON COLUMN public.refresh_tokens.rotated_from IS 'Points to the previous token in the rotation chain';