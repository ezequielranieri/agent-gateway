# DECISIONS.md — agent-gateway

Decisiones de arquitectura y constitución del proyecto. Cada entrada responde: qué elegimos, por qué, y qué sacrificamos. Escrito para el próximo ingeniero (o el entrevistador) que lea este repositorio en frío.

> 🇺🇸 [English version](./DECISIONS.md)

Este archivo es la fuente de verdad para cualquier agente (humano o IA) que trabaje en este repositorio. Toda implementación debe respetar estas reglas. Si una tarea entra en conflicto con algo definido aquí, **parar y consultar antes de proceder**.

Cada decisión registra: la regla (qué el código DEBE hacer), las alternativas rechazadas y por qué, y el costo que el proyecto acepta por elegirla. Esta última parte importa: saber qué cuesta una decisión es lo que permite reabrirla luego con justificación fuerte en vez de corazonada.

Leyenda de estado: `Accepted` = resuelto; `Open` = propuesta, no implementar.

---

## 1. Stack Tecnológico (fijo, no negociable sin discusión explícita)

| Capa | Tecnología | Notas |
|---|---|---|
| Lenguaje | Go 1.22+ | Concurrencia nativa, bajo overhead |
| Arquitectura | Clean / Hexagonal | Dominio independiente de frameworks |
| HTTP | chi | Minimalista, sin opiniones extra |
| Validación | go-playground/validator | Structs tipadas + validación declarativa |
| Base de datos | PostgreSQL 16 | Fuente de verdad (ACID) + Row Level Security como defensa en profundidad |
| Queries | sqlc + pgx | Complementarios: sqlc genera código type-safe sobre pgx |
| Migraciones | goose | Obligatorio — sqlc no gestiona schema |
| Cache / Rate Limit | Redis 7 + redis_rate | Token bucket implementado |
| Access Token | JWT HS256 | Rotación multi-key via `kid` |
| Refresh Token | Opaco + rotación obligatoria + detección de reuse (family_id) | Solo se guarda SHA-256 |
| Password Hashing | Argon2id | Parámetros OWASP (m=65536, t=1, p=4) |
| Rate Limiting | Redis + redis_rate (token bucket) | 3 dimensiones: requests, tokens, tool_execs por tenant/user/role |
| Observabilidad | OpenTelemetry (stdout exporter) + Prometheus /metrics | Corrible localmente, sin vendor lock-in |
| Healthchecks | /health, /ready | Conectividad viva DB + Redis |
| Testing | testify + table-driven + mocks en puertos | testcontainers-go para integración |
| Contrato API | OpenAPI 3.1 (oapi-codegen) | Spec = verdad, handlers generados |
| Local / Demo | Docker Compose (Postgres + Redis + Gateway) | `make docker-up` → todo corre |
| CI | GitHub Actions (lint, test, build, secret scan) | gitleaks en diffs de PR + rango de push |
| Secretos | Variables de entorno + .env.example — nada hardcodeado | Vault/SealedSecrets para producción |

**Explícitamente fuera del MVP:**
- Terraform / Kubernetes
- gRPC (se puede agregar después)
- Service mesh
- Multi-region
- UI de administración

**Deuda técnica (explícita, nombrada):**
1. **Redis SPOF** — instancia única en MVP. Criterio de salida: migrar a Sentinel/Cluster antes de producción.
2. **Sin tool sandbox** — gateway routea, no ejecuta. Criterio: interfaz `ToolExecutor` + adapter wazero WASM.
3. **Sin model routing/fallback** — single provider por ahora. Criterio: provider port + `PricingService` implementados.
4. **Sin clasificador externo de guardrails** — solo implementación local. Criterio: interfaz `Guardrail` + adapter `ExternalClassifier`.
5. **Sin schema-per-tenant** — RLS en instancia compartida basta. Criterio: solo si aparece requisito concreto.

---

## 2. Decisiones de Diseño Confirmadas (no reabrir sin justificación fuerte)

### AD-001 · Arquitectura Clean/Hexagonal — Status: Accepted

