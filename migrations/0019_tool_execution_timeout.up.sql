-- +goose Up
-- Migration 0019: Drop fuel_limit, add execution_timeout_ms for WASM resource limits
-- fuel_limit is replaced by execution_timeout_ms (milliseconds) for timeout-based enforcement

-- Add execution_timeout_ms column with constraints
ALTER TABLE public.tool_definitions 
    ADD COLUMN IF NOT EXISTS execution_timeout_ms bigint NOT NULL DEFAULT 30000;

-- Add CHECK constraint for execution_timeout_ms (1ms to 300000ms = 5 minutes)
ALTER TABLE public.tool_definitions 
    ADD CONSTRAINT chk_execution_timeout_ms_range 
    CHECK (execution_timeout_ms > 0 AND execution_timeout_ms <= 300000);

-- Migrate existing fuel_limit values to execution_timeout_ms
-- Approximate conversion: fuel units ~ instructions, but we use timeout-based enforcement
-- Default: 10,000,000 fuel -> 30,000ms (30 seconds)
-- We'll set a reasonable default based on fuel_limit buckets
UPDATE public.tool_definitions 
SET execution_timeout_ms = CASE 
    WHEN fuel_limit <= 1000000 THEN 5000      -- 5 seconds for low fuel
    WHEN fuel_limit <= 5000000 THEN 15000     -- 15 seconds for medium fuel
    WHEN fuel_limit <= 10000000 THEN 30000    -- 30 seconds for default fuel
    WHEN fuel_limit <= 50000000 THEN 60000    -- 60 seconds for high fuel
    ELSE 120000                               -- 120 seconds for very high fuel
END
WHERE execution_timeout_ms = 30000; -- Only update rows with default value

-- Drop fuel_limit column
ALTER TABLE public.tool_definitions 
    DROP COLUMN IF EXISTS fuel_limit;

-- Update column comment
COMMENT ON COLUMN public.tool_definitions.execution_timeout_ms IS 'Maximum execution timeout in milliseconds (1-300000). Replaces fuel_limit for timeout-based enforcement.';