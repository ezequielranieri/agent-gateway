-- name: CreateQuota :one
INSERT INTO public.quotas (tenant_id, scope, scope_id, requests_per_min, tokens_per_min, tool_execs_per_min)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, tenant_id, scope, scope_id, requests_per_min, tokens_per_min, tool_execs_per_min, created_at, updated_at;

-- name: GetQuotaByScope :one
SELECT id, tenant_id, scope, scope_id, requests_per_min, tokens_per_min, tool_execs_per_min, created_at, updated_at
FROM public.quotas
WHERE tenant_id = $1 AND scope = $2 AND scope_id = $3
LIMIT 1;

-- name: ListQuotasByTenant :many
SELECT id, tenant_id, scope, scope_id, requests_per_min, tokens_per_min, tool_execs_per_min, created_at, updated_at
FROM public.quotas
WHERE tenant_id = $1
ORDER BY scope, scope_id;

-- name: UpdateQuota :one
UPDATE public.quotas
SET requests_per_min = $4, tokens_per_min = $5, tool_execs_per_min = $6, updated_at = now()
WHERE tenant_id = $1 AND scope = $2 AND scope_id = $3
RETURNING id, tenant_id, scope, scope_id, requests_per_min, tokens_per_min, tool_execs_per_min, created_at, updated_at;

-- name: DeleteQuota :exec
DELETE FROM public.quotas
WHERE tenant_id = $1 AND scope = $2 AND scope_id = $3;