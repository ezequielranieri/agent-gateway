# AD-008: Human-in-the-Loop — State Machine + SSE + Full Re-validation

**Status:** Accepted
**Date:** 2026-08-22
**Updated:** 2026-10-19 — Fixed cross-tenant token lookup; per-tenant expiry sweep; context handling for writes

## Context

Write actions by AI agents must require human approval. The approval must be auditable, replayable, and secure against TOCTOU.

## Decision

- Write actions never mutate directly: agent creates `ReviewRequest` `PENDING` with opaque token (32 random bytes, hex), SHA-256 stored
- Approve/reject: token presented → timing-safe compare → pending + non-expired (24h TTL) + RBAC (`admin`/`operator`)
- **Token lookup uses reviewer's tenant from auth context** — the opaque token is looked up within the reviewer's tenant using `WithTenantTx`. If no tenant in context (SuperAdmin) or not found in that tenant, returns `ErrInvalidToken` without distinguishing cases. No cross-tenant query.
- On approve: **full re-validation** — payload re-parsed with `DisallowUnknownFields`, resource IDs resolved inside tenant → materialize → mark `EXECUTED`
- **ExpirePending sweeps per-tenant** — reads active tenant IDs from global `tenants` table, then sweeps each tenant within its own `WithTenantTx` transaction using `SweepExpiredReviewsCount` (returns affected rows). Errors are aggregated; sweep continues for remaining tenants.
- Audit: fail-open (WARN only)
- SSE streaming (`net/http` native) for real-time status — simpler than WebSockets, works with `curl -N`
- **Database operations for writes that must complete (UpdateStatus, MarkExecuted, audit Append) use `context.WithoutCancel(ctx)`** to survive client disconnect. Reads use request context.

## Consequences

- No "approved but not executed" state yet (future: deferred worker)
- SSE is unidirectional (server→client)
- Token lookup is now strictly tenant-scoped; SuperAdmin cannot approve/reject without explicit tenant context
- Expiry sweep is per-tenant and uses the same RLS-enforced transactions as other operations
- Write operations survive client disconnect via `context.WithoutCancel`