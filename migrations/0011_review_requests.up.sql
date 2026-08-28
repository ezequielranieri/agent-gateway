-- +goose Up
-- Create review_requests table (tenanted, RLS FORCE)

CREATE TABLE IF NOT EXISTS public.review_requests (
    id              uuid NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    requester_id    uuid NOT NULL, -- user_id who requested the review
    reviewer_id     uuid, -- user_id who will review (can be NULL for any admin)
    action          text NOT NULL, -- e.g., "tool:execute", "model:call"
    payload         jsonb NOT NULL, -- The request data needing human approval
    status          text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'EXPIRED', 'EXECUTED')),
    token_hash      bytea NOT NULL, -- SHA-256 hash of the opaque review token (returned once to requester)
    expires_at      timestamptz NOT NULL,
    decided_at      timestamptz,
    decided_by      uuid, -- user_id who approved/rejected
    decision_reason text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, tenant_id)
);

-- Index for token hash lookups (used for approve/reject)
CREATE INDEX IF NOT EXISTS idx_review_requests_token_hash ON public.review_requests (token_hash);
-- Index for pending reviews by tenant
CREATE INDEX IF NOT EXISTS idx_review_requests_tenant_status ON public.review_requests (tenant_id, status);
-- Index for requester queries
CREATE INDEX IF NOT EXISTS idx_review_requests_requester ON public.review_requests (tenant_id, requester_id);
-- Index for reviewer queries
CREATE INDEX IF NOT EXISTS idx_review_requests_reviewer ON public.review_requests (tenant_id, reviewer_id);

-- Enable Row Level Security
ALTER TABLE public.review_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.review_requests FORCE ROW LEVEL SECURITY;

-- RLS Policy: review_requests can only be seen/modified within their tenant
CREATE POLICY review_requests_tenant_isolation ON public.review_requests
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMENT ON TABLE public.review_requests IS 'Human-in-the-Loop review requests (tenanted, RLS FORCE)';
COMMENT ON COLUMN public.review_requests.token_hash IS 'SHA-256 hash of opaque token (returned once); timing-safe compare on approve/reject';
COMMENT ON COLUMN public.review_requests.status IS 'State machine: PENDING -> APPROVED | REJECTED | EXPIRED (no other transitions)';


