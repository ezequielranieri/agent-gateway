# AD-007: Audit Log — Append-Only + Per-Tenant Hash Chaining

**Status:** Accepted
**Date:** 2026-08-22

## Context

Audit trail must be tamper-evident, queryable, and survive database compromise.

## Decision

- Hash-chained per tenant: every `private.audit_events` row has `seq`, `prev_hash`, `chain_hash` with `UNIQUE (tenant_id, seq)`
- Genesis: `seq=1`, `prev_hash` = 64 hex zeros
- `chain_hash` = SHA-256 over fixed field order: `prev_hash|seq|tenant_id|actor_user_id|action|entity_type|entity_id|payload|created_at`
- Payload = canonicalized JSON (decode → re-marshal), `created_at` truncated to microsecond precision
- Same code path for insert and verify → no drift
- `VerifyChain` internal: recomputes chain, reports first broken `seq`
- Audit appends inside tenant-bound transaction (RLS FORCE), fail-open (WARN only)

## Consequences

- One indexed tail read + insert per audit event (sync, write path)
- Verification O(n) — fine at portfolio volume
- Payloads must stay float64-safe for stable canonicalization