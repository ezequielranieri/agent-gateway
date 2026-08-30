# agent-gateway

🇺🇸 [English version](./README.md)

Gateway / Control Plane multi-tenant para agentes LLM — el **único camino** entre tu aplicación y el modelo. Cada llamada autenticada, autorizada, rate-limitada, auditada y guardada.

> Estado: **MVP completo** — Fases 0-8 implementadas (Fundación, Rate Limiting, Audit Log, HITL, Guardrails, **Model Routing**, **Tool Sandbox**, **Clasificador Externo**, **CI/CD + Observabilidad**). Ver roadmap abajo.

## El problema

Todo sistema que opera agentes LLM a escala termina enfrentando tres preguntas incómodas:

- **¿Cómo garantizás que ninguna llamada saltee el gateway?** Sin una capa de intercepción obligatoria, un `http.Post` directo a un endpoint LLM filtra datos, quema presupuesto y evade todo control.
- **¿Cómo los datos, presupuesto y comportamiento de un tenant quedan aislados de otro?** Un `WHERE tenant_id = ?` olvidado en una query, un bucket de rate limit compartido, o un contexto de ejecución de tool filtrado es un incidente cross-tenant — no un bug, una brecha.
- **¿Cómo probás qué pasó cuando algo falla?** "Lo hizo el modelo" no es un audit trail. Necesitás logs inmutables de cada llamada al modelo, ejecución de tool, decisión de guardrail y aprobación humana — queryables, reejecutables, a prueba de manipulación.

`agent-gateway` responde esas tres con las herramientas más simples que dan la garantía correcta:

- **Arquitectura zero-bypass**: cadena de middleware chi — auth → resolución de tenant → rate limit → audit → guardrails → model router. Ningún endpoint llega al modelo sin pasar la cadena completa.
- **Aislamiento de tenant enforzado en la base de datos**: Row Level Security (RLS) **FORCE** en toda tabla tenanted, claves primarias compuestas `(id, tenant_id)`, contexto de tenant bindeado por transacción vía `set_config(..., true)`. Dos capas independientes (DB + middleware), no una.
- **Audit log que sobrevive al compromiso**: Tabla append-only en PostgreSQL con hash-chaining por tenant (`seq`, `prev_hash`, `chain_hash`), payloads JSON canonizados, detector `VerifyChain`. La manipulación deja evidencia.
- **Human-in-the-Loop como servicio reutilizable**: Máquina de estados en PostgreSQL + streaming SSE. Crear request de aprobación → humano revisa via token → re-validar contexto → materializar. El mismo servicio para cualquier acción de escritura en cualquier agente.
- **Guardrails como interfaz de dominio**: Interfaz `Guardrail` en `internal/domain/guardrail` — implementación local (regex, wordlist, patrones PII) incluída por defecto; clasificador externo (Claude API, Llama Guard) se enchufa como adapter sin tocar lógica de dominio.

## Arquitectura

```
┌─────────────────────────────────────────────────────────────────────┐
│                         Capa HTTP (chi)                             │
│  router.go · middleware.go (auth, tenant, ratelimit, audit, hitl)   │
└───────────────────────────▲───────────────────────────▲─────────────┘
                            │                           │
              ports.AuthService  │                    ports.Guardrail
                            │                           │
┌───────────────────────────┴───────────────────────────┴─────────────┐
│                      Capa de Aplicación (Casos de Uso)             │
│  services/gateway.go  — pipeline de request, model routing         │
│  services/ratelimit.go — token bucket por tenant/user/role         │
│  services/audit.go     — append-only + hash chain                  │
│  services/hitl.go      — state machine de aprobación + SSE         │
│  services/guardrail.go — pipeline de validación input/output       │
│  ports/repositories.go · ports/services.go  (DIP: solo interfaces) │
└───────────────────────────▲───────────────────────────▲─────────────┘
                            │                           │
┌───────────────────────────┴───────────────────────────┴─────────────┐
│                    Capa de Infraestructura (Adapters)              │
│  postgres/  (pgx, sqlc, sesiones RLS tenant, audit chain)          │
│  redis/     (go-redis + redis_rate token bucket)                   │
│  jwt/       (golang-jwt/v5 HS256, rotación multi-key via kid)      │
│  otel/      (OpenTelemetry stdout exporter + Prometheus /metrics)  │
│  guardrail/ (LocalGuardrail — regex/wordlist/patrones PII)         │
│  guardrail/ (ExternalClassifier — OpenAI/Anthropic/LlamaGuard)     │
│  hitl/      (handler SSE + state machine PG)                       │
└─────────────────────────────────────────────────────────────────────┘

Capa de Dominio (internal/domain): entidades puras + errores centinela, CERO
dependencias — importa solo la librería estándar.
```

