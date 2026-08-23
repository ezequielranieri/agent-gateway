-- 0008_quotas.down.sql
-- Drop quotas table

DROP POLICY IF EXISTS quotas_tenant_isolation ON public.quotas;
DROP TABLE IF EXISTS public.quotas;