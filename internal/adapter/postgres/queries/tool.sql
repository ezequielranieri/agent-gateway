-- name: CreateToolDefinition :one
INSERT INTO public.tool_definitions (
    tenant_id, name, description, input_schema, grants, execution_timeout_ms, memory_pages, hash, is_active
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING id, tenant_id, name, description, input_schema, grants, execution_timeout_ms, memory_pages, hash, is_active, created_at, updated_at;

-- name: GetToolDefinitionByName :one
SELECT id, tenant_id, name, description, input_schema, grants, execution_timeout_ms, memory_pages, hash, is_active, created_at, updated_at
FROM public.tool_definitions
WHERE tenant_id = $1 AND name = $2 AND is_active = true;

-- name: GetToolDefinitionByNameAll :one
SELECT id, tenant_id, name, description, input_schema, grants, execution_timeout_ms, memory_pages, hash, is_active, created_at, updated_at
FROM public.tool_definitions
WHERE tenant_id = $1 AND name = $2;

-- name: UpdateToolDefinition :one
UPDATE public.tool_definitions
SET description = $3,
    input_schema = $4,
    grants = $5,
    execution_timeout_ms = $6,
    memory_pages = $7,
    hash = $8,
    is_active = $9,
    updated_at = now()
WHERE tenant_id = $1 AND name = $2
RETURNING id, tenant_id, name, description, input_schema, grants, execution_timeout_ms, memory_pages, hash, is_active, created_at, updated_at;

-- name: DeactivateToolDefinition :exec
UPDATE public.tool_definitions
SET is_active = false, updated_at = now()
WHERE tenant_id = $1 AND name = $2;

-- name: UpsertToolDefinition :one
INSERT INTO public.tool_definitions (
    tenant_id, name, description, input_schema, grants, execution_timeout_ms, memory_pages, hash, is_active
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (tenant_id, name) WHERE is_active = true DO UPDATE SET
    description = EXCLUDED.description,
    input_schema = EXCLUDED.input_schema,
    grants = EXCLUDED.grants,
    execution_timeout_ms = EXCLUDED.execution_timeout_ms,
    memory_pages = EXCLUDED.memory_pages,
    hash = EXCLUDED.hash,
    is_active = EXCLUDED.is_active,
    updated_at = now()
RETURNING id, tenant_id, name, description, input_schema, grants, execution_timeout_ms, memory_pages, hash, is_active, created_at, updated_at;

-- name: ListToolDefinitions :many
SELECT id, tenant_id, name, description, input_schema, grants, execution_timeout_ms, memory_pages, hash, is_active, created_at, updated_at
FROM public.tool_definitions
WHERE tenant_id = $1
ORDER BY name;

-- name: DeleteToolDefinition :exec
DELETE FROM public.tool_definitions
WHERE tenant_id = $1 AND name = $2;