# AD-009: Guardrails — Domain Interface + Local Implementation

**Status:** Accepted
**Date:** 2026-08-22

## Context

Input/output validation must be extensible, testable, and not require external dependencies for the MVP.

## Decision

- `Guardrail` interface in `internal/domain/guardrail`: `CheckInput`, `CheckOutput`, `Violation` types
- `LocalGuardrail` ships by default: regex patterns, wordlists, PII detection (email, CC, SSN), prompt injection heuristics — zero network, zero API keys, in-process
- External classifiers (Claude API, Llama Guard) plug in as adapters implementing `Guardrail` — never in domain
- Input validation fails closed (reject); output validation fails closed (reject or sanitize by severity)
- Violations recorded in audit log with severity `critical`

## Consequences

- Local implementation catches common patterns, not semantic attacks (external adapter fills gap)
- Regex/wordlist maintenance is manual (accepted for portfolio scope)