**Regla**
- `internal/domain` — entidades puras, errores centinela, **cero dependencias externas** (solo stdlib).
- `internal/usecase` — servicios de aplicación dependiendo solo de interfaces `ports` (DIP).
- `internal/adapter` — implementaciones: `postgres`, `redis`, `jwt`, `otel`, `guardrail`, `hitl`.
- `internal/api` — handlers OpenAPI generados (finos, delegan a use cases).
- `internal/middleware` — middlewares chi: auth, tenant, ratelimit, audit, hitl.
- Compuesto en `cmd/gateway` (composition root).

**Por qué:** El gateway no debe saber que existen PostgreSQL, Redis o Gemini. El servicio HITL no debe saber que existe pgx. Los tests usan fakes en todo lado, lo que hace que los tests unitarios corran con `go test ./...` y sin testcontainers.

**Trade-off:** Más archivos e indireccion que un servicio delgado. El payoff es que cada slice (rate limiting, audit, HITL, guardrails) aterriza sin tocar el dominio.

---

### AD-002 · RLS FORCE + PK Compuesta (id, tenant_id) como Mecanismo de Tenancy — Status: Accepted

**Regla**
- El tenancy DEBE ser enforzado por **PostgreSQL Row Level Security FORCE**, nunca solo por la capa de aplicación. El aislamiento DEBE sostenerse contra una query buggy, una capa de repositorio rota, o una sesión `psql` cruda.
- Toda tabla tenanted DEBE llevar `tenant_id`, DEBE tener RLS enabled con `FORCE`, y DEBE tener una policy filtrando filas en `tenant_id = current_setting('app.tenant_id')::uuid`.
- Las tablas globales (`public.tenants`, `public.roles`) son **globales**: ninguna lleva `tenant_id`, ninguna tiene RLS, ninguna puede ser tenanted.
- Las tablas tenanted DEBEN usar **claves primarias compuestas `(id, tenant_id)`**.
- La aplicación DEBE bindear el contexto de tenant por transacción vía `SELECT set_config('app.tenant_id', $1, true)` dentro de una transacción dedicada — el `true` lo hace LOCAL. Nunca un `SET` plano en una conexión pooled.

**Alternativas rechazadas**
- *Schema-per-tenant*: cientos de tenants pequeños explotan el catálogo y el tooling de migración/backup; el producto necesita operaciones baratas por tenant.
- *Database-per-tenant*: infra dedicada por tenant debe ser barata; una instancia sirve miles.
- *Filtrado solo en aplicación (`WHERE tenant_id = ...`)*: no estructural — una query buggy o sesión SQL cruda lo bypasea; el aislamiento debe sobrevivir bugs de aplicación.
- *ENABLE RLS sin FORCE*: un table owner (a menudo el usuario de migración o DBA) bypasea RLS totalmente; una query accidental sin filtro desde la conexión de la app podría cruzar tenants.
- *PK de columna simple `id`*: crea un side channel — una FK en tabla no protegida, o cualquier `JOIN`, puede referenciar la fila de otro tenant por su `id` plano, y una query que devuelve ese `id` revela que la fila existe aunque RLS oculte sus columnas.

**Costo aceptado**
- Toda query necesita contexto de tenant o devuelve cero filas silenciosamente — debuggear un resultado "vacío" requiere conocer el GUC.
- Mecanismo Postgres-only: RLS no tiene equivalente portable, así que la elección de DB queda lockeada.
- Cada unidad de trabajo tenanted paga el costo de una transacción.
- Las `FOREIGN KEY` a tablas tenanted deben repetir el par de columnas compuestas.
- Operaciones de migración y seed que tocan todos los tenants requieren privilegios elevados.

---

### AD-003 · JWT HS256 + Rotación Multi-Key (kid) — Status: Accepted

