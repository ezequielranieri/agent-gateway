# SPEC — agent-gateway v1

**Estado:** Cerrada — fuente de verdad funcional del proyecto
**Fecha:** 2026-08-22

## Propósito

Gateway / Control Plane multi-tenant para agentes LLM — la **única capa intermedia** entre la aplicación y el modelo/agent. Ninguna llamada puede saltearla. Operar agentes de forma segura y controlada a escala (no solo construirlos).

## Planos del sistema

El sistema separa explícitamente dos planos, siguiendo el patrón de sistemas multi-tenant serios:

- **Plano de control (plataforma):** Super Admin, gestión de tenants, cuotas globales.
- **Plano de datos (tenant):** Users, Roles, Permissions, Quotas, AuditEvents, ReviewRequests, GuardrailViolations — todo scopeado a un tenant específico.

Estos planos no se mezclan: un Super Admin no es un "admin de tenant" con privilegios extendidos, es una entidad separada, global, sin `tenant_id`.

## Entidades core

| Entidad | Descripción | Notas |
|---|---|---|
| **SuperAdmin** | Entidad global de plataforma | Sin `tenant_id`. Único mecanismo capaz de crear tenants. |
| **Tenant** | Organización/cliente | id, nombre, estado (activo/suspendido), cuotas globales |
| **User** | Usuario dentro de un tenant | id, tenant_id, email, password_hash (Argon2id), estado, roles |
| **Role** | Rol dentro de un tenant | id, tenant_id, nombre (ej: admin, operator, viewer) |
| **Permission** | Acción sobre recurso | Formato `recurso:accion` (ej: `models:call`, `tools:execute`) |
| **Quota** | Límites por tenant/user/role | requests/min, tokens/min, tool_execs/min |
| **AuditEvent** | Evento de auditoría inmutable | hash-chained per tenant, severity (info/warn/critical) |
| **ReviewRequest** | Solicitud HITL | state machine: PENDING → APPROVED/REJECTED/EXPIRED |
| **GuardrailViolation** | Violación de guardrail | input/output, severity, regla que disparó |

## Flujo de provisioning (decisión cerrada)

```
Super Admin      → entidad/flag global (sin tenant_id)
Creación Tenant  → solo por Super Admin (provisioning manual)
Registro Usuario → dentro de un tenant ya existente (no crea tenant)
Primer usuario   → se le asigna explícitamente el rol "admin" del tenant al crearlo
```

**Por qué:** el servicio es interno, no público. Self-service (creación de tenant al registrarse) es el patrón correcto para SaaS B2B público, pero introduce complejidad de onboarding/billing/límites que no aplica aquí. Provisioning manual reduce superficie de ataque y mantiene el control dentro de la organización.

## Casos de uso (MVP)

### Auth & Tenancy
1. **Bootstrap**: seed / comando CLI para crear el primer Super Admin en un entorno nuevo.
2. **Creación de Tenant**: solo por Super Admin.
3. **Registro de usuario**: dentro de un tenant existente; si es el primer usuario del tenant, se le asigna rol `admin` explícitamente.
4. **Login** (email + password + tenant_id) → access token (JWT HS256) + refresh token (opaco).
5. **Refresh de sesión** con rotación obligatoria + detección de reuse (breach detection vía `family_id`).
6. **Logout**: revocar el refresh token / familia actual.
7. **Revocación forzada de todas las sesiones** de un usuario (uso: compromiso detectado, desactivación de cuenta).
8. **Listar sesiones activas** de un usuario (basado en metadata de `RefreshToken`).

### Gateway Core
9. **Endpoint `/v1/chat/completions`**: única entrada para llamadas a LLM — auth → tenant → ratelimit → audit → guardrails → model router.
10. **Rate limiting** en 3 dimensiones: requests/min, tokens/min, tool_execs/min — por tenant, user, role (precedencia configurable).
11. **Audit log** completo e inmutable: llamadas al modelo, ejecución de tools, decisiones de HITL, violaciones de guardrail — hash-chained per tenant.
12. **Guardrails input/output**: validación antes y después de la llamada al modelo — interfaz `Guardrail` + `LocalGuardrail` (regex/wordlist/PII/injection) por defecto.

