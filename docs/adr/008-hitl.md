# AD-008: Human-in-the-Loop — State Machine + SSE + Full Re-validation

**Status:** Accepted
**Date:** 2026-08-22

## Context

Write actions by AI agents must require human approval. The approval must be auditable, replayable, and secure against TOCTOU.

## Decision

- Write actions never mutate directly: agent creates `ReviewRequest` `PENDING` with opaque token (32 random bytes, hex), SHA-256 stored
- Approve/reject: token presented → timing-safe compare → pending + non-expired (24h TTL) + RBAC (`admin`/`operator`)
- On approve: **full re-validation** — payload re-parsed with `DisallowUnknownFields`, resource IDs resolved inside tenant → materialize → mark `EXECUTED`
- Audit: fail-open (WARN only)
- SSE streaming (`net/http` native) for real-time status — simpler than WebSockets, works with `curl -N`

## Consequences

- No "approved but not executed" state yet (future: deferred worker)
- SSE is unidirectional (server→client)