**Regla**
- Los access tokens DEBEN ser **JWT HS256** con **TTL 15 min**, llevando `sub` (user id), `tenant_id`, `role`, `scopes`.
- Múltiples claves de firma pueden estar activas para verificación simultáneamente, identificadas por `kid`. Exactamente una clave se usa para *firmar* tokens nuevos; todas las conocidas (actual y rotadas previamente) se aceptan para *verificar*.
- El algoritmo DEBE estar **pinneado a HS256**: la verificación DEBE rechazar cualquier otro método de firma, cerrando los ataques clásicos `alg=none` y key-confusion.

**Alternativas rechazadas**
- *RS256 (asimétrico)*: más moving parts de key management; rechazado por simplicidad de implementación, con path documentado a RS256/JWKS cuando se necesite verificación multi-servicio.
- *Session tokens opacos (estado server-side)*: sin problema de revocación, pero necesitan estado server-side por request; tokens stateless elegidos por simplicidad.

**Costo aceptado**
- HS256 es simétrico — el secreto firma y verifica; un `JWT_SECRET` filtrado falsifica tokens para cualquier tenant, así que el secreto debe rotar en schedule.
- Access tokens no pueden revocarse antes de expiración (mitigado por TTL corto).
- Un segundo servicio solo puede verificar tokens con los que comparte el secreto.

---

### AD-004 · Refresh Tokens: Opacos + Rotación + Detección de Reuse por Familia — Status: Accepted

**Regla**
- Los refresh tokens DEBEN ser **valores random 256-bit opacos** (`crypto/rand`, base64url) devueltos al cliente exactamente una vez y persistidos **solo como su digest SHA-256**.
- La rotación DEBE mantener una **cadena por familia**: cada refresh revoca el viejo, emite sucesor en el mismo `family_id`, y registra `replaced_by`.
- Presentar un token ya rotado (revocado con `replaced_by` seteado) ES la firma de replay y DEBE revocar la **familia entera** (`RevokeFamily`), matando el sucesor recién emitido del atacante también.
- El orden revocado-vs-expirado importa: un token que es ambas cosas se reporta como reuse, no como "expirado" — sino el intento de reuse del atacante podría esconderse detrás de un error benigno.
- El orden de rotación DEBE fallar seguro: persistir el nuevo token *primero*; solo revocar el viejo después que el nuevo esté durablemente guardado; emitir el nuevo access token último (menos probable de fallar, firma en memoria sin I/O).

**Alternativas rechazadas**
- *Guardar refresh tokens en plaintext*: un breach de DB dejaría al atacante rejugar el token para siempre.
- *Rotación sin tracking de familia*: en la race clásica donde tanto atacante como usuario legítimo presentan el mismo token, el segundo no debe tener éxito — sin revocación familia-wide el sucesor robado del atacante sobrevive.

**Costo aceptado**
- La rotación agrega estado y escrituras concurrency-sensitive.
- Un sucesor perdido o no transmitido tras un refresh fuerza re-login.
- El replay de un token *expirado* no debe clasificarse mal como robo — la lógica de rotación debe distinguir Expired de Revoked.

---

### AD-005 · Argon2id Password Hashing sobre bcrypt — Status: Accepted

**Regla**
- Los passwords DEBEN hashearse con **Argon2id** usando parámetros OWASP interactive `m=65536, t=1, p=4` (64 MiB memoria, 1 iteración, 4 lanes), key de 32 bytes, y salt random de 16 bytes.
- Los hashes DEBEN codificarse en formato **PHC** (`$argon2id$v=19$m=65536,t=1,p=4$...`), y `Verify` DEBE re-derivar los parámetros del string guardado — nunca de configuración — así un bump futuro de parámetros es transparente a hashes existentes.

**Alternativas rechazadas**
- *bcrypt*: su memory footprint es chico, así que resiste mal ASIC/GPU brute force por diseño; la recomendación de la comunidad para logins interactivos cambió.

**Costo aceptado**
- Hashear cuesta 64 MiB por llamada — más CPU/memoria por login.
- No es primitiva FIPS-approved en algunos contextos de compliance.
- Parámetros fijos en código, así que re-hashear en cambio de parámetros es una migración.

---

### AD-006 · Rate Limiting: Redis Token Bucket (3 Dimensiones) — Status: Accepted

