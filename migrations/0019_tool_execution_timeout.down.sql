-- +goose Down
-- Migration 0019: Rollback - Drop execution_timeout_ms, restore fuel_limit

-- Add back fuel_limit column
ALTER TABLE public.tool_definitions 
    ADD COLUMN IF NOT EXISTS fuel_limit bigint NOT NULL DEFAULT 10000000;

-- Migrate execution_timeout_ms back to fuel_limit (approximate)
UPDATE public.tool_definitions 
SET fuel_limit = CASE 
    WHEN execution_timeout_ms <= 5000 THEN 1000000
    WHEN execution_timeout_ms <= 15000 THEN 5000000
    WHEN execution_timeout_ms <= 30000 THEN 10000000
    WHEN execution_timeout_ms <= 60000 THEN 50000000
    ELSE 100000000
END
WHERE fuel_limit = 10000000; -- Only update rows with default value

-- Drop CHECK constraint
ALTER TABLE public.tool_definitions 
    DROP CONSTRAINT IF EXISTS chk_execution_timeout_ms_range;

-- Drop execution_timeout_ms column
ALTER TABLE public.tool_definitions 
    DROP COLUMN IF EXISTS execution_timeout_ms;