```
cmd/
├── gateway/     composition root: servidor HTTP, cadena middleware, DI
├── bootstrap/   CLI: crea primer Super Admin + tenant por defecto
internal/
├── domain/           entidades: Tenant, User, Role, Quota, AuditEvent, ReviewRequest, GuardrailViolation
├── usecase/          servicios de aplicación (gateway, ratelimit, audit, hitl, guardrail)
├── adapter/          postgres, redis, jwt, otel, guardrail, hitl
├── api/              handlers OpenAPI 3.1 (generados con oapi-codegen)
└── middleware/       middlewares chi: auth, tenant, ratelimit, audit, hitl
migrations/           SQL schema + políticas RLS + seed
docker-compose.yml    postgres:16 + redis:7 + migrate
Makefile · .env.example · sqlc.yaml · goose.yaml
```

## Modelo de Tenancy y RLS (defensa en profundidad)

Tenants y roles son **globales**: `public.tenants` es el registro; `public.roles` es el catálogo compartido de roles. Ninguno lleva `tenant_id`, ninguno tiene RLS.

Toda tabla de negocio lleva `tenant_id` y está protegida por RLS **y FORCE**:

```sql
CREATE POLICY tenant_isolation ON private.audit_events
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
ALTER TABLE private.audit_events FORCE ROW LEVEL SECURITY;
```

- `current_setting('app.tenant_id')` devuelve `NULL` cuando no está seteado — un predicado NULL es **siempre falso**, así que un contexto de tenant faltante devuelve **cero filas, nunca un leak**.
- La aplicación bindea el tenant **dentro de una transacción**: `SELECT set_config('app.tenant_id', $1, true)` — el `true` lo hace LOCAL. Un `SET` plano en una conexión pooled leakea la identidad del tenant A en las requests del tenant B; el scoping transaccional lo previene.
- `FORCE` extiende RLS al owner de la tabla, así que incluso una sesión `psql` cruda o una query buggy no pueden cruzar tenants sin derechos de superuser.
- Las tablas tenanted usan **claves primarias compuestas** `(id, tenant_id)`. Esto cierra el side channel donde una FK de una tabla no protegida podría referenciar (y por tanto revelar) una fila de otro tenant.

## Diseño de Seguridad

| Preocupación | Mecanismo |
|---|---|
| Almacenamiento de passwords | Argon2id (m=65536, t=1, p=4, key 32 bytes), encoding PHC, `Verify` parsea el formato — nunca confía en config |
| Access token | JWT HS256, `sub`/`tenant_id`/`role`/`scopes`, **TTL 15 min**, algoritmo pinneado a HS256, rotación multi-key via `kid` |
| Refresh token | Opaco 256-bit random, guardado solo SHA-256, **rotación obligatoria** |
| Rotación | Cada refresh revoca el viejo, emite sucesor en el mismo `family_id` |
| Detección de replay | Presentar un token ya rotado (revocado + `replaced_by`) revoca la **familia entera** |
| Aislamiento tenant | PostgreSQL RLS FORCE + PK compuesta + middleware cross-check (token tenant = URL tenant) |
| Integridad audit | Append-only `private.audit_events` con hash-chaining por tenant (`seq`, `prev_hash`, `chain_hash`); `VerifyChain` detecta manipulación; eventos con severidad (info/warn/critical) |
| Rate limiting | Redis + `redis_rate` (token bucket), 3 dimensiones: reqs/min, tokens/min, tool_execs/min — por tenant, user, role |
| Guardrails | Interfaz de dominio `Guardrail` + `LocalGuardrail` (regex, wordlist, PII, patrones inyección); `CompositeGuardrail` con clasificadores externos (OpenAI Moderation, Anthropic, Llama Guard) — merge logic: any/all/weighted, fail behaviors: fallback_local/fail_open/fail_closed |
| HITL | State machine en PG (`PENDING` → `APPROVED`/`REJECTED`/`EXPIRED`), token opaco (SHA-256 guardado), streaming SSE, re-validación completa al aprobar |

## Quickstart

Requiere: Go 1.22+, Docker con Compose.

