-- 0013_seed.down.sql
-- Remove seed data

DROP FUNCTION IF EXISTS bootstrap_super_admin(citext, text);
DELETE FROM public.roles WHERE name IN ('admin', 'operator', 'viewer');