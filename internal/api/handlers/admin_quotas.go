package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/usecase/quota"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
)

// AdminQuotasHandler handles admin quota operations
type AdminQuotasHandler struct {
	uc     *quota.UseCase
	logger zerolog.Logger
}

// NewAdminQuotasHandler creates a new admin quotas handler
func NewAdminQuotasHandler(uc *quota.UseCase, logger zerolog.Logger) *AdminQuotasHandler {
	return &AdminQuotasHandler{
		uc:     uc,
		logger: logger.With().Str("handler", "admin_quotas").Logger(),
	}
}

// RegisterRoutes registers the admin quota routes
func (h *AdminQuotasHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.Get)
	r.Patch("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
}

// List handles GET /admin/quotas
func (h *AdminQuotasHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	out, err := h.uc.ListByTenant(r.Context(), quota.ListByTenantInput{TenantID: tenantID})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.Quotas)
}

// Create handles POST /admin/quotas
func (h *AdminQuotasHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	var req struct {
		Scope           string `json:"scope"`            // tenant, user, role
		ScopeID         string `json:"scope_id"`         // tenant_id, user_id, or role_id
		RequestsPerMin  int    `json:"requests_per_min"`
		TokensPerMin    int    `json:"tokens_per_min"`
		ToolExecsPerMin int    `json:"tool_execs_per_min"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	scopeID, err := domain.ParseUUID(req.ScopeID)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if req.RequestsPerMin <= 0 || req.TokensPerMin <= 0 || req.ToolExecsPerMin <= 0 {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.Create(r.Context(), quota.CreateInput{
		TenantID:         tenantID,
		Scope:            domain.QuotaScope(req.Scope),
		ScopeID:          scopeID,
		RequestsPerMin:   req.RequestsPerMin,
		TokensPerMin:     req.TokensPerMin,
		ToolExecsPerMin:  req.ToolExecsPerMin,
	})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusCreated, out.Quota)
}

// Get handles GET /admin/quotas/{id}
func (h *AdminQuotasHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	// For quotas, the ID is composite (scope + scope_id), but we'll use the path param as scope
	scope := domain.QuotaScope(chi.URLParam(r, "id"))
	scopeIDStr := chi.URLParam(r, "scope_id")
	if scopeIDStr == "" {
		h.writeError(w, domain.ErrValidation)
		return
	}

	scopeID, err := domain.ParseUUID(scopeIDStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.GetByScope(r.Context(), quota.GetByScopeInput{
		TenantID: tenantID,
		Scope:    scope,
		ScopeID:  scopeID,
	})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.Quota)
}

// Update handles PATCH /admin/quotas/{id}
func (h *AdminQuotasHandler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	scopeIDStr := chi.URLParam(r, "scope_id")
	if scopeIDStr == "" {
		h.writeError(w, domain.ErrValidation)
		return
	}

	scopeID, err := domain.ParseUUID(scopeIDStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	var req struct {
		RequestsPerMin  int `json:"requests_per_min"`
		TokensPerMin    int `json:"tokens_per_min"`
		ToolExecsPerMin int `json:"tool_execs_per_min"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if req.RequestsPerMin <= 0 || req.TokensPerMin <= 0 || req.ToolExecsPerMin <= 0 {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.Update(r.Context(), quota.UpdateInput{
		TenantID:         tenantID,
		Scope:            domain.QuotaScope(chi.URLParam(r, "id")),
		ScopeID:          scopeID,
		RequestsPerMin:   req.RequestsPerMin,
		TokensPerMin:     req.TokensPerMin,
		ToolExecsPerMin:  req.ToolExecsPerMin,
	})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.Quota)
}

// Delete handles DELETE /admin/quotas/{id}
func (h *AdminQuotasHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	scopeIDStr := chi.URLParam(r, "scope_id")
	if scopeIDStr == "" {
		h.writeError(w, domain.ErrValidation)
		return
	}

	scopeID, err := domain.ParseUUID(scopeIDStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if err := h.uc.Delete(r.Context(), quota.DeleteInput{
		TenantID: tenantID,
		Scope:    domain.QuotaScope(chi.URLParam(r, "id")),
		ScopeID:  scopeID,
	}); err != nil {
		h.writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminQuotasHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *AdminQuotasHandler) writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	switch err {
	case domain.ErrNotFound:
		w.WriteHeader(http.StatusNotFound)
	case domain.ErrConflict:
		w.WriteHeader(http.StatusConflict)
	case domain.ErrValidation:
		w.WriteHeader(http.StatusBadRequest)
	case domain.ErrUnauthorized:
		w.WriteHeader(http.StatusUnauthorized)
	case domain.ErrForbidden:
		w.WriteHeader(http.StatusForbidden)
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}