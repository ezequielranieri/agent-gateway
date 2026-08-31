# STACK.md

**Estado:** Cerrado — fuente de verdad técnica del proyecto

## Core

| Capa | Tecnología | Notas |
|---|---|---|
| Lenguaje | Go 1.22+ | Concurrencia nativa, bajo overhead |
| Arquitectura | Clean / Hexagonal | Dominio independiente de frameworks |
| HTTP | chi | Minimalista, sin opiniones de más |
| Validación | go-playground/validator | Estructuras tipadas + validación declarativa |

## Persistencia y Caché

| Capa | Tecnología | Notas |
|---|---|---|
| Base de datos | PostgreSQL 16 | Fuente de verdad (ACID) + Row Level Security FORCE como defensa en profundidad. Multi-tenancy vía tenant_id + PK compuesta |
| Queries | sqlc + pgx | Complementarios: sqlc genera código type-safe sobre pgx |
| Migraciones | goose | Obligatorio — sqlc no gestiona schema |
| Caché / Rate Limit | Redis 7 + redis_rate | Token bucket (requests, tokens, tool_execs) |

## Seguridad

| Aspecto | Decisión |
|---|---|
| Access Token | JWT HS256 (multi-key rotation via `kid`, 15 min TTL, algorithm pinned) |
| Refresh Token | Opaco + rotación obligatoria + detección de reuse (family_id) |
| Password Hashing | Argon2id (m=65536, t=1, p=4, 32-byte key, PHC format) |
| Key Management | Múltiples claves activas (kid) + rotación |
| Rate Limiting | Redis + redis_rate (token bucket), 3 dimensiones: requests, tokens, tool_execs por tenant/user/role |
| Propagación de identidad | JWT interno de corta duración firmado (nunca headers planos) |

## Observabilidad

| Tipo | Herramienta |
|---|---|
| Logs | zerolog (JSON estructurado) |
| Métricas | Prometheus (`/metrics` endpoint) |
| Trazas | OpenTelemetry (stdout exporter para dev) |
| Healthchecks | /health, /ready (live DB + Redis) |
| Dashboards | Grafana (auto-provisioned) |
| Alertas | Alertmanager (email, Slack, PagerDuty) |
| Logs centralizados | Loki + Promtail |
| Trazas distribuidas | Jaeger |

## Testing

| Tipo | Herramienta |
|---|---|
| Unit | testify + table-driven + mocks en puertos (sin DB) |
| Integración | testcontainers-go (PostgreSQL + Redis reales en CI, no mocks) |

## Contrato de API

OpenAPI 3.1, definido desde el día 1. Handlers generados con `oapi-codegen`.

## Infraestructura (MVP)

| Capa | Tecnología |
|---|---|
| Local / Demo | Docker Compose (Postgres 16 + Redis 7 + Gateway) |
| CI | GitHub Actions (test, build, secret scan gitleaks, vulnerability scan Trivy) |
| Secretos | Variables de entorno + .env.example — nada hardcodeado |
| Registry | GHCR (ghcr.io) — build & push en push a master |

## Explícitamente fuera del MVP

- Terraform / Kubernetes
- gRPC (se puede agregar después)
- Service mesh
- Multi-region
- UI de administración compleja

## Deuda técnica explícita

1. **Redis SPOF** — instancia única en MVP. Criterio de salida: migrar a Sentinel/Cluster antes de producción real.
2. **Schema-per-tenant isolation** — RLS en instancia compartida basta. Criterio: evaluar solo ante requisito real de aislamiento fuerte.

## Auditoría de seguridad en CI

El pipeline incluye un job `secrets` (gitleaks) que escanea el diff de cada Pull Request y el rango pusheado en cada push a `master`, bloqueando el merge ante cualquier credencial detectada. No escanea el historial completo del repositorio en cada push (decisión deliberada: evita que un hallazgo histórico ya resuelto genere fricción recurrente en cada push futuro — "alert fatigue"). Si en el futuro se requiere una auditoría periódica de historial completo, debe implementarse como job programado (`schedule`) independiente del flujo normal de CI, no atado a cada push.

## Decisión: Rate Limiter fail-open ante caída de Redis

Dado que Redis es un SPOF documentado (ítem 1 arriba), el rate limiter (`internal/adapter/redis`) adopta **fail-open** por defecto: si Redis está inaccesible, las requests se permiten sin throttling, en vez de bloquear el servicio completo.

**Por qué:** un atacante sin credenciales válidas no gana acceso solo porque el rate limiting esté temporalmente desactivado — Argon2id ya hace cada intento individual computacionalmente costoso, y el rate limiter es una capa de mitigación adicional (contra fuerza bruta/credential stuffing), no la única defensa. Para un gateway multi-tenant interno, priorizar disponibilidad sobre una protección temporalmente degradada es el trade-off correcto. Entornos que prefieran cerrar el servicio ante esta situación pueden invertir el comportamiento con config.

Esta decisión queda resuelta automáticamente si se resuelve el ítem 1 (Sentinel/Cluster) — con Redis en alta disponibilidad, el escenario de "Redis inaccesible" se vuelve mucho menos probable y el trade-off pierde relevancia práctica.