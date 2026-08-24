package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/usecase/role"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
)

// AdminRolesHandler handles admin role operations
type AdminRolesHandler struct {
	uc     *role.UseCase
	logger zerolog.Logger
}

// NewAdminRolesHandler creates a new admin roles handler
func NewAdminRolesHandler(uc *role.UseCase, logger zerolog.Logger) *AdminRolesHandler {
	return &AdminRolesHandler{
		uc:     uc,
		logger: logger.With().Str("handler", "admin_roles").Logger(),
	}
}

// RegisterRoutes registers the admin role routes
func (h *AdminRolesHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.Get)
	r.Patch("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
	r.Post("/{id}/permissions", h.AssignPermissions)
	r.Get("/{id}/permissions", h.GetPermissions)
	r.Delete("/{id}/permissions", h.RevokePermissions)
}

// List handles GET /admin/roles
func (h *AdminRolesHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	out, err := h.uc.ListByTenant(r.Context(), role.ListByTenantInput{TenantID: tenantID})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.Roles)
}

// Create handles POST /admin/roles
func (h *AdminRolesHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if req.Name == "" {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.Create(r.Context(), role.CreateInput{Name: req.Name, Description: req.Description})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusCreated, out.Role)
}

// Get handles GET /admin/roles/{id}
func (h *AdminRolesHandler) Get(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.GetByID(r.Context(), role.GetByIDInput{ID: id})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.Role)
}

// Update handles PATCH /admin/roles/{id}
func (h *AdminRolesHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	// Get existing role first
	getOut, err := h.uc.GetByID(r.Context(), role.GetByIDInput{ID: id})
	if err != nil {
		h.writeError(w, err)
		return
	}

	if req.Name != "" {
		getOut.Role.Name = req.Name
	}
	if req.Description != "" {
		getOut.Role.Description = req.Description
	}

	// Note: Update not implemented in role use case yet
	h.writeError(w, domain.ErrNotImplemented)
}

// Delete handles DELETE /admin/roles/{id}
func (h *AdminRolesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if err := h.uc.Delete(r.Context(), role.DeleteInput{ID: id}); err != nil {
		h.writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AssignPermissions handles POST /admin/roles/{id}/permissions
func (h *AdminRolesHandler) AssignPermissions(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	var req struct {
		TenantID    string   `json:"tenant_id"`
		Permissions []string `json:"permissions"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if req.TenantID == "" || len(req.Permissions) == 0 {
		h.writeError(w, domain.ErrValidation)
		return
	}

	tenantID, err := domain.ParseUUID(req.TenantID)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if err := h.uc.AssignPermissions(r.Context(), role.AssignPermissionsInput{
		TenantID:    tenantID,
		RoleID:      id,
		Permissions: req.Permissions,
	}); err != nil {
		h.writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetPermissions handles GET /admin/roles/{id}/permissions
func (h *AdminRolesHandler) GetPermissions(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	tenantIDStr := chi.URLParam(r, "tenant_id")
	if tenantIDStr == "" {
		h.writeError(w, domain.ErrValidation)
		return
	}

	tenantID, err := domain.ParseUUID(tenantIDStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.GetPermissions(r.Context(), role.GetPermissionsInput{
		TenantID: tenantID,
		RoleID:   id,
	})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.Permissions)
}

// RevokePermissions handles DELETE /admin/roles/{id}/permissions
func (h *AdminRolesHandler) RevokePermissions(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	tenantIDStr := chi.URLParam(r, "tenant_id")
	if tenantIDStr == "" {
		h.writeError(w, domain.ErrValidation)
		return
	}

	tenantID, err := domain.ParseUUID(tenantIDStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if err := h.uc.RevokePermissions(r.Context(), role.RevokePermissionsInput{
		TenantID: tenantID,
		RoleID:   id,
	}); err != nil {
		h.writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminRolesHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *AdminRolesHandler) writeError(w http.ResponseWriter, err error) {
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