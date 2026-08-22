# AD-006: Rate Limiting — Redis Token Bucket (3 Dimensions)

**Status:** Accepted
**Date:** 2026-08-22

## Context

Rate limiting must be granular (requests, tokens, tool executions), distributed, and not become a denial-of-service vector itself.

## Decision

- Token bucket via `redis_rate` (Redis + Lua, atomic)
- Three independent buckets per identity: requests/min, tokens/min, tool_execs/min
- Identity dimensions: per tenant, per user, per role (configurable precedence)
- Fail-open on Redis outage: nil client or Redis error → allow request + WARN log
- 429 with JSON error + `Retry-After` header

## Consequences

- In-memory fallback is per-process (lost on restart, wrong cross-instances)
- Three buckets per identity = more Redis keys (accepted for granularity)