-- name: CreateRefreshToken :one
INSERT INTO public.refresh_tokens (user_id, tenant_id, token_hash, family_id, expires_at, user_agent, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, user_id, tenant_id, token_hash, family_id, revoked, expires_at, user_agent, ip, created_at, last_used_at, rotated_from;

-- name: GetRefreshTokenByHash :one
SELECT id, user_id, tenant_id, token_hash, family_id, revoked, expires_at, user_agent, ip, created_at, last_used_at, rotated_from
FROM public.refresh_tokens
WHERE token_hash = $1
LIMIT 1;

-- name: RotateRefreshToken :one
WITH new_token AS (
    INSERT INTO public.refresh_tokens (user_id, tenant_id, token_hash, family_id, expires_at, user_agent, ip, rotated_from)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    RETURNING id, user_id, tenant_id, token_hash, family_id, revoked, expires_at, user_agent, ip, created_at, last_used_at, rotated_from
),
revoked_old AS (
    UPDATE public.refresh_tokens
    SET revoked = true
    WHERE id = $8
)
SELECT * FROM new_token;

-- name: RevokeRefreshToken :exec
UPDATE public.refresh_tokens
SET revoked = true
WHERE id = $1;

-- name: RevokeRefreshTokenFamily :exec
UPDATE public.refresh_tokens
SET revoked = true
WHERE family_id = $1 AND revoked = false;

-- name: ListActiveRefreshTokens :many
SELECT id, user_id, tenant_id, token_hash, family_id, revoked, expires_at, user_agent, ip, created_at, last_used_at, rotated_from
FROM public.refresh_tokens
WHERE user_id = $1 AND tenant_id = $2 AND revoked = false AND expires_at > now()
ORDER BY created_at DESC;

-- name: UpdateRefreshTokenLastUsed :exec
UPDATE public.refresh_tokens
SET last_used_at = now()
WHERE id = $1;