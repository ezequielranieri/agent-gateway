# AD-004: Refresh Tokens — Opaque + Rotation + Family Reuse Detection

**Status:** Accepted
**Date:** 2026-08-22

## Context

Refresh tokens are long-lived credentials. If stolen, they must be detectable and revocable. Simple rotation is insufficient — an attacker racing the legitimate user must lose.

## Decision

- Refresh tokens: opaque 256-bit random (base64url), returned once, stored only as SHA-256 digest
- Rotation keeps per-family chain: every refresh revokes old token, issues successor in same `family_id`, records `replaced_by`
- Presenting a rotated token (revoked + `replaced_by` set) = replay signature → revokes **entire family** (`RevokeFamily`)
- Revoked-vs-expired check order: revoked+expired → reported as reuse, not "expired"
- Rotation order (fail-safe): 1) persist new token, 2) revoke old token, 3) issue new access token

## Consequences

- Rotation adds concurrency-sensitive writes
- Lost/untransmitted successor forces re-login
- Must distinguish Expired from Revoked in replay logic