**Regla**
- El rate limiting DEBE usar modelo **token bucket** via `redis_rate` (Redis + Lua, atómico y distribuido).
- Tres buckets independientes por identidad: **requests/min**, **tokens/min** (costo), **tool_execs/min**.
- Dimensiones de identidad: **por tenant**, **por user**, **por role** — precedencia configurable.
- Redis NO DEBE ser dependencia dura para disponibilidad: cliente nil o error de Redis falla **open** (permite la request) con WARN log — un rate limiter sano nunca se vuelve denial of service para usuarios legítimos.
- Límites excedidos DEBEN devolver `429` con body JSON de error y header `Retry-After` (segundos ceiling).

**Alternativas rechazadas**
- *Fixed window / sliding window*: token bucket maneja burst mejor y mapea naturalmente a "tokens" y "tool executions" como recursos consumibles.
- *Fail-closed en outage de Redis*: convertiría un blip de Redis en outage de disponibilidad para cada llamada autenticada — inaceptable para un gateway.
- *Bucket único por tenant*: un usuario comprometido podría agotar la cuota del tenant entero; per-`tenant:user:role` mantiene a un bad actor sin DoSear el tenant.

**Costo aceptado**
- Fallback en memoria (si Redis no disponible) es estado per-process: se pierde en restart y es incorrecto cross-instancias — limitación documentada, ok para postura single-instance.
- Tres buckets por identidad = más keys en Redis; aceptado por granularidad.

---

### AD-007 · Audit Log: Append-Only + Hash-Chaining Por Tenant — Status: Accepted

**Regla**
- El audit trail DEBE ser **hash-chained por tenant**: cada fila de `private.audit_events` lleva `seq`, `prev_hash`, y `chain_hash` con `UNIQUE (tenant_id, seq)`.
- La entrada génesis usa `seq = 1` y `prev_hash` = 64 hex zeros; cada fila subsecuente su `prev_hash` DEBE igualar el `chain_hash` de la anterior.
- `chain_hash` DEBE ser `SHA-256` sobre el orden fijo de campos `prev_hash|seq|tenant_id|actor_user_id|action|entity_type|entity_id|payload|created_at` donde `payload` es el JSON **canonizado** (decode → re-marshal), y `created_at` truncado a precisión microsegundo (resolución `timestamptz` de Postgres) — el mismo codepath exacto para insert y verify, así no hay drift.
- La verificación de cadena (`VerifyChain`) DEBE ser interna: recalcular la cadena desde todas las filas guardadas y reportar el primer `seq` roto.
- Los appends de audit DEBEN correr dentro de la transacción tenant-bound (RLS `FORCE` rechazaría el insert si no) y DEBEN ser fail-open: un fallo de audit nunca falla el flow principal, solo WARN log.

**Alternativas rechazadas**
- *Cadena global (una secuencia para todos los tenants)*: una sesión de tenant no puede leer filas de otros tenants bajo RLS, así que el append rompería; también serializa a todos los tenants en una fila hot y leakea ordering cross-tenant.
- *Trigger de DB calculando el hash*: zero drift, pero lógica de seguridad escondida en SQL de migración, no testeable en Go puro, y contra la convención de mantener lógica en servicios.
- *Hashear la fila SQL completa*: cualquier futura migración agregando columna invalidaría toda la cadena.

**Costo aceptado**
- Un indexed tail read + insert por evento de audit (sync, en write path).
- Verificación es O(n) — ok en volumen portfolio, revisar si crece.
- Payloads deben mantenerse float64-safe para que la canonización sea estable.

---

### AD-008 · Human-in-the-Loop: State Machine + SSE + Re-validación Completa — Status: Accepted

