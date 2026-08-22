# AD-003: JWT HS256 + Multi-Key Rotation (kid)

**Status:** Accepted
**Date:** 2026-08-22

## Context

Access tokens need to be short-lived, stateless, and support key rotation without downtime.

## Decision

- Access tokens: JWT HS256, 15-minute TTL, claims: `sub`, `tenant_id`, `role`, `scopes`
- Multiple signing keys active for verification simultaneously, identified by `kid`
- Exactly one key used to *sign* new tokens; all known keys accepted for *verification*
- Algorithm pinned to HS256 — verification rejects any other method (closes `alg=none`, key-confusion)

## Consequences

- Symmetric secret: leaked `JWT_SECRET` forges tokens for any tenant → secret rotation schedule required
- Tokens cannot be revoked before expiry (mitigated by 15-min TTL)
- Multi-service verification requires shared secret
- Path to RS256/JWKS documented for future