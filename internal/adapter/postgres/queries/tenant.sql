-- name: CreateTenant :one
INSERT INTO public.tenants (name, status)
VALUES ($1, 'active')
RETURNING id, name, status, created_at;

-- name: GetTenantByID :one
SELECT id, name, status, created_at
FROM public.tenants
WHERE id = $1
LIMIT 1;

-- name: GetTenantByName :one
SELECT id, name, status, created_at
FROM public.tenants
WHERE name = $1
LIMIT 1;

-- name: UpdateTenantStatus :one
UPDATE public.tenants
SET status = $2
WHERE id = $1
RETURNING id, name, status, created_at;

-- name: ListTenants :many
SELECT id, name, status, created_at
FROM public.tenants
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;