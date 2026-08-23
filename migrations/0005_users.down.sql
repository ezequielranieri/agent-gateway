-- 0005_users.down.sql
-- Drop users table

DROP POLICY IF EXISTS users_tenant_isolation ON public.users;
DROP TABLE IF EXISTS public.users;