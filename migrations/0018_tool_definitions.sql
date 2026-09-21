-- +goose Up
-- Create tool_definitions table (tenanted, RLS FORCE, persistent tool registry with hash-based integrity)

CREATE TABLE IF NOT EXISTS public.tool_definitions (
    id              bigserial NOT NULL,
    tenant_id       uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    name            varchar(255) NOT NULL,
    description     text NOT NULL DEFAULT '',
    input_schema    jsonb NOT NULL DEFAULT '{}',
    grants          jsonb NOT NULL DEFAULT '[]',
    fuel_limit      bigint NOT NULL DEFAULT 10000000,
    memory_pages    int NOT NULL DEFAULT 512,
    hash            char(64) NOT NULL,
    is_active       boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, tenant_id)
);

-- Unique constraint: one active tool definition per tenant per name
CREATE UNIQUE INDEX IF NOT EXISTS uq_tool_definitions_tenant_name_active
    ON public.tool_definitions (tenant_id, name)
    WHERE is_active = true;

-- Index for tenant queries
CREATE INDEX IF NOT EXISTS idx_tool_definitions_tenant_active ON public.tool_definitions (tenant_id, is_active);

-- Enable Row Level Security
ALTER TABLE public.tool_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.tool_definitions FORCE ROW LEVEL SECURITY;

-- RLS Policy: tool_definitions can only be seen within their tenant
CREATE POLICY tool_definitions_tenant_isolation ON public.tool_definitions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Revoke UPDATE and DELETE on the table directly (use repository methods for modifications)
-- Application role will be granted SELECT, INSERT, UPDATE, DELETE via repository

COMMENT ON TABLE public.tool_definitions IS 'Persistent tool definitions with SHA-256 hash-based integrity validation (tenanted, RLS FORCE)';
COMMENT ON COLUMN public.tool_definitions.name IS 'Tool identifier (unique per tenant when active)';
COMMENT ON COLUMN public.tool_definitions.input_schema IS 'JSON Schema for tool parameters (canonicalized for hashing)';
COMMENT ON COLUMN public.tool_definitions.grants IS 'Array of grant strings (e.g., ["filesystem:read", "network:egress"])';
COMMENT ON COLUMN public.tool_definitions.fuel_limit IS 'Maximum WASM fuel units per execution (0 = use config default)';
COMMENT ON COLUMN public.tool_definitions.memory_pages IS 'Maximum WASM memory pages per execution (64KB each, 0 = use config default)';
COMMENT ON COLUMN public.tool_definitions.hash IS 'SHA-256 lowercase hex (64 chars) of canonical JSON: name, description, parameters';

-- +goose Down
-- Drop tool_definitions table

DROP TABLE IF EXISTS public.tool_definitions;
