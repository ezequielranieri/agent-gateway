package api

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/api/handlers"
	"github.com/ezequielranieri/agent-gateway/internal/config"
)

// RouterConfig holds dependencies for the router
type RouterConfig struct {
	Config                 *config.Config
	Logger                 zerolog.Logger
	AuthMW                 func(http.Handler) http.Handler
	TenantMW               func(http.Handler) http.Handler
	RateLimitMW            func(http.Handler) http.Handler
	AuditMW                func(http.Handler) http.Handler
	GuardrailsMW           func(http.Handler) http.Handler
	HITLMW                 func(http.Handler) http.Handler
	AuthHandlers           *handlers.AuthHandlers
	ReviewHandlers         *handlers.ReviewHandlers
	ChatHandlers           *handlers.ChatHandlers
	AdminAuditHandlers     *handlers.AdminAuditHandlers
	AdminTenantsHandler    *handlers.AdminTenantsHandler
	AdminUsersHandler      *handlers.AdminUsersHandler
	AdminRolesHandler      *handlers.AdminRolesHandler
	AdminQuotasHandler     *handlers.AdminQuotasHandler
}

// NewRouter creates a new chi router with all middleware and routes
func NewRouter(cfg RouterConfig) *chi.Mux {
	r := chi.NewRouter()

	// Base middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(cfg.Config.Server.ReadTimeout))

	// Health endpoints (no auth required)
	r.Get("/health", healthHandler)
	r.Get("/ready", readyHandler)

	// API v1 routes with middleware chain
	r.Route("/v1", func(r chi.Router) {
		// Auth endpoints (login, refresh, register) - no auth required
		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", cfg.AuthHandlers.Login)
			r.Post("/super-admin/login", cfg.AuthHandlers.SuperAdminLogin)
			r.Post("/refresh", cfg.AuthHandlers.Refresh)
			r.Post("/register", cfg.AuthHandlers.Register)

			// Protected auth endpoints
			r.Group(func(r chi.Router) {
				r.Use(cfg.AuthMW)
				r.Post("/logout", cfg.AuthHandlers.Logout)
				r.Get("/sessions", cfg.AuthHandlers.ListSessions)
				r.Delete("/sessions", cfg.AuthHandlers.RevokeAllSessions)
			})
		})

		// SuperAdmin + Tenant admin routes
		r.Route("/admin", func(r chi.Router) {
			// SuperAdmin tenant management (no tenant required)
			r.Route("/tenants", func(r chi.Router) {
				r.Use(cfg.AuthMW)
				if cfg.AdminTenantsHandler != nil {
					cfg.AdminTenantsHandler.RegisterRoutes(r)
				} else {
					// Fallback to placeholders
					r.Post("/", createTenantHandler)
					r.Get("/", listTenantsHandler)
					r.Get("/{id}", getTenantHandler)
					r.Patch("/{id}", updateTenantHandler)
					r.Delete("/{id}", deleteTenantHandler)
				}
			})

			// Tenant admin routes (tenant-scoped, require tenant context)
			r.Group(func(r chi.Router) {
				// Apply tenant middleware chain
				r.Use(cfg.AuthMW)
				r.Use(cfg.TenantMW)
				r.Use(cfg.RateLimitMW)
				r.Use(cfg.AuditMW)
				r.Use(cfg.GuardrailsMW)
				r.Use(cfg.HITLMW)

				if cfg.AdminUsersHandler != nil {
					cfg.AdminUsersHandler.RegisterRoutes(r)
				} else {
					// Fallback to placeholders
					r.Get("/users", listUsersHandler)
					r.Post("/users", createUserHandler)
					r.Get("/users/{id}", getUserHandler)
					r.Patch("/users/{id}", updateUserHandler)
					r.Delete("/users/{id}", deleteUserHandler)
				}

				if cfg.AdminRolesHandler != nil {
					cfg.AdminRolesHandler.RegisterRoutes(r)
				} else {
					// Fallback to placeholders
					r.Get("/roles", listRolesHandler)
					r.Post("/roles", createRoleHandler)
					r.Get("/roles/{id}", getRoleHandler)
					r.Patch("/roles/{id}", updateRoleHandler)
					r.Delete("/roles/{id}", deleteRoleHandler)
				}

				if cfg.AdminQuotasHandler != nil {
					cfg.AdminQuotasHandler.RegisterRoutes(r)
				} else {
					// Fallback to placeholders
					r.Get("/quotas", listQuotasHandler)
					r.Post("/quotas", createQuotaHandler)
					r.Get("/quotas/{id}", getQuotaHandler)
					r.Patch("/quotas/{id}", updateQuotaHandler)
					r.Delete("/quotas/{id}", deleteQuotaHandler)
				}

				// Admin-only: revoke user sessions
				r.Post("/users/{id}/revoke-sessions", cfg.AuthHandlers.RevokeUserSessions)

				// Audit routes
				if cfg.AdminAuditHandlers != nil {
					r.Get("/audit", cfg.AdminAuditHandlers.ListAuditEvents)
					r.Post("/audit/verify-chain", cfg.AdminAuditHandlers.VerifyChain)
				}
			})
		})

		// Review routes (HITL) - require full middleware chain
		r.Route("/reviews", func(r chi.Router) {
			r.Use(cfg.AuthMW)
			r.Use(cfg.TenantMW)
			r.Use(cfg.RateLimitMW)
			r.Use(cfg.AuditMW)
			r.Use(cfg.GuardrailsMW)
			r.Use(cfg.HITLMW)

			if cfg.ReviewHandlers != nil {
				r.Post("/", cfg.ReviewHandlers.CreateReview)
				r.Get("/", cfg.ReviewHandlers.ListReviews)
				r.Get("/{id}", cfg.ReviewHandlers.GetReview)
				r.Post("/{id}/approve", cfg.ReviewHandlers.ApproveReview)
				r.Post("/{id}/reject", cfg.ReviewHandlers.RejectReview)
				r.Patch("/{id}", cfg.ReviewHandlers.ExecuteReview)
				r.Get("/{id}/stream", cfg.ReviewHandlers.StreamReview)      // SSE with ticket auth
				r.Get("/{id}/status", cfg.ReviewHandlers.GetReviewStatus)  // Polling for agents
			} else {
				// Fallback to placeholders
				r.Post("/", createReviewHandler)
				r.Get("/", listReviewsHandler)
				r.Get("/{id}", getReviewHandler)
				r.Post("/{id}/approve", approveReviewHandler)
				r.Post("/{id}/reject", rejectReviewHandler)
				r.Get("/{id}/stream", streamReviewHandler)
			}
		})

		// Chat/Completions route (gateway core) - require full middleware chain
		r.Route("/chat", func(r chi.Router) {
			r.Use(cfg.AuthMW)
			r.Use(cfg.TenantMW)
			r.Use(cfg.RateLimitMW)
			r.Use(cfg.AuditMW)
			r.Use(cfg.GuardrailsMW)
			r.Use(cfg.HITLMW)

			r.Post("/completions", cfg.ChatHandlers.ChatCompletions)
		})
	})

	return r
}

