-- 0010_audit_events.down.sql
-- Drop audit_events table

DROP POLICY IF EXISTS audit_events_tenant_isolation ON public.audit_events;
DROP TABLE IF EXISTS public.audit_events;