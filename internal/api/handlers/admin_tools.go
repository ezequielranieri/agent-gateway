package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
)

// AdminToolsHandler holds the admin tools handlers
type AdminToolsHandler struct {
	toolRepo *postgres.ToolRepository
	logger   zerolog.Logger
}

// NewAdminToolsHandler creates new admin tools handlers
func NewAdminToolsHandler(toolRepo *postgres.ToolRepository, logger zerolog.Logger) *AdminToolsHandler {
	return &AdminToolsHandler{
		toolRepo: toolRepo,
		logger:   logger.With().Str("handler", "admin_tools").Logger(),
	}
}

// RegisterRoutes registers the admin tools routes
func (h *AdminToolsHandler) RegisterRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(middleware.AuthConfig{}))
		r.Post("/admin/tools", h.CreateTool)
		r.Patch("/admin/tools/{name}", h.UpdateTool)
		r.Delete("/admin/tools/{name}", h.DeactivateTool)
	})
}

// CreateToolRequest represents the request to create a tool definition
type CreateToolRequest struct {
	TenantID             string          `json:"tenant_id" validate:"required"`
	Name                 string          `json:"name" validate:"required"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters" validate:"required"`
	Grants               json.RawMessage `json:"grants,omitempty"`
	ExecutionTimeoutMs   *uint64         `json:"execution_timeout_ms,omitempty"`
	MemoryPages          *uint32         `json:"memory_pages,omitempty"`
}

// UpdateToolRequest represents the request to update a tool definition
type UpdateToolRequest struct {
	TenantID             string           `json:"tenant_id" validate:"required"`
	Description          *string          `json:"description,omitempty"`
	Parameters           *json.RawMessage `json:"parameters,omitempty"`
	Grants               *json.RawMessage `json:"grants,omitempty"`
	ExecutionTimeoutMs   *uint64          `json:"execution_timeout_ms,omitempty"`
	MemoryPages          *uint32          `json:"memory_pages,omitempty"`
	IsActive             *bool            `json:"is_active,omitempty"`
}

