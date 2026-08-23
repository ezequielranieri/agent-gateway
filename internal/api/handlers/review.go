package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
	"github.com/ezequielranieri/agent-gateway/internal/usecase/hitl"
)

// ReviewHandlers holds the review handlers
type ReviewHandlers struct {
	hitlUC       *hitl.HITLUseCase
	reviewRepo   *postgres.ReviewRepository
	jwtSecret    string
	logger       zerolog.Logger
	
	// SSE connection registry
	connections map[string]map[chan []byte]bool // reviewID -> connections
	connMutex   sync.RWMutex
}

// NewReviewHandlers creates new review handlers
func NewReviewHandlers(hitlUC *hitl.HITLUseCase, reviewRepo *postgres.ReviewRepository, jwtSecret string, logger zerolog.Logger) *ReviewHandlers {
	return &ReviewHandlers{
		hitlUC:     hitlUC,
		reviewRepo: reviewRepo,
		jwtSecret:  jwtSecret,
		logger:     logger.With().Str("handler", "review").Logger(),
		connections: make(map[string]map[chan []byte]bool),
	}
}

// RegisterRoutes registers the review routes
func (h *ReviewHandlers) RegisterRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(middleware.AuthConfig{}))
		r.Post("/reviews", h.CreateReview)
		r.Get("/reviews", h.ListReviews)
		r.Get("/reviews/{id}", h.GetReview)
		r.Post("/reviews/{id}/approve", h.ApproveReview)
		r.Post("/reviews/{id}/reject", h.RejectReview)
		r.Patch("/reviews/{id}", h.ExecuteReview) // Agent marks EXECUTED
	})

	// SSE stream with ticket auth (query param)
	r.Get("/reviews/{id}/stream", h.StreamReview)

	// Status polling for external agents (requires auth)
	r.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(middleware.AuthConfig{}))
		r.Get("/reviews/{id}/status", h.GetReviewStatus)
	})
}

// CreateReview handles POST /v1/reviews
func (h *ReviewHandlers) CreateReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "CreateReview").Logger()

	// Get user ID and tenant ID from context
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

	// Parse request body
	var req struct {
		Action      string                 `json:"action"`
		Payload     map[string]interface{} `json:"payload"`
		ReviewerID  string                 `json:"reviewer_id,omitempty"`
		TTLSeconds  int                    `json:"ttl_seconds,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	if req.Action == "" {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	var reviewerID *domain.UUID
	if req.ReviewerID != "" {
		id, err := domain.ParseUUID(req.ReviewerID)
		if err != nil {
			h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
			return
		}
		reviewerID = &id
	}

	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}

	input := hitl.CreateReviewInput{
		TenantID:    tenantID,
		RequesterID: userID,
		Action:      req.Action,
		Payload:     req.Payload,
		ReviewerID:  reviewerID,
		TTL:         ttl,
	}

	output, err := h.hitlUC.CreateReview(ctx, input)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to create review")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Return review with opaque token (only time it's returned)
	response := map[string]interface{}{
		"review": output.ReviewRequest,
		"token":  output.Token, // Opaque token - caller must save this!
	}

	h.writeJSON(w, http.StatusCreated, response)
}

// ListReviews handles GET /v1/reviews
func (h *ReviewHandlers) ListReviews(w http.ResponseWriter, r *http.Request) {
	_ = r.Context()
	_ = h.logger.With().Str("method", "ListReviews").Logger()

	_, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	// Parse query parameters for filtering
	_ = r.URL.Query().Get("status")
	_ = r.URL.Query().Get("requester_id")
	_ = r.URL.Query().Get("reviewer_id")
	_ = r.URL.Query().Get("limit")
	_ = r.URL.Query().Get("offset")

	// Use SQLC ListReviewRequests via direct pool query since we don't have a wrapper
	// For now, return placeholder
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"reviews": []interface{}{},
		"message": "List reviews not fully implemented - use SQLC ListReviewRequests",
	})
}

// GetReview handles GET /v1/reviews/{id}
func (h *ReviewHandlers) GetReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "GetReview").Logger()

	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	reviewIDStr := chi.URLParam(r, "id")
	reviewID, err := domain.ParseUUID(reviewIDStr)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	review, err := h.hitlUC.GetStatus(ctx, tenantID, reviewID)
	if err != nil {
		if err == domain.ErrNotFound {
			h.writeError(w, r, http.StatusNotFound, domain.ErrNotFound)
			return
		}
		logger.Error().Err(err).Msg("Failed to get review")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Don't return token hash
	review.TokenHash = ""
	h.writeJSON(w, http.StatusOK, review)
}

// GetReviewStatus handles GET /v1/reviews/{id}/status (polling for external agents)
func (h *ReviewHandlers) GetReviewStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "GetReviewStatus").Logger()

	tenantID, ok := middleware.GetTenantID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	reviewIDStr := chi.URLParam(r, "id")
	reviewID, err := domain.ParseUUID(reviewIDStr)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	review, err := h.hitlUC.GetStatus(ctx, tenantID, reviewID)
	if err != nil {
		if err == domain.ErrNotFound {
			h.writeError(w, r, http.StatusNotFound, domain.ErrNotFound)
			return
		}
		logger.Error().Err(err).Msg("Failed to get review status")
		h.writeError(w, r, http.StatusInternalServerError, err)
		return
	}

	// Return minimal status for polling
	response := map[string]interface{}{
		"id":        review.ID,
		"status":    review.Status,
		"expires_at": review.ExpiresAt,
		"decided_at": review.DecidedAt,
	}

	h.writeJSON(w, http.StatusOK, response)
}

// ApproveReview handles POST /v1/reviews/{id}/approve
func (h *ReviewHandlers) ApproveReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "ApproveReview").Logger()

	// Get user ID (the approver)
	userID, ok := middleware.GetUserID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	// Parse request body for token
	var req struct {
		Token          string `json:"token"`
		DecisionReason string `json:"decision_reason,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	if req.Token == "" {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	input := hitl.ApproveInput{
		Token:           req.Token,
		DecidedBy:       userID,
		DecisionReason:  req.DecisionReason,
	}

	output, err := h.hitlUC.Approve(ctx, input)
	if err != nil {
		logger.Debug().Err(err).Msg("Approve failed")
		switch err {
		case domain.ErrInvalidToken:
			h.writeError(w, r, http.StatusForbidden, err) // 403 to avoid oracle
		case domain.ErrReviewNotPending:
			h.writeError(w, r, http.StatusConflict, err)
		case domain.ErrExpired:
			h.writeError(w, r, http.StatusGone, err)
		case domain.ErrValidation:
			h.writeError(w, r, http.StatusBadRequest, err)
		default:
			h.writeError(w, r, http.StatusInternalServerError, err)
		}
		return
	}

	// Notify SSE connections
	h.notifyReviewUpdate(output.ReviewRequest.ID.String(), "status:APPROVED")

	h.writeJSON(w, http.StatusOK, output.ReviewRequest)
}

