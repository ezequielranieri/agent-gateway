-- name: CreateUser :one
INSERT INTO public.users (tenant_id, email, password_hash, status)
VALUES ($1, $2, $3, 'active')
RETURNING id, tenant_id, email, password_hash, status, created_at, updated_at;

-- name: GetUserByEmail :one
SELECT id, tenant_id, email, password_hash, status, created_at, updated_at
FROM public.users
WHERE tenant_id = $1 AND email = $2
LIMIT 1;

-- name: GetUserByID :one
SELECT id, tenant_id, email, password_hash, status, created_at, updated_at
FROM public.users
WHERE tenant_id = $1 AND id = $2
LIMIT 1;

-- name: UpdateUser :one
UPDATE public.users
SET email = $3, password_hash = $4, status = $5, updated_at = now()
WHERE tenant_id = $1 AND id = $2
RETURNING id, tenant_id, email, password_hash, status, created_at, updated_at;

-- name: ListUserSessions :many
SELECT id, user_id, token_hash, family_id, revoked, expires_at, user_agent, ip, created_at, last_used_at, rotated_from
FROM public.refresh_tokens
WHERE user_id = $1 AND tenant_id = $2 AND revoked = false AND expires_at > now()
ORDER BY created_at DESC;

-- name: RevokeAllUserSessions :exec
UPDATE public.refresh_tokens
SET revoked = true
WHERE user_id = $1 AND tenant_id = $2 AND revoked = false;