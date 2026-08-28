-- name: CreateAuditEvent :one
WITH prev AS (
    SELECT hash
    FROM public.audit_events
    WHERE tenant_id = $1
    ORDER BY seq DESC
    LIMIT 1
),
new_seq AS (
    SELECT COALESCE(MAX(seq), 0) + 1 AS next_seq
    FROM public.audit_events
    WHERE tenant_id = $1
),
genesis AS (
    SELECT '\x0000000000000000000000000000000000000000000000000000000000000000'::bytea AS hash
)
INSERT INTO public.audit_events (tenant_id, seq, actor_type, actor_id, action, entity_type, entity_id, payload, severity, prev_hash, hash)
SELECT
    $1,
    ns.next_seq,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8,
    COALESCE(p.hash, g.hash),
    $9 -- pre-computed hash from application layer
FROM new_seq ns
CROSS JOIN genesis g
LEFT JOIN prev p ON true
RETURNING id, tenant_id, seq, actor_type, actor_id, action, entity_type, entity_id, payload, severity, prev_hash, hash, created_at;

-- name: GetAuditEvents :many
SELECT id, tenant_id, seq, actor_type, actor_id, action, entity_type, entity_id, payload, severity, prev_hash, hash, created_at
FROM public.audit_events
WHERE tenant_id = $1
  AND ($2::timestamptz IS NULL OR created_at >= $2)
  AND ($3::timestamptz IS NULL OR created_at <= $3)
  AND ($4::text IS NULL OR action = $4)
  AND ($5::text IS NULL OR actor_type = $5)
  AND ($6::uuid IS NULL OR actor_id = $6)
  AND ($7::text IS NULL OR entity_type = $7)
  AND ($8::uuid IS NULL OR entity_id = $8)
  AND ($9::text IS NULL OR severity = $9)
ORDER BY created_at DESC
LIMIT $10 OFFSET $11;

-- name: GetAuditEventBySeq :one
SELECT id, tenant_id, seq, actor_type, actor_id, action, entity_type, entity_id, payload, severity, prev_hash, hash, created_at
FROM public.audit_events
WHERE tenant_id = $1 AND seq = $2
LIMIT 1;