**Regla**
- Las acciones de escritura nunca mutan directo. El agente crea un request de aprobación `PENDING` con un **token opaco** (32 random bytes, hex) cuyo **hash SHA-256** se persiste.
- Aprobar/rechazar requiere el token (compare timing-safe), un request pending no expirado (TTL 24h), y RBAC (`admin`/`operator`).
- Al aprobar, el servicio **re-valida** el contexto: payload re-parseado con `DisallowUnknownFields`, IDs de recursos resueltos dentro del tenant, luego materializar la acción y marcar request `EXECUTED`.
- Audit es fail-open: si falla escribir la fila de audit, el flow continúa con WARN.
- Streaming SSE (`net/http` nativo) para updates de status en tiempo real — más simple que WebSockets, atraviesa proxies, auto-reconnect en browser.

**Alternativas rechazadas**
- *WebSockets (nhooyr.io/websocket)*: requieren sticky sessions, config de proxy, lógica de reconexión; SSE es nativo, más simple, anda con `curl -N`.
- *Estado approved-but-not-executed*: agrega complejidad; approve == execute para MVP. Estado `APPROVED` existe en enum para futuro worker diferido.
- *Checks de aprobación a nivel handler*: convención del proyecto es middleware (RequireAuth, RateLimit, Audit); checks dispersos son más difíciles de auditar y más fáciles de saltear.

**Costo aceptado**
- Sin estado "approved but not executed" aún — un worker diferido es trabajo futuro.
- SSE es unidireccional (server→client); si se necesita bidireccional, WebSockets se puede agregar.

---

### AD-009 · Guardrails: Interfaz de Dominio + Implementación Local — Status: Accepted

**Regla**
- Los guardrails DEBEN ser una **interfaz de dominio** (`Guardrail`) en `internal/domain/guardrail` con `CheckInput`, `CheckOutput`, tipos `Violation`.
- Una **implementación local** (`LocalGuardrail`) viene por defecto: patrones regex, wordlists, detección PII (email, tarjeta, SSN), heurísticas de prompt injection — cero dependencia de red, cero API keys, corre in-process.
- Clasificadores externos (Claude API, Llama Guard) se enchufan como **adapters** implementando `Guardrail` — nunca en el dominio.
- Validación de input falla closed (rechaza); validación de output falla closed (rechaza o sanitiza según severidad).
- Violaciones de guardrail se registran en audit log con severidad `critical`.

**Alternativas rechazadas**
- *Solo clasificador externo (Claude API / Llama Guard)*: dependencia de red, latencia, costo, API keys, sus rate limits — rompe "corre localmente con un comando".
- *Reglas hardcodeadas en middleware*: no testeable, no extensible, acopla capa HTTP a policy.

**Costo aceptado**
- Implementación local atrapa patrones comunes pero no ataques semánticos; adapter clasificador externo llena ese gap cuando haga falta.
- Mantenimiento de regex/wordlist es manual; aceptado para scope portfolio.

---

### AD-010 · Model Routing: Provider Port + Fallback + Pricing Abstraction — Status: Open (Design Phase)

**Regla**
- El model routing DEBE estar detrás de un **provider port** (`ModelProvider` interface) en `internal/domain/model`.
- Router selecciona provider basado en: disponibilidad, latency SLO, budget de costo, preferencia de tenant.
- Cadena de fallback: primary → secondary → local (Ollama/Llama.cpp) — retries acotados, circuit breaker.
- **PricingService** mapea `model + input_tokens + output_tokens → USD` con tablas de precios versionadas por provider.

**Por qué:** Un gateway que solo llama un provider no es un gateway — es un proxy. Producción necesita routing, fallback, y control de costo.

**Trade-off:** Agrega capa de abstracción de provider; tablas de pricing deben mantenerse.

---

### AD-011 · Tool Sandbox: ToolExecutor Interface + WASM (wazero) — Status: Open (Design Phase)

**Regla**
- La ejecución de tools DEBE estar detrás de una interfaz **`ToolExecutor`** en `internal/domain/tool`.
- Implementación default: `MockExecutor` para tests, `WasmExecutor` (wazero) para producción — WASM corre in-process, sin kernel mods, portable, aislamiento fuerte.
- Cada tenant obtiene instancia de módulo aislada; límites de recursos (fuel, memoria, wall time) enforzados por ejecución.

