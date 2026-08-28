-- name: CreateGuardrailViolation :one
INSERT INTO public.guardrail_violations (tenant_id, request_id, direction, rule_id, severity, payload_excerpt, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, tenant_id, request_id, direction, rule_id, severity, payload_excerpt, metadata, created_at;

-- name: ListGuardrailViolations :many
SELECT id, tenant_id, request_id, direction, rule_id, severity, payload_excerpt, metadata, created_at
FROM public.guardrail_violations
WHERE tenant_id = $1
  AND ($2 = '' OR direction = $2)
  AND ($3 = '' OR rule_id = $3)
  AND ($4 = '' OR severity = $4)
  AND ($5 = '00000000-0000-0000-0000-000000000000'::uuid OR request_id = $5)
ORDER BY created_at DESC
LIMIT $6 OFFSET $7;