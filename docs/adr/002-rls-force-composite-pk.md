# AD-002: RLS FORCE + Composite PK as Tenancy Mechanism

**Status:** Accepted
**Date:** 2026-08-22

## Context

Multi-tenant isolation must be structural — surviving buggy queries, broken repository layers, and raw `psql` sessions. Application-layer filtering alone is insufficient.

## Decision

Enforce tenancy at the database level using PostgreSQL Row Level Security with `FORCE`:

- Every tenanted table carries `tenant_id` and has RLS enabled with `FORCE`
- Policy: `USING (tenant_id = current_setting('app.tenant_id')::uuid) WITH CHECK (...)`
- Global tables (`public.tenants`, `public.roles`) have no `tenant_id` and no RLS
- Tenanted tables use **composite primary keys `(id, tenant_id)`**
- Application binds tenant context per transaction: `SELECT set_config('app.tenant_id', $1, true)` inside a dedicated transaction (the `true` makes it LOCAL)

## Consequences

- Zero-row result when tenant context missing (NULL predicate = always false) — never a leak
- Postgres-only mechanism locks in database choice
- Every tenant-scoped unit of work pays transaction cost
- Foreign keys to tenanted tables must repeat composite pair
- Migration/seeding operations touching all tenants need elevated privileges