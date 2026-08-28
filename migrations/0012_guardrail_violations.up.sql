-- +goose Up
-- Create guardrail_violations table (tenanted, RLS FORCE)

CREATE TABLE IF NOT EXISTS public.guardrail_violations (
    id              uuid NOT NULL DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    request_id      uuid, -- Reference to the audit event or review request that triggered this
    direction       text NOT NULL CHECK (direction IN ('input', 'output')),
    rule_id         text NOT NULL, -- Identifier of the rule that triggered (e.g., "pii.email", "injection.pattern1")
    severity        text NOT NULL DEFAULT 'warn' CHECK (severity IN ('info', 'warn', 'critical')),
    payload_excerpt text NOT NULL, -- Redacted excerpt of the payload that triggered the violation
    metadata        jsonb, -- Additional context (matched pattern, position, etc.)
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, tenant_id)
);

-- Index for tenant queries
CREATE INDEX IF NOT EXISTS idx_guardrail_violations_tenant_created ON public.guardrail_violations (tenant_id, created_at DESC);
-- Index for rule queries
CREATE INDEX IF NOT EXISTS idx_guardrail_violations_rule ON public.guardrail_violations (tenant_id, rule_id);
-- Index for direction queries
CREATE INDEX IF NOT EXISTS idx_guardrail_violations_direction ON public.guardrail_violations (tenant_id, direction);
-- Index for request correlation
CREATE INDEX IF NOT EXISTS idx_guardrail_violations_request ON public.guardrail_violations (tenant_id, request_id);

-- Enable Row Level Security
ALTER TABLE public.guardrail_violations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.guardrail_violations FORCE ROW LEVEL SECURITY;

-- RLS Policy: guardrail_violations can only be seen within their tenant
CREATE POLICY guardrail_violations_tenant_isolation ON public.guardrail_violations
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

COMMENT ON TABLE public.guardrail_violations IS 'Guardrail violations (tenanted, RLS FORCE)';
COMMENT ON COLUMN public.guardrail_violations.direction IS 'Input (pre-router) or output (post-upstream)';
COMMENT ON COLUMN public.guardrail_violations.rule_id IS 'Rule identifier (e.g., pii.email, injection.pattern1)';


