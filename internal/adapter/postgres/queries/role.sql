-- name: CreateRole :one
INSERT INTO public.roles (name, description)
VALUES ($1, $2)
RETURNING id, name, description, created_at;

-- name: GetRoleByID :one
SELECT id, name, description, created_at
FROM public.roles
WHERE id = $1
LIMIT 1;

-- name: ListRolesByTenant :many
SELECT r.id, r.name, r.description, r.created_at
FROM public.roles r
JOIN public.role_permissions rp ON r.id = rp.role_id
WHERE rp.tenant_id = $1
GROUP BY r.id, r.name, r.description, r.created_at
ORDER BY r.name;

-- name: AssignRolePermissions :exec
INSERT INTO public.role_permissions (role_id, tenant_id, permission)
SELECT $1, $2, unnest($3::text[])
ON CONFLICT (role_id, tenant_id, permission) DO NOTHING;

-- name: GetRolePermissions :many
SELECT permission
FROM public.role_permissions
WHERE role_id = $1 AND tenant_id = $2
ORDER BY permission;

-- name: DeleteRole :exec
DELETE FROM public.roles
WHERE id = $1;

-- name: RevokeRolePermissions :exec
DELETE FROM public.role_permissions
WHERE role_id = $1 AND tenant_id = $2;