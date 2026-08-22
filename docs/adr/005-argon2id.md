# AD-005: Argon2id Password Hashing

**Status:** Accepted
**Date:** 2026-08-22

## Context

Password storage must resist offline brute-force including ASIC/GPU attacks.

## Decision

- Argon2id with OWASP interactive params: `m=65536, t=1, p=4` (64 MiB, 1 iter, 4 lanes)
- 32-byte key, random 16-byte salt
- PHC format encoding: `$argon2id$v=19$m=65536,t=1,p=4$...`
- `Verify` re-derives parameters from stored string — never from config (transparent future bumps)

## Consequences

- 64 MiB per hash — more CPU/memory per login
- Not FIPS-approved in some compliance contexts
- Parameter change = migration with re-hashing