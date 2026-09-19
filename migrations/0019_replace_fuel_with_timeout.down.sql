-- +goose Down
-- Rollback 0019: Restore fuel_limit, remove execution_timeout_ms

-- Add fuel_limit column back
ALTER TABLE public.tool_definitions 
ADD COLUMN fuel_limit bigint NOT NULL DEFAULT 10000000;

-- Approximate reverse conversion: fuel_limit = timeout_ms * 1000000 / 3
UPDATE public.tool_definitions SET fuel_limit = execution_timeout_ms * 1000000 / 3;

-- Drop execution_timeout_ms column
ALTER TABLE public.tool_definitions DROP COLUMN execution_timeout_ms;