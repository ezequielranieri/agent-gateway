package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/usecase/tenant"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// AdminTenantsHandler handles admin tenant operations
type AdminTenantsHandler struct {
	uc     *tenant.UseCase
	logger zerolog.Logger
}

// NewAdminTenantsHandler creates a new admin tenants handler
func NewAdminTenantsHandler(uc *tenant.UseCase, logger zerolog.Logger) *AdminTenantsHandler {
	return &AdminTenantsHandler{
		uc:     uc,
		logger: logger.With().Str("handler", "admin_tenants").Logger(),
	}
}

// RegisterRoutes registers the admin tenant routes
func (h *AdminTenantsHandler) RegisterRoutes(r chi.Router) {
	r.Post("/", h.Create)
	r.Get("/", h.List)
	r.Get("/{id}", h.Get)
	r.Patch("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
}

// Create handles POST /admin/tenants
func (h *AdminTenantsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if req.Name == "" {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.Create(r.Context(), tenant.CreateInput{Name: req.Name})
	if err != nil {
		h.logger.Debug().Err(err).Msg("Failed to create tenant")
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusCreated, out.Tenant)
}

// List handles GET /admin/tenants
func (h *AdminTenantsHandler) List(w http.ResponseWriter, r *http.Request) {
	// Parse query params
	limit := 50
	offset := 0

	out, err := h.uc.List(r.Context(), tenant.ListInput{Limit: limit, Offset: offset})
	if err != nil {
		h.logger.Debug().Err(err).Msg("Failed to list tenants")
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.Tenants)
}

// Get handles GET /admin/tenants/{id}
func (h *AdminTenantsHandler) Get(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.Get(r.Context(), tenant.GetInput{ID: id})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.Tenant)
}

// Update handles PATCH /admin/tenants/{id}
func (h *AdminTenantsHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	var req struct {
		Status string `json:"status"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	status := domain.TenantStatus(req.Status)
	if status != domain.TenantStatusActive && status != domain.TenantStatusSuspended {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.UpdateStatus(r.Context(), tenant.UpdateStatusInput{ID: id, Status: status})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.Tenant)
}

// Delete handles DELETE /admin/tenants/{id}
func (h *AdminTenantsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if err := h.uc.Delete(r.Context(), tenant.DeleteInput{ID: id}); err != nil {
		h.writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminTenantsHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *AdminTenantsHandler) writeError(w http.ResponseWriter, err error) {
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