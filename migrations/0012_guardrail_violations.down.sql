-- 0012_guardrail_violations.down.sql
-- Drop guardrail_violations table

DROP POLICY IF EXISTS guardrail_violations_tenant_isolation ON public.guardrail_violations;
DROP TABLE IF EXISTS public.guardrail_violations;