```bash
# 1. levantá db + redis, aplicá migraciones
make docker-up
make migrate-up

# 2. configurá la app
cp .env.example .env          # seteá DATABASE_URL, JWT_SECRET (nunca "change-me")

# 3. creá el primer Super Admin
make bootstrap

# 4. corré el gateway
make run

# sanity check
curl http://localhost:8080/health   # -> {"status":"ok"}
```

### Flujo demo (después del bootstrap)

```bash
# 1. Super Admin crea un tenant
curl -s -X POST http://localhost:8080/v1/tenants \
  -H "Authorization: Bearer $SUPER_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Acme Inc"}'

# 2. Registra el primer user de ese tenant (rol "admin" auto-asignado)
curl -s -X POST http://localhost:8080/v1/tenants/<tenantID>/users \
  -H "Authorization: Bearer $SUPER_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@acme.com","password":"a-strong-password"}'

# 3. Ese usuario hace login
curl -s -X POST http://localhost:8080/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@acme.com","password":"a-strong-password","tenant_id":"<tenantID>"}'
# -> {"access_token":"...","refresh_token":"...","expires_in":900}

# 4. Llama a un LLM a través del gateway (rate limited, auditado, guardado)
curl -s -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello"}]}'

# 5. Pide una aprobación humana (HITL) — ves el stream SSE en terminal
curl -N -H "Authorization: Bearer $ACCESS_TOKEN" \
  http://localhost:8080/v1/reviews/<request_id>/stream
```

El contrato completo — cada endpoint, cada status code, cada schema de request/response — está en [`openapi.yaml`](openapi.yaml) (generado desde la fuente de verdad).

## Cómo Testear

```bash
# Tests unitarios (sin DB) — table-driven, mocks en ports
go test ./internal/domain/... ./internal/usecase/...

# Tests de integración (Postgres + Redis reales via testcontainers-go)
# Requiere Docker corriendo
make test

# Lint
make lint
```

Los tests de integración corren contra **PostgreSQL y Redis reales** via testcontainers-go — sin mocks en persistencia, rate-limiting, ni audit. Cada repo, cada migración (incluyendo políticas RLS), y la atomicidad del rate limiter se testean contra infra real.

## Garantías Verificadas

Dos de las claims de correctitud más importantes se miden, no se asumen:

- **El aislamiento de tenant se sostiene a nivel base de datos.** Verificado por tests de integración que aseveran: (a) sin contexto de tenant, un `SELECT * FROM private.audit_events` crudo devuelve cero filas; (b) dentro de una transacción bindeada a tenant la misma tabla devuelve exactamente las filas de ese tenant; (c) un INSERT cross-tenant es rechazado por la política `WITH CHECK`.
- **El rate limiter es atómico bajo concurrencia real.** Verificado disparando 50 goroutines concurrentes contra la misma clave de rate limit con límite 3, y aseverando que exactamente 3 pasan y 47 son rechazados — no "aproximadamente", exactamente.

## Lo Que Deliberadamente NO Está Implementado (Todavía)

Mismo principio que `go-authz` y `agro-iam`: nombrado explícito, no silenciosamente ausente.

| Ítem | Razón | Criterio de salida |
|---|---|---|
| Model routing / fallback (GPT-4o → Claude → Llama local) | Requiere abstracción de provider + pricing model primero | Provider port + `PricingService` implementados |
| Abstracción de pricing/costo (model + tokens → USD) | Precios cambian por provider; necesita config versionado | `PricingService` con matriz provider/version |
| Tool sandbox (WASM via wazero / gVisor) | Aislamiento de ejecución es boundary separado; gateway routea, no ejecuta | Interfaz `ToolExecutor` + adapter `WasmExecutor` |
| Clasificador externo de guardrails (OpenAI / Anthropic / Llama Guard) | Agrega dependencia de red, latencia, costo, API keys | ✅ Fase 7 completa — adapter `ExternalClassifier` + merge compuesto |
| Métricas Prometheus / Dashboards Grafana | **IMPLEMENTADO** — Prometheus + Grafana + Alertmanager + Loki/Promtail para logs | ✅ Fase 8 completa |
| Aislamiento schema-per-tenant | RLS en instancia compartida basta para threat model actual | Solo si aparece requisito concreto de aislamiento mayor |
| Secret scanning full-history en cada push | CI escanea diffs de PR + rango pusheado; full history = alert fatigue | Gitleaks + Trivy en CI; job programado si se necesita |

## Pipeline CI/CD (Fase 8)

