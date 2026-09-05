-- +goose Up
-- Add chain_id and parent_event_id columns to audit_events for delegation tree linkage.
-- Additive-only migration: existing rows retain their data, new columns default to NULL.
-- Hash chain integrity is preserved because chain_id/parent_event_id are NOT included
-- in the hash input (hash covers prev_hash, seq, tenant_id, actor, action, entity, payload, created_at).

ALTER TABLE public.audit_events
    ADD COLUMN IF NOT EXISTS chain_id uuid DEFAULT NULL,
    ADD COLUMN IF NOT EXISTS parent_event_id uuid DEFAULT NULL;

-- Index for querying all events in a delegation chain
CREATE INDEX IF NOT EXISTS idx_audit_events_chain ON public.audit_events (tenant_id, chain_id) WHERE chain_id IS NOT NULL;

-- Index for parent event lookups (tree reconstruction)
CREATE INDEX IF NOT EXISTS idx_audit_events_parent_event ON public.audit_events (tenant_id, parent_event_id) WHERE parent_event_id IS NOT NULL;

-- Comments
COMMENT ON COLUMN public.audit_events.chain_id IS 'Links audit events to a delegation chain (NULL for non-delegation events)';
COMMENT ON COLUMN public.audit_events.parent_event_id IS 'Points to the parent agent authorization event (NULL for root events)';

-- +goose Down
DROP INDEX IF EXISTS idx_audit_events_parent_event;
DROP INDEX IF EXISTS idx_audit_events_chain;
ALTER TABLE public.audit_events DROP COLUMN IF EXISTS parent_event_id;
ALTER TABLE public.audit_events DROP COLUMN IF EXISTS chain_id;