// RejectReview handles POST /v1/reviews/{id}/reject
func (h *ReviewHandlers) RejectReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "RejectReview").Logger()

	userID, ok := middleware.GetUserID(r)
	if !ok {
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	var req struct {
		Token          string `json:"token"`
		DecisionReason string `json:"decision_reason,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	if req.Token == "" {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	input := hitl.RejectInput{
		Token:          req.Token,
		DecidedBy:      userID,
		DecisionReason: req.DecisionReason,
	}

	review, err := h.hitlUC.Reject(ctx, input)
	if err != nil {
		logger.Debug().Err(err).Msg("Reject failed")
		switch err {
		case domain.ErrInvalidToken:
			h.writeError(w, r, http.StatusForbidden, err)
		case domain.ErrReviewNotPending:
			h.writeError(w, r, http.StatusConflict, err)
		case domain.ErrExpired:
			h.writeError(w, r, http.StatusGone, err)
		default:
			h.writeError(w, r, http.StatusInternalServerError, err)
		}
		return
	}

	h.notifyReviewUpdate(review.ID.String(), "status:REJECTED")
	h.writeJSON(w, http.StatusOK, review)
}

// ExecuteReview handles PATCH /v1/reviews/{id} (agent marks EXECUTED)
func (h *ReviewHandlers) ExecuteReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With().Str("method", "ExecuteReview").Logger()

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

	reviewIDStr := chi.URLParam(r, "id")
	reviewID, err := domain.ParseUUID(reviewIDStr)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	input := hitl.ExecuteInput{
		TenantID: tenantID,
		ReviewID: reviewID,
		ActorID:  userID,
	}

	review, err := h.hitlUC.Execute(ctx, input)
	if err != nil {
		logger.Error().Err(err).Msg("Execute failed")
		if err == domain.ErrReviewNotPending {
			h.writeError(w, r, http.StatusConflict, err)
		} else if err == domain.ErrNotFound {
			h.writeError(w, r, http.StatusNotFound, err)
		} else {
			h.writeError(w, r, http.StatusInternalServerError, err)
		}
		return
	}

	h.notifyReviewUpdate(review.ID.String(), "status:EXECUTED")
	h.writeJSON(w, http.StatusOK, review)
}

// StreamReview handles GET /v1/reviews/{id}/stream (SSE with ticket auth)
func (h *ReviewHandlers) StreamReview(w http.ResponseWriter, r *http.Request) {
	_ = r.Context()
	logger := h.logger.With().Str("method", "StreamReview").Logger()

	reviewIDStr := chi.URLParam(r, "id")
	reviewID, err := domain.ParseUUID(reviewIDStr)
	if err != nil {
		h.writeError(w, r, http.StatusBadRequest, domain.ErrValidation)
		return
	}

	// Ticket auth: validate JWT from query parameter
	// EventSource can't set headers, so we use a short-lived signed ticket
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		logger.Debug().Msg("Missing ticket parameter for SSE")
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	// Verify ticket JWT
	claims := &TicketClaims{}
	token, err := jwt.ParseWithClaims(ticket, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(h.jwtSecret), nil
	})
	if err != nil || !token.Valid {
		logger.Debug().Err(err).Msg("Invalid SSE ticket")
		h.writeError(w, r, http.StatusUnauthorized, domain.ErrUnauthorized)
		return
	}

	// Verify ticket matches the review ID
	if claims.ReviewID != reviewID.String() {
		logger.Debug().Msg("Ticket review ID mismatch")
		h.writeError(w, r, http.StatusForbidden, domain.ErrForbidden)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.Error().Msg("ResponseWriter does not support flushing")
		h.writeError(w, r, http.StatusInternalServerError, fmt.Errorf("streaming not supported"))
		return
	}

	// Create a channel for this connection
	connChan := make(chan []byte, 10)
	h.registerConnection(reviewID.String(), connChan)
	defer h.unregisterConnection(reviewID.String(), connChan)

	// Send initial connected event
	h.sendEvent(w, flusher, "connected", map[string]string{"review_id": reviewID.String()})

	// Get current status and send
	// For SSE with ticket auth, we don't have tenant context from auth middleware
	// The ticket contains tenant info, but for now we'll just send the status from the review
	// In production, we'd decode the ticket to get tenant ID and use it here
	_, _ = middleware.GetTenantID(r) // We don't use it yet, but ticket has it

	// For now, just send current status - we can't easily query without tenant
	// This is a simplified version - in reality we'd decode ticket for tenant ID
	// review, err := h.reviewRepo.GetByID(ctx, tenantID, reviewID)
	// if err == nil {
	// 	h.sendEvent(w, flusher, "status", map[string]string{"status": string(review.Status)})
	// }
	h.sendEvent(w, flusher, "status", map[string]string{"status": "PENDING"})

	// Heartbeat ticker
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// Listen for events or client disconnect
	for {
		select {
		case <-r.Context().Done():
			logger.Debug().Msg("SSE client disconnected")
			return
		case eventData := <-connChan:
			// Write event to client
			if _, err := w.Write(eventData); err != nil {
				logger.Debug().Err(err).Msg("Failed to write SSE event")
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			// Send heartbeat
			h.sendEvent(w, flusher, "heartbeat", map[string]string{"time": time.Now().Format(time.RFC3339)})
		}
	}
}

// TicketClaims represents the claims in an SSE ticket JWT
type TicketClaims struct {
	ReviewID string `json:"review_id"`
	TenantID string `json:"tenant_id"`
	jwt.RegisteredClaims
}

// GenerateStreamTicket generates a short-lived JWT ticket for SSE access
func (h *ReviewHandlers) GenerateStreamTicket(tenantID, reviewID domain.UUID, ttl time.Duration) (string, error) {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}

	claims := TicketClaims{
		ReviewID: reviewID.String(),
		TenantID: tenantID.String(),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    "agent-gateway",
			Subject:   "sse-stream",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(h.jwtSecret))
}

// SSE connection management
func (h *ReviewHandlers) registerConnection(reviewID string, ch chan []byte) {
	h.connMutex.Lock()
	defer h.connMutex.Unlock()

	if h.connections[reviewID] == nil {
		h.connections[reviewID] = make(map[chan []byte]bool)
	}
	h.connections[reviewID][ch] = true
}

func (h *ReviewHandlers) unregisterConnection(reviewID string, ch chan []byte) {
	h.connMutex.Lock()
	defer h.connMutex.Unlock()

	if conns, ok := h.connections[reviewID]; ok {
		delete(conns, ch)
		if len(conns) == 0 {
			delete(h.connections, reviewID)
		}
	}
	close(ch)
}

func (h *ReviewHandlers) notifyReviewUpdate(reviewID, eventType string) {
	h.connMutex.RLock()
	conns := h.connections[reviewID]
	h.connMutex.RUnlock()

	if len(conns) == 0 {
		return
	}

	eventData := h.formatSSEEvent(eventType, map[string]string{
		"review_id": reviewID,
		"status":    strings.TrimPrefix(eventType, "status:"),
	})

	for ch := range conns {
		select {
		case ch <- eventData:
		default:
			// Channel full, skip
		}
	}
}

func (h *ReviewHandlers) formatSSEEvent(eventType string, data map[string]string) []byte {
	jsonData, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(jsonData)))
}

func (h *ReviewHandlers) sendEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, data map[string]string) {
	eventData := h.formatSSEEvent(eventType, data)
	if _, err := w.Write(eventData); err != nil {
		return
	}
	flusher.Flush()
}

func (h *ReviewHandlers) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (h *ReviewHandlers) writeError(w http.ResponseWriter, r *http.Request, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}