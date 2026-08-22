-- 0001_extensions.down.sql
-- Drop PostgreSQL extensions (if safe to do so)

-- Note: Only drop if no other objects depend on them
-- DROP EXTENSION IF EXISTS "uuid-ossp";
-- DROP EXTENSION IF EXISTS pgcrypto;