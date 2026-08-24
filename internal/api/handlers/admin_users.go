package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/usecase/user"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
)

// AdminUsersHandler handles admin user operations within a tenant
type AdminUsersHandler struct {
	uc     *user.UseCase
	logger zerolog.Logger
}

// NewAdminUsersHandler creates a new admin users handler
func NewAdminUsersHandler(uc *user.UseCase, logger zerolog.Logger) *AdminUsersHandler {
	return &AdminUsersHandler{
		uc:     uc,
		logger: logger.With().Str("handler", "admin_users").Logger(),
	}
}

// RegisterRoutes registers the admin user routes
func (h *AdminUsersHandler) RegisterRoutes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.Get)
	r.Patch("/{id}", h.Update)
	r.Delete("/{id}", h.Delete)
}

// List handles GET /admin/users
func (h *AdminUsersHandler) List(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	// Note: List not implemented in user use case yet
	h.writeError(w, domain.ErrNotImplemented)
}

// Create handles POST /admin/users
func (h *AdminUsersHandler) Create(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if req.Email == "" || req.Password == "" {
		h.writeError(w, domain.ErrValidation)
		return
	}

	// Note: User creation with password hashing should go through auth use case
	// This is a simplified version for admin user creation
	h.writeError(w, domain.ErrNotImplemented)
}

// Get handles GET /admin/users/{id}
func (h *AdminUsersHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	out, err := h.uc.GetByID(r.Context(), user.GetByIDInput{TenantID: tenantID, ID: id})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.User)
}

// Update handles PATCH /admin/users/{id}
func (h *AdminUsersHandler) Update(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	var req struct {
		Email  string `json:"email"`
		Status string `json:"status"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	// Get existing user first
	getOut, err := h.uc.GetByID(r.Context(), user.GetByIDInput{TenantID: tenantID, ID: id})
	if err != nil {
		h.writeError(w, err)
		return
	}

	if req.Email != "" {
		getOut.User.Email = req.Email
	}
	if req.Status != "" {
		getOut.User.Status = domain.UserStatus(req.Status)
	}

	out, err := h.uc.Update(r.Context(), user.UpdateInput{TenantID: tenantID, User: getOut.User})
	if err != nil {
		h.writeError(w, err)
		return
	}

	h.writeJSON(w, http.StatusOK, out.User)
}

// Delete handles DELETE /admin/users/{id}
func (h *AdminUsersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, domain.ErrUnauthorized)
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := domain.ParseUUID(idStr)
	if err != nil {
		h.writeError(w, domain.ErrValidation)
		return
	}

	if err := h.uc.Delete(r.Context(), user.DeleteInput{TenantID: tenantID, ID: id}); err != nil {
		h.writeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminUsersHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *AdminUsersHandler) writeError(w http.ResponseWriter, err error) {
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