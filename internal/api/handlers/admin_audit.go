package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
)

// AdminAuditHandlers holds the admin audit handlers
type AdminAuditHandlers struct {
	auditRepo *postgres.AuditRepository
	logger    zerolog.Logger
}

// NewAdminAuditHandlers creates new admin audit handlers
func NewAdminAuditHandlers(auditRepo *postgres.AuditRepository, logger zerolog.Logger) *AdminAuditHandlers {
	return &AdminAuditHandlers{
		auditRepo: auditRepo,
		logger:    logger.With().Str("handler", "admin_audit").Logger(),
	}
}

// RegisterRoutes registers the admin audit routes
func (h *AdminAuditHandlers) RegisterRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(middleware.AuthConfig{}))
		r.Get("/admin/audit", h.ListAuditEvents)
		r.Post("/admin/audit/verify-chain", h.VerifyChain)
	})
}

// ListAuditEvents handles GET /v1/admin/audit
func (h *AdminAuditHandlers) ListAuditEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "ListAuditEvents").Logger()

	// Get tenant ID from context (admin must have tenant context)
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	// Parse query parameters
	filter := postgres.AuditFilter{
		TenantID: tenantID,
	}

	// Actor ID filter
	if actorIDStr := r.URL.Query().Get("actor_id"); actorIDStr != "" {
		actorID, err := domain.ParseUUID(actorIDStr)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
			return
		}
		filter.ActorID = &actorID
	}

	// Action filter
	if action := r.URL.Query().Get("action"); action != "" {
		filter.Action = action
	}

	// Entity type filter
	if entityType := r.URL.Query().Get("entity_type"); entityType != "" {
		filter.EntityType = entityType
	}

	// Entity ID filter
	if entityIDStr := r.URL.Query().Get("entity_id"); entityIDStr != "" {
		entityID, err := domain.ParseUUID(entityIDStr)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
			return
		}
		filter.EntityID = &entityID
	}

	// Severity filter
	if severity := r.URL.Query().Get("severity"); severity != "" {
		filter.Severity = severity
	}

	// Time range filters
	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		from, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
			return
		}
		filter.From = &from
	}

	if toStr := r.URL.Query().Get("to"); toStr != "" {
		to, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
			return
		}
		filter.To = &to
	}

	// Pagination
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit <= 0 {
			h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
			return
		}
		filter.Limit = limit
	} else {
		filter.Limit = 50 // Default limit
	}

	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		offset, err := strconv.Atoi(offsetStr)
		if err != nil || offset < 0 {
			h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
			return
		}
		filter.Offset = offset
	}

	// Query audit events
	events, err := h.auditRepo.Query(ctx, filter)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to query audit events")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Convert to response format
	response := map[string]interface{}{
		"events": events,
		"count":  len(events),
		"limit":  filter.Limit,
		"offset": filter.Offset,
	}

	h.writeJSON(w, http.StatusOK, response)
}

// VerifyChain handles POST /v1/admin/audit/verify-chain
func (h *AdminAuditHandlers) VerifyChain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "VerifyChain").Logger()

	// Get tenant ID from context
	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	// Parse request body
	var req struct {
		TenantID string `json:"tenant_id"`
		FromSeq  int64  `json:"from_seq"`
		ToSeq    int64  `json:"to_seq"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Use tenant from context if not provided (SuperAdmin can specify any tenant)
	var targetTenantID domain.UUID
	var err error
	if req.TenantID != "" {
		targetTenantID, err = domain.ParseUUID(req.TenantID)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
			return
		}
	} else {
		targetTenantID = tenantID
	}

	// Default to full chain if not specified
	fromSeq := req.FromSeq
	toSeq := req.ToSeq
	if fromSeq == 0 {
		fromSeq = 1
	}
	if toSeq == 0 {
		// Get last event to determine upper bound
		lastEvent, err := h.auditRepo.GetLastEvent(ctx, targetTenantID)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to get last event")
			h.writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		if lastEvent != nil {
			toSeq = lastEvent.Seq
		}
	}

	// Verify chain
	result, err := h.auditRepo.VerifyChain(ctx, targetTenantID, fromSeq, toSeq)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to verify chain")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	response := map[string]interface{}{
		"valid":      result.Valid,
		"from_seq":   fromSeq,
		"to_seq":     toSeq,
		"total_seen": result.TotalSeen,
	}
	if !result.Valid {
		response["broken_seq"] = result.BrokenSeq
		response["error"] = result.Error.Error()
	}

	h.writeJSON(w, http.StatusOK, response)
}

func (h *AdminAuditHandlers) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *AdminAuditHandlers) writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}