**Por qué:** Si el agente llama `rm -rf /` o accede a secrets de otro tenant, el gateway debe prevenirlo. Sandbox es el punto de enforcement.

**Trade-off:** wazero agrega tamaño de binary y complejidad de compilación; gVisor/Firecracker son alternativas más pesadas.

---

## 3. Convenciones de Código

- **Dominio puro**: `internal/domain` importa solo la librería estándar — entidades y errores centinela, cero dependencias externas.
- **SQL vive en dos lugares solo**: `migrations/` y `internal/adapter/postgres`. SQL nunca se llama directo desde handlers `internal/api` o servicios `internal/usecase`.
- **Todo acceso tenanted pasa por el helper `WithTenant`** (`internal/adapter/postgres/db.go`); repositorios corren cada query adentro para que RLS sea el punto de enforcement, no una cláusula `WHERE`.
- **Errores se mapean en el boundary del adapter**: errores de driver pgx traducidos a errores de dominio (`mapPgErr` — ej. `unique_violation` 23505 → `domain.ErrConflict`); código de aplicación nunca depende de tipos de error pgx.
- **Awareness de clave compuesta**: tablas tenanted claveadas `(id, tenant_id)`; foreign keys repiten el par compuesto.
- **Naming**: tablas y columnas en `snake_case`; tipos y funciones Go en estilo idiomático (`CamelCase` / `camelCase`).
- **Comentarios en inglés** (EN).
- **Configuración**: `koanf` (YAML + env) — un solo `configs/config.yaml` versionado, env overrides.

---

## 4. Prohibiciones Explícitas

- No deshabilitar, debilitar, o bypassear FORCE RLS en ninguna tabla tenanted; no correr queries tenanted fuera del helper `WithTenant`.
- No agregar ORM (GORM, sqlc codegen para queries está bien, ent, u similar) — SQL pgx escrito a mano para queries, sqlc solo para generación type-safe.
- No agregar middleware chi que bypassee la cadena estándar (auth → tenant → ratelimit → audit → guardrails → router).
- No guardar refresh tokens ni passwords en plaintext — solo digests SHA-256 y strings Argon2id PHC.
- No permitir flexibilidad de algoritmo JWT — HS256 pinneado; verificación rechaza cualquier otro método (no `alg=none`, no key confusion).
- No agregar `tenant_id` a las tablas globales `public.tenants` y `public.roles` — son globales por diseño.
- No introducir schema-per-tenant ni database-per-tenant — RLS en una instancia compartida es el mecanismo de tenancy.

---

## 5. Roadmap de Referencia (alto nivel)

- [x] **Fase 0** — Fundación: entidades de dominio, schema RLS, primitivas auth, cadena middleware
- [x] **Fase 1** — Rate limiting: Redis token bucket (reqs/tokens/tools), por tenant/user/role
- [x] **Fase 2** — Audit log: append-only + hash chaining + VerifyChain
- [x] **Fase 3** — HITL: state machine + SSE + re-validación al aprobar
- [x] **Fase 4** — Guardrails: interfaz dominio + LocalGuardrail (regex/wordlist/PII)
- [ ] **Fase 5** — Model routing: provider port + fallback + abstracción pricing
- [ ] **Fase 6** — Tool sandbox: interfaz `ToolExecutor` + adapter wazero WASM
- [ ] **Fase 7** — Adapter clasificador guardrail externo
- [ ] **Fase 8** — Pipeline CI/CD + secret management + deploy canary

---

## Cómo Agregar una Decisión

1. Elegí el próximo número libre (`AD-012`, ...) en la sección 2.
2. Copiá el template de abajo, completalo, y mantené la sección ordenada.

```markdown
### AD-XXX · <Título> — Status: Accepted

**Regla**
- <qué el código DEBE hacer, bullets estilo MUST>

**Alternativas rechazadas**
- *<alternativa>*: <razón técnica por la que se rechazó>

**Costo aceptado**
- <trade-off honesto que el proyecto acepta>
```

3. Actualizá el mirror en inglés en `DECISIONS.md` en la misma sesión para que ambos idiomas estén en sync.