// ToolResponse represents a tool definition in API responses
type ToolResponse struct {
	ID                  int64           `json:"id"`
	TenantID            string          `json:"tenant_id"`
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	Parameters          json.RawMessage `json:"parameters"`
	Grants              json.RawMessage `json:"grants"`
	ExecutionTimeoutMs  uint64          `json:"execution_timeout_ms"`
	MemoryPages         uint32          `json:"memory_pages"`
	Hash                string          `json:"hash"`
	IsActive            bool            `json:"is_active"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
}

// CreateTool handles POST /admin/tools
func (h *AdminToolsHandler) CreateTool(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "CreateTool").Logger()

	// Parse request
	var req CreateToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Validate tenant_id
	tenantID, err := domain.ParseUUID(req.TenantID)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Treat nil parameters as empty object
	if req.Parameters == nil {
		req.Parameters = json.RawMessage(`{}`)
	}
	if req.Grants == nil {
		req.Grants = json.RawMessage(`[]`)
	}

	// Compute hash from definition fields
	hash := tool.ComputeHash(req.Name, req.Description, req.Parameters)

	// Build tool definition
	def := &tool.ToolDefinition{
		TenantID:            tenantID,
		Name:                req.Name,
		Description:         req.Description,
		InputSchema:         req.Parameters,
		Grants:              req.Grants,
		ExecutionTimeoutMs:  0,
		MemoryPages:         0,
		Hash:                hash,
		IsActive:            true,
	}
	if req.ExecutionTimeoutMs != nil {
		def.ExecutionTimeoutMs = *req.ExecutionTimeoutMs
	}
	if req.MemoryPages != nil {
		def.MemoryPages = *req.MemoryPages
	}

	// Create tool definition (includes audit emission and cache invalidation)
	if err := h.toolRepo.CreateToolDefinition(ctx, tenantID, def); err != nil {
		logger.Error().Err(err).Str("tool", req.Name).Msg("Failed to create tool definition")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Return created tool with computed hash
	resp := h.toToolResponse(def)
	h.writeJSON(w, http.StatusCreated, resp)
}

// UpdateTool handles PATCH /admin/tools/{name}
func (h *AdminToolsHandler) UpdateTool(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "UpdateTool").Logger()

	// Get tool name from URL
	name := chi.URLParam(r, "name")
	if name == "" {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Parse request
	var req UpdateToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Validate tenant_id
	tenantID, err := domain.ParseUUID(req.TenantID)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Get existing tool to preserve unchanged fields
	existing, err := h.toolRepo.GetByName(ctx, tenantID, name)
	if err != nil {
		if err == tool.ErrToolNotFound {
			h.writeError(w, r, http.StatusNotFound, domain.ErrNotFound)
			return
		}
		logger.Error().Err(err).Str("tool", name).Msg("Failed to get existing tool")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Build updated definition (merge with existing)
	def := &tool.ToolDefinition{
		ID:           existing.ID,
		TenantID:            tenantID,
		Name:                name,
		Description:         existing.Description,
		InputSchema:         existing.InputSchema,
		Grants:              existing.Grants,
		ExecutionTimeoutMs:  existing.ExecutionTimeoutMs,
		MemoryPages:         existing.MemoryPages,
		Hash:                existing.Hash,
		IsActive:            existing.IsActive,
	}

	// Apply updates
	if req.Description != nil {
		def.Description = *req.Description
	}
	if req.Parameters != nil {
		def.InputSchema = *req.Parameters
	}
	if req.Grants != nil {
		def.Grants = *req.Grants
	}
	if req.ExecutionTimeoutMs != nil {
		def.ExecutionTimeoutMs = *req.ExecutionTimeoutMs
	}
	if req.MemoryPages != nil {
		def.MemoryPages = *req.MemoryPages
	}
	if req.IsActive != nil {
		def.IsActive = *req.IsActive
	}

	// Recompute hash if definition fields changed
	def.Hash = tool.ComputeHash(def.Name, def.Description, def.InputSchema)

	// Update tool definition (includes audit emission and cache invalidation)
	hashChanged, err := h.toolRepo.UpdateToolDefinition(ctx, tenantID, def)
	if err != nil {
		logger.Error().Err(err).Str("tool", name).Msg("Failed to update tool definition")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Log hash change for audit verification
	if hashChanged {
		logger.Info().Str("tool", name).Str("new_hash", def.Hash).Msg("Tool definition hash changed")
	}

	// Return updated tool with new hash
	resp := h.toToolResponse(def)
	h.writeJSON(w, http.StatusOK, resp)
}

// DeactivateTool handles DELETE /admin/tools/{name}
func (h *AdminToolsHandler) DeactivateTool(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "DeactivateTool").Logger()

	// Get tool name from URL
	name := chi.URLParam(r, "name")
	if name == "" {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Get tenant_id from query param (super-admin must specify)
	tenantIDStr := r.URL.Query().Get("tenant_id")
	if tenantIDStr == "" {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	tenantID, err := domain.ParseUUID(tenantIDStr)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Deactivate tool definition (includes audit emission and cache invalidation)
	if err := h.toolRepo.DeactivateToolDefinition(ctx, tenantID, name); err != nil {
		if err == tool.ErrToolNotFound {
			h.writeError(w, r, http.StatusNotFound, domain.ErrNotFound)
			return
		}
		logger.Error().Err(err).Str("tool", name).Msg("Failed to deactivate tool definition")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Return 204 No Content
	w.WriteHeader(http.StatusNoContent)
}

// toToolResponse converts domain ToolDefinition to API response
func (h *AdminToolsHandler) toToolResponse(def *tool.ToolDefinition) ToolResponse {
	return ToolResponse{
		ID:                  def.ID,
		TenantID:            def.TenantID.String(),
		Name:                def.Name,
		Description:         def.Description,
		Parameters:          def.InputSchema,
		Grants:              def.Grants,
		ExecutionTimeoutMs:  def.ExecutionTimeoutMs,
		MemoryPages:         def.MemoryPages,
		Hash:                def.Hash,
		IsActive:            def.IsActive,
		CreatedAt:    def.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
		UpdatedAt:    def.UpdatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
	}
}

func (h *AdminToolsHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *AdminToolsHandler) writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}