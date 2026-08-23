package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/usecase/auth"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
)

// AuthHandlers holds the auth handlers
type AuthHandlers struct {
	authUC           *auth.AuthUseCase
	superAdminLoginUC *auth.SuperAdminLoginUseCase
	logger           zerolog.Logger
}

// NewAuthHandlers creates new auth handlers
func NewAuthHandlers(authUC *auth.AuthUseCase, superAdminLoginUC *auth.SuperAdminLoginUseCase, logger zerolog.Logger) *AuthHandlers {
	return &AuthHandlers{
		authUC:            authUC,
		superAdminLoginUC: superAdminLoginUC,
		logger:            logger.With().Str("handler", "auth").Logger(),
	}
}

// RegisterRoutes registers the auth routes
func (h *AuthHandlers) RegisterRoutes(r chi.Router) {
	// Public routes (no auth required)
	r.Post("/auth/login", h.Login)
	r.Post("/auth/super-admin/login", h.SuperAdminLogin)
	r.Post("/auth/refresh", h.Refresh)
	r.Post("/auth/register", h.Register)

	// Protected routes (require auth)
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(middleware.AuthConfig{})) // Will be configured in main
		r.Post("/auth/logout", h.Logout)
		r.Get("/auth/sessions", h.ListSessions)
		r.Delete("/auth/sessions", h.RevokeAllSessions)
	})

	// Admin routes
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(middleware.AuthConfig{}))
		r.Post("/admin/users/{id}/revoke-sessions", h.RevokeUserSessions)
	})
}

// Login handles POST /v1/auth/login
func (h *AuthHandlers) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email     string `json:"email"`
		Password  string `json:"password"`
		TenantID  string `json:"tenant_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	tenantID, err := domain.ParseUUID(req.TenantID)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	loginReq := auth.LoginRequest{
		Email:     req.Email,
		Password:  req.Password,
		TenantID:  tenantID,
		UserAgent: r.UserAgent(),
		IP:        r.RemoteAddr,
	}

	resp, err := h.authUC.Login(r.Context(), loginReq)
	if err != nil {
		h.logger.Debug().Err(err).Msg("Login failed")
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrInvalidCredentials)
		return
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// SuperAdminLogin handles POST /v1/auth/super-admin/login
func (h *AuthHandlers) SuperAdminLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	resp, err := h.superAdminLoginUC.Execute(r.Context(), auth.SuperAdminLoginInput{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		h.logger.Debug().Err(err).Msg("SuperAdmin login failed")
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrInvalidCredentials)
		return
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// Refresh handles POST /v1/auth/refresh
func (h *AuthHandlers) Refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	refreshReq := auth.RefreshRequest{
		RefreshToken: req.RefreshToken,
		UserAgent:    r.UserAgent(),
		IP:           r.RemoteAddr,
	}

	resp, err := h.authUC.Refresh(r.Context(), refreshReq)
	if err != nil {
		h.logger.Debug().Err(err).Msg("Token refresh failed")
		h.writeError(w, r, http.StatusUnauthorized, err)
		return
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// Register handles POST /v1/auth/register
func (h *AuthHandlers) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		TenantID string `json:"tenant_id"`
		Role     string `json:"role"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	tenantID, err := domain.ParseUUID(req.TenantID)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	registerReq := auth.RegisterRequest{
		Email:    req.Email,
		Password: req.Password,
		TenantID: tenantID,
		Role:     req.Role,
	}

	user, err := h.authUC.Register(r.Context(), registerReq)
	if err != nil {
		h.logger.Debug().Err(err).Msg("Registration failed")
		h.writeError(w, r, http.StatusConflict, err)
		return
	}

	h.writeJSON(w, http.StatusCreated, user)
}

// Logout handles POST /v1/auth/logout
func (h *AuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
		RevokeAll    bool   `json:"revoke_all"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	logoutReq := auth.LogoutRequest{
		RefreshToken: req.RefreshToken,
		RevokeAll:    req.RevokeAll,
	}

	if err := h.authUC.Logout(r.Context(), logoutReq); err != nil {
		h.logger.Debug().Err(err).Msg("Logout failed")
		h.writeError(w, r, http.StatusUnauthorized, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]string{"message": "logged out"})
}

// ListSessions handles GET /v1/auth/sessions
func (h *AuthHandlers) ListSessions(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	sessions, err := h.authUC.ListSessions(r.Context(), tenantID, userID)
	if err != nil {
		h.logger.Error().Err(err).Msg("Failed to list sessions")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, sessions)
}

// RevokeAllSessions handles DELETE /v1/auth/sessions
func (h *AuthHandlers) RevokeAllSessions(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	if err := h.authUC.RevokeAllSessions(r.Context(), tenantID, userID); err != nil {
		h.logger.Error().Err(err).Msg("Failed to revoke all sessions")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]string{"message": "all sessions revoked"})
}

// RevokeUserSessions handles POST /v1/admin/users/{id}/revoke-sessions
func (h *AuthHandlers) RevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	userIDStr := chi.URLParam(r, "id")
	userID, err := domain.ParseUUID(userIDStr)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	if err := h.authUC.RevokeAllSessions(r.Context(), tenantID, userID); err != nil {
		h.logger.Error().Err(err).Msg("Failed to revoke user sessions")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]string{"message": "user sessions revoked"})
}

func (h *AuthHandlers) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *AuthHandlers) writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}