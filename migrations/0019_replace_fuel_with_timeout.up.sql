-- +goose Up
-- Migration 0019: Replace fuel_limit with execution_timeout_ms in tool_definitions
-- This migration replaces the fuel_limit column with execution_timeout_ms

-- Add execution_timeout_ms column with CHECK constraint
ALTER TABLE public.tool_definitions 
ADD COLUMN execution_timeout_ms bigint NOT NULL DEFAULT 30000
CONSTRAINT chk_execution_timeout_ms CHECK (execution_timeout_ms > 0 AND execution_timeout_ms <= 300000);

-- Update existing rows: convert fuel_limit to approximate timeout (rough conversion)
-- 10M fuel ≈ 30 seconds, so timeout_ms = fuel_limit * 3 / 1000000
-- But since we're dropping fuel, just use default
UPDATE public.tool_definitions SET execution_timeout_ms = 30000;

-- Drop fuel_limit column
ALTER TABLE public.tool_definitions DROP COLUMN fuel_limit;

-- +goose Down
-- Rollback: Re-add fuel_limit, drop execution_timeout_ms

-- Add fuel_limit column back
ALTER TABLE public.tool_definitions 
ADD COLUMN fuel_limit bigint NOT NULL DEFAULT 10000000;

-- Approximate reverse conversion: fuel_limit = timeout_ms * 1000000 / 3
UPDATE public.tool_definitions SET fuel_limit = execution_timeout_ms * 1000000 / 3;

-- Drop execution_timeout_ms column
ALTER TABLE public.tool_definitions DROP COLUMN execution_timeout_ms;