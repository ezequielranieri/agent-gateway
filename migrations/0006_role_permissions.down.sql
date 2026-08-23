-- 0006_role_permissions.down.sql
-- Drop role_permissions table

DROP POLICY IF EXISTS role_permissions_tenant_isolation ON public.role_permissions;
DROP TABLE IF EXISTS public.role_permissions;