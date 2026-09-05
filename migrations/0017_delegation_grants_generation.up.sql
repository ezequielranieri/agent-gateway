-- +goose Up
-- Add generation column to delegation_grants for chain revocation tracking.
-- Generation is set at issuance time and compared against the current chain
-- generation at each authorization boundary. Grants issued before a revocation
-- have a lower generation and are rejected.

ALTER TABLE public.delegation_grants
    ADD COLUMN IF NOT EXISTS generation integer NOT NULL DEFAULT 0;

COMMENT ON COLUMN public.delegation_grants.generation IS 'Chain version at issuance time; grants with generation < current chain generation are rejected (revoked)';

-- +goose Down
ALTER TABLE public.delegation_grants DROP COLUMN IF EXISTS generation;
