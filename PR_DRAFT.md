# PR Draft: tool-registry-hardening (Front A Hardening)

## Resumen

Este PR implementa el hardening del registro de tools (Front A) para cerrar dos vulnerabilidades críticas:

1. **Tool injection** — `chat.go:156-168` hacía `json.Unmarshal(req.Tools)` sin validación → cualquier tool arbitraria pasaba
2. **Resource exhaustion** — WASM executor no aplicaba fuel/memory limits configurados → DoS vía tool maliciosa

## Arquitectura

```
tool-registry (Front A)
├── tool-registry: Registro persistente con hash SHA-256 (RFC 8785/JCS)
│   ├── PostgreSQL table `tool_definitions` (tenanted, RLS FORCE)
│   ├── Hash computation: canonical JSON (name, description, parameters) → SHA-256
│   ├── LRU cache 5-min TTL con invalidación post-commit
│   └── Admin API: POST/PATCH/DELETE /admin/tools (super-auth + tenant_id en body)
├── wasm-resource-limits
│   ├── Runtime pool por buckets de memoria {256, 512, 1024, 2048}
│   ├── Timeout enforcement: execution_timeout_ms (reemplaza fuel_limit)
│   ├── WithCloseOnContextDone(true) en todos los runtimes
│   ├── Compilación lazy con cache compartido CompiledModule
│   └── Instancia nueva por Execute() (aislamiento de estado)
└── Integración
    ├── Chat handler valida tools contra registry (tenant_id del JWT)
    ├── Orchestrator: authorized tool set inmutable + re-resolución en ejecución
    └── Auditoría: hash-chained audit log con advisory lock serialization
```

## Estado de Verificación

| Componente | Estado | Detalle |
|------------|--------|---------|
| **WASM Fixtures** | ✅ Compilados | 4 módulos (.wat + .wasm commiteados) |
| **CI/CD Guards** | ✅ Activos | `-tags integration`, `TEST_DATABASE_URL`, `check-skip`, `check-disabled` |
| **Phase 2 Tests (unit)** | ✅ Verdes | Domain, config, hash tests |
| **Phase 2 Tests (integration)** | ⏳ **Sin ejecutar** | Requieren `TEST_DATABASE_URL` con Postgres no-superuser |
| **Phase 3 Tests (wazero)** | ✅ Verdes | 4 tests de límites (memoria, timeout, fail-closed) |
| **Phase 3 Tests (chat/usecase - unit)** | ✅ **Verdes** | 5 tests de revocación/validación con mocks |
| **Phase 3 Tests (chat/usecase - integration)** | ⏳ **Compilan** | Requieren Postgres para ejecución |
| **Mutaciones Phase 2** | ⏳ **Pendientes** | Documentadas, no ejecutadas |
| **Mutaciones Phase 3** | ⏳ **Pendientes** | Documentadas, no ejecutadas |

### Tests con Build Tag `integration` (requieren `TEST_DATABASE_URL`)

| Archivo | Tests | Estado |
|---------|-------|--------|
| `tool_repository_audit_test.go` | Atomicidad, severidad, concurrencia | ✅ Compila, ⏳ No ejecutado |
| `tool_repository_cache_test.go` | Cache hit/miss/TTL, aislamiento tenant | ✅ Compila, ⏳ No ejecutado |
| `admin_tools_test.go` | Authz, RLS cross-tenant, severidad | ✅ Compila, ⏳ No ejecutado |
| `TestConcurrentAuditAppends` | 20 goroutines × 5 events | ✅ Compila, ⏳ No ejecutado |

### Tests Phase 3 - Estado

| Archivo | Estado | Qué necesita |
|---------|--------|--------------|
| `executor_limits_test.go` | ✅ **Verdes** | 4 subtests: memory grow, min memory, infinite loop, fail-closed |
| `tool_calls_revocation_unit_test.go` | ✅ **Verdes** | 5 subtests: unauthorized tool, cross-tenant, deleted tool, hash mismatch, repo error |
| `chat_validation_unit_test.go` | ✅ **Verdes** | 7 subtests: unknown tool, hash mismatch, inactive tool, repo error, tenant isolation, registry definition sanitization, multiple tools |
| `chat_validation_test.go` (integration) | ⏳ **Compila** | Requiere Postgres + TEST_DATABASE_URL |
| `tool_calls_revocation_test.go` (integration) | ❌ **Eliminado (era stub)** | Reemplazado por `tool_calls_revocation_unit_test.go` |