### Human-in-the-Loop
13. **Crear request de aprobación**: el agente propone una acción de escritura → se crea `ReviewRequest` `PENDING` con token opaco (SHA-256 guardado).
14. **Aprobar/Rechazar**: humano presenta token → timing-safe compare → re-validación completa (payload, IDs, tenant) → materializar o rechazar.
15. **SSE streaming**: status en tiempo real (`/v1/reviews/{id}/stream`) — `curl -N` compatible.

### Admin & Observability
16. **CRUD de roles, permisos, cuotas** (solo admin del tenant, scoped a su propio tenant).
17. **Consulta de audit log** con filtros (tenant, actor, acción, entidad, rango temporal, severidad).
18. **Verificación de cadena de hash** (`VerifyChain`) — detector de manipulación interno.
19. **Health/Readiness**: `/health` (liveness), `/ready` (DB + Redis connectivity).

## Modelo de permisos

- Granularidad: acción sobre recurso (`recurso:accion`).
- Scope: permisos definidos y asignables solo dentro del tenant al que pertenecen.
- Fuera de alcance en MVP: permisos a nivel de objeto concreto (ABAC/ReBAC) — se evalúa solo si aparece requisito real.

## Fuera de alcance (v1) — deuda técnica / roadmap anotado

| Ítem | Motivo de exclusión |
|---|---|
| OIDC/OAuth2 provider completo para terceros | No es el posicionamiento del servicio (interno, no público) |
| MFA (TOTP/WebAuthn) | No requerido en MVP; agregar cuando exista necesidad real |
| SSO (SAML, Google/Microsoft login) | Mismo criterio que MFA |
| Service Accounts / M2M (client_credentials) | Anotado como deuda: probable necesidad futura cuando otros servicios necesiten autenticarse como "máquina" y no como usuario humano |
| UI de administración | No forma parte del MVP (servicio expone solo API) |
| Model routing / fallback (GPT-4o → Claude → Llama local) | Requiere provider port + pricing abstraction primero — Fase 5 |
| Pricing / cost abstraction (model + tokens → USD) | Precios cambian por provider; necesita config versionado — Fase 5 |
| Tool sandbox (WASM via wazero / gVisor) | Aislamiento de ejecución es boundary separado — Fase 6 |
| Clasificador externo de guardrails (Claude API / Llama Guard) | Agrega dependencia de red, latencia, costo, API keys — Fase 7 |
| Schema-per-tenant isolation | RLS en instancia compartida basta para threat model actual |
| Auditoría avanzada / event sourcing completo | Se evalúa si aparece requisito de compliance o trazabilidad extendida |

## No-funcionales

Definidos íntegramente por el stack tecnológico cerrado en `STACK.md` (fuente de verdad técnica, en la raíz del repo): seguridad (JWT HS256, Argon2id, rate limiting Redis token bucket), performance, observabilidad (zerolog + Prometheus + OpenTelemetry), testing (testcontainers-go), documentación de contrato (OpenAPI 3.1).

## Deuda técnica explícita (issues abiertos desde el día 1)

1. **Redis SPOF** — instancia única en MVP. Criterio de salida: migrar a Sentinel/Cluster antes de producción real.
2. **Multi-tenancy isolation** — actualmente `tenant_id` + RLS FORCE + PK compuesta. Criterio de salida: evaluar schema-per-tenant solo si aparece requisito real de aislamiento fuerte.
3. **Tool Sandbox** — no implementado. Criterio: interfaz `ToolExecutor` + `WasmExecutor` (wazero) implementados.
4. **Model Routing / Fallback** — no implementado. Criterio: provider port + `PricingService` implementados.
5. **External Guardrail Classifier** — no implementado. Criterio: adapter `ExternalClassifier` implementado.