// Health handler - liveness probe
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// Ready handler - readiness probe (checks DB + Redis)
func readyHandler(w http.ResponseWriter, r *http.Request) {
	// In implementation, check DB and Redis connectivity
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

// Admin handlers (placeholders - to be implemented in later phases)
func listTenantsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"list tenants placeholder"}`))
}

func createTenantHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	id := newUUID()
	_, _ = w.Write([]byte(`{"id":"` + id + `","name":"Acme Corp","status":"active","created_at":"` + time.Now().Format(time.RFC3339) + `"}`))
}

// newUUID generates a simple UUID v4 string
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func getTenantHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"get tenant placeholder"}`))
}

func updateTenantHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"update tenant placeholder"}`))
}

func deleteTenantHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"delete tenant placeholder"}`))
}

func listUsersHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"list users placeholder"}`))
}

func createUserHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"create user placeholder"}`))
}

func getUserHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"get user placeholder"}`))
}

func updateUserHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"update user placeholder"}`))
}

func deleteUserHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"delete user placeholder"}`))
}

func listRolesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"list roles placeholder"}`))
}

func createRoleHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"create role placeholder"}`))
}

func getRoleHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"get role placeholder"}`))
}

func updateRoleHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"update role placeholder"}`))
}

func deleteRoleHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"delete role placeholder"}`))
}

func listPermissionsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"list permissions placeholder"}`))
}

func createPermissionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"create permission placeholder"}`))
}

func listQuotasHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"list quotas placeholder"}`))
}

func createQuotaHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"create quota placeholder"}`))
}

func getQuotaHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"get quota placeholder"}`))
}

func updateQuotaHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"update quota placeholder"}`))
}

func deleteQuotaHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"delete quota placeholder"}`))
}

// Review handlers (placeholders)
func createReviewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"create review placeholder"}`))
}

func listReviewsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"list reviews placeholder"}`))
}

func getReviewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"get review placeholder"}`))
}

func approveReviewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"approve review placeholder"}`))
}

func rejectReviewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"message":"reject review placeholder"}`))
}

func streamReviewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, _ = w.Write([]byte("data: {\"status\":\"PENDING\"}\n\n"))
}