### Workflows

| Workflow | Trigger | Propósito |
|---|---|---|
| `.github/workflows/ci.yml` | Push a main, PR | Lint, test, build, secret scan, vulnerability scan |
| `.github/workflows/cd-staging.yml` | Push a main | Deploy a staging (automático) |
| `.github/workflows/release.yml` | Tag push (v*) | Build release, firmar imágenes, deploy a producción |

### Gestión de Secretos

- **GitHub Secrets**: Secrets de CI/CD (registry tokens, SSH keys, webhook URLs)
- **SOPS + age**: `.env.staging.enc` y `.env.prod.enc` encriptados en el repo
  - Solo claves públicas age autorizadas pueden desencriptar
  - Claves gestionadas via `.sops.yaml`
- **Ningún secreto en texto plano** en repo o imágenes Docker

### Despliegue Canary

```bash
# Flujo canary completo (deploy → monitor → promote)
./scripts/canary-deploy.sh full v1.0.0-rc2

# O paso a paso:
./scripts/canary-deploy.sh deploy v1.0.0-rc2
./scripts/canary-deploy.sh monitor
./scripts/canary-deploy.sh promote

# Rollback si hace falta
./scripts/canary-deploy.sh rollback
```

**Criterios de promoción** (configurables):
- Success rate ≥ 95% (default)
- Duración de monitoreo: 10 minutos (default)
- Latencia P99 < 5s
- Error rate < 5%

### Stack de Observabilidad

| Componente | Propósito |
|---|---|
| **Prometheus** | Recolección de métricas + reglas de alerta |
| **Grafana** | Dashboards (auto-provisionados) |
| **Alertmanager** | Enrutamiento de alertas (email, Slack, PagerDuty) |
| **Loki + Promtail** | Agregación de logs |
| **Jaeger** | Trazado distribuido |

**Dashboards clave** (auto-provisionados):
- `Agent Gateway - Overview` — Salud del servicio, latencia, costos, guardrails, HITL, tools

**Reglas de alerta** (`prometheus/rules/alerts.yml`):
- Servicio caído, error rate alto, latencia alta
- Agotamiento de rate limit, saturación DB/Redis
- Picos de violaciones guardrail, fallos de auth
- Picos de costo, rate de fallback de modelos

### Quickstart: CI Local

```bash
# Correr CI localmente (requiere act)
act -j lint
act -j test
act -j build

# O correr los pasos manualmente:
golangci-lint run ./...
go test -race -count=1 ./...
go build -o bin/gateway ./cmd/gateway
docker build -t agent-gateway:local .
```

### Proceso de Release

```bash
# 1. Crear y pushear tag
git tag v1.0.0
git push origin v1.0.0

# 2. GitHub Actions ejecuta:
#    - GoReleaser crea GitHub Release + binarios
#    - Imágenes Docker construidas y pusheadas a GHCR (firmadas con cosign)
#    - Deploy a producción (blue-green)

# 3. Verificar deploy
curl https://api.agent-gateway.com/health
```

## Roadmap

- [x] **Fase 0** — Fundación: entidades de dominio, schema RLS, primitivas auth, cadena middleware
- [x] **Fase 1** — Rate limiting: Redis token bucket (reqs/tokens/tools), por tenant/user/role
- [x] **Fase 2** — Audit log: append-only + hash chaining + VerifyChain
- [x] **Fase 3** — HITL: state machine + SSE + re-validación al aprobar
- [x] **Fase 4** — Guardrails: interfaz dominio + LocalGuardrail (regex/wordlist/PII)
- [x] **Fase 5** — Model routing: provider port + fallback chain + circuit breaker + pricing + adapter OpenAI
- [x] **Fase 6** — Tool sandbox: interfaz `ToolExecutor` + `WasmExecutor` (wazero) con fuel/memory/wall-time, FS read-only, sin red, instanciación por ejecución, bounded loop + HITL gate
- [x] **Fase 7** — Clasificador externo de guardrails (OpenAI Moderation, Anthropic, Llama Guard) con merge logic (any/all/weighted), fail behaviors (fallback_local/fail_open/fail_closed), HTTP client con retry + circuit breaker
- [x] **Fase 8** — Pipeline CI/CD (GitHub Actions), secret management (SOPS + GitHub Secrets), canary deploy, observability stack (Prometheus/Grafana/Alertmanager/Loki/Jaeger)

## Licencia

MIT — ver [LICENSE](LICENSE).