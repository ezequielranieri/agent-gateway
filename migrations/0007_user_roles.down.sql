-- 0007_user_roles.down.sql
-- Drop user_roles table

DROP POLICY IF EXISTS user_roles_tenant_isolation ON public.user_roles;
ALTER TABLE public.user_roles DROP CONSTRAINT IF EXISTS fk_user_roles_user;
DROP TABLE IF EXISTS public.user_roles;