## Comandos para Verificación Completa (requiere Docker + Postgres)

```bash
# 1. Levantar Postgres (rol no-superuser)
docker compose up -d postgres redis
# Crear rol de test: CREATE ROLE gateway WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD 'gateway';
# GRANT USAGE ON SCHEMA public TO gateway;
# GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO gateway;

# 2. Migraciones (como owner)
goose -dir migrations postgres "$DATABASE_URL" up

# 3. DSN para tests (rol no-superuser)
export TEST_DATABASE_URL="postgres://gateway:gateway@localhost:5432/agent_gateway?sslmode=disable"

# 4. Suite Phase 2
go test -tags integration ./internal/adapter/postgres/... -v -run "TestToolRepository" -count=1
go test -tags integration ./internal/api/handlers/... -v -run "TestAdminTools" -count=1
go test -tags integration -v ./test/integration/... -run "TestConcurrentAuditAppends" -count=1

# 5. Suite Phase 3 (tras re-habilitar tests)
go test -tags integration ./internal/adapter/tool/wazero/... -v -run "TestExecutor" -count=1
go test -tags integration ./internal/api/handlers/... -v -run "TestChatHandler" -count=1
go test -tags integration ./internal/usecase/chat/... -v -run "TestToolCalls" -count=1

# 6. Suite completa (sin SKIP, sin .disabled)
go test -tags integration ./... -count=1 -v
make check-disabled
make check-skip
```

## CI / Guards Implementados

| Guard | Ubicación | Qué hace |
|-------|-----------|----------|
| `check-disabled` | `Makefile`, CI step | Falla si hay `*.disabled` |
| `check-skip` | `Makefile`, CI step | Falla si `--- SKIP` inesperado (allowlist: short mode) |
| `test-integration` | `Makefile` | `go test -tags integration -count=1 -v ./...` |
| CI step | `.github/workflows/ci.yml` | `-tags integration`, `TEST_DATABASE_URL`, `check-skip` |

## Pendientes Documentados (para PR final)

### Phase 2 - Cambios Pendientes

| Item | Descripción | Test asociado |
|------|-------------|---------------|
| **Lock advisory** | Mover lock solo a `AppendWithTx` (quitar de `Append`) | `TestConcurrentAuditAppends` |
| **Atomicidad invertida** | Test: audit falla → tool write se revierte | `tool_repository_audit_test.go` |
| **Concurrencia mixta** | Admin writes + normal appends mismo tenant | Nuevo test en `tool_repository_audit_test.go` |
| **Fixture memory_grow** | `grow(300)` en bucket 256 + control positivo en 512 | `executor_limits_test.go` |

### Phase 3 - Mutaciones (Phase 2)

| Mutación | Test que debe fallar | Estado |
|----------|---------------------|--------|
| Quitar RLS policy / `FORCE` | Cross-tenant tests fallan | ⏳ No ejecutada |
| Quitar `WITH CHECK` | Test cambio `tenant_id` falla | ⏳ No ejecutada |
| Quitar check `super_admin` | Test 403 falla | ⏳ No ejecutada |
| Audit fuera de transacción | Test atomicidad falla | ⏳ No ejecutada |
| Invalidar cache antes de commit | Test invalidación falla | ⏳ No ejecutada |

### Phase 3 - Mutaciones (Phase 3) - RESULTADOS

| Mutación | Test que debe fallar | Estado | Evidencia |
|----------|---------------------|--------|-----------|
| 1. Quitar hash check en `chat.go` | `Known_tool_with_hash_mismatch_rejected` | ✅ **DETECTADA** | Retorna 200 en vez de 400 |
| 2. Reenviar request bytes al proveedor | `Provider_receives_registry_definition` | ❌ **NO DETECTADA** | JSON equivalente bajo JCS produce mismos bytes canónicos |
| 3. Error repo → permitir (chat.go) | `Repo_error_at_validation_fails_closed` | ✅ **DETECTADA** | Retorna 200 en vez de 503 |
| 4. Tomar tenant del body | `Body_tenant_id_ignored` | ✅ **DETECTADA** | Retorna 200 en vez de 400 |
| 5. Validar solo primera tool | `Multiple_tools_-_one_invalid` | ✅ **DETECTADA** | Retorna 200 en vez de 400 |
| 6. Tool inactiva como activa | `Tool_inactive_rejected` | ✅ **DETECTADA** | Retorna 200 en vez de 400 |
| 7. Quitar chequeo set autorizado | `Authorized_tool_set_is_immutable` | ✅ **DETECTADA** | GetByName llamado (count=1 vs 0) |
| 8. Quitar re-resolución + hash | `Re-resolve_at_execution_verifies_hash` | ✅ **DETECTADA** | Executor llamado (count=1) |
| 9. Error repo en ejecución → permitir | `Repo_error_at_re-resolution` | ❌ **NO DETECTADA** | Nil pointer panic (test detecta, pero no por aserción) |
| 10. Quitar `WithCloseOnContextDone` | `Infinite_loop_terminated_by_timeout` | ✅ **DETECTADA** | Test cuelga (timeout) |
| 11. Quitar `WithMemoryLimitPages` | `Memory_grow_exceeding_limit` | ✅ **DETECTADA** | Test pasa sin error (sin límite de memoria) |
| 12. Aceptar límites en cero | `No_limits_configured_-_fail_closed` | ✅ **DETECTADA** | Retorna éxito en vez de error |
| 13. Hash con `json.Marshal` en vez de `ComputeHash` | JCS canonicalization test | ❌ **NO DETECTADA** | Test no escrito aún |

**Resumen**: 10/13 detectadas (77%), 3 no detectadas (#2, #9, #13).

## Pendientes de Tests (para que las mutaciones restantes detecten)

| Test | Problema | Fix necesario |
|------|----------|---------------|
| `Authorized_tool_set_is_immutable` | ✅ **ARREGLADO** - test usa tool existente en registry | GetByName spy agregado, mutación #7 detectada |
| `Tool_deleted_mid-request` | ❌ Pendiente | Agregar tool al fake repo ANTES de validar, remover ENTRE validación y ejecución |
| `chat_validation_test.go` (todos) | ❌ Pendiente | Asertar que `fakeAuditRepo` recibe eventos con action/severity/tenant correctos |
| JCS canonicalization test | ❌ Pendiente | Agregar mutación #13: hash con `json.Marshal` en vez de `ComputeHash` |

## Próximos pasos para completar mutaciones

| Acción | Detalle |
|--------|---------|
| Fix test #9 | Mutación debe fallar por "executor no debe ser llamado", no por nil pointer |
| Fix test #13 | Agregar test JCS canonicalization + mutación hash con `json.Marshal` |
| Auditoria en `chat_validation_test.go` | Asertar `fakeAuditRepo` recibe eventos con action/severity/tenant correctos en cada rechazo |
| Mid-request deletion | Agregar tool ANTES de validar, remover ENTRE validación y ejecución |

## Comandos para Ejecución Rápida (sin Postgres)

```bash
# Verificación de compilación (unit + integration tags)
go build ./...
go vet -tags integration ./...
go test -tags integration -c ./internal/adapter/postgres/...  # Compila tests sin ejecutar
go test -tags integration -c ./internal/api/handlers/...
go test -tags integration -c ./internal/usecase/chat/...
go test -tags integration -c ./internal/adapter/tool/wazero/...

# Tests que NO requieren Postgres (unit)
go test ./internal/domain/tool/... -v -count=1
go test ./internal/domain/... -count=1
go test ./internal/adapter/tool/wazero/... -count=1
go test ./internal/usecase/chat/... -tags=unit -v -run TestToolCalls_RevocationAndValidation_Unit -count=1
go test ./internal/api/handlers/... -tags=unit -v -run TestChatHandler_Validation_Unit -count=1

# Guards
make check-disabled
make check-skip
```

## Checklist para PR Ready

- [ ] Tests Phase 2 integration corren en CI (PostgreSQL no-superuser)
- [ ] Tests Phase 3 re-habilitados y verdes
- [ ] Mutaciones Phase 2 ejecutadas y documentadas
- [ ] Mutaciones Phase 3 ejecutadas y documentadas
- [ ] `make check-disabled` verde (0 archivos `.disabled`)
- [ ] `make check-skip` verde (sin SKIP inesperados)
- [ ] `go test -tags integration ./... -count=1 -v` verde sin SKIP
- [ ] Output real pegado en PR description
- [ ] Tabla de mutaciones con estado y evidencia