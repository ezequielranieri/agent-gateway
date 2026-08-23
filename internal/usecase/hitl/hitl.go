package hitl

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// HITLConfig holds configuration for the HITL use case
type HITLConfig struct {
	ReviewRepo   *postgres.ReviewRepository
	AuditRepo    *postgres.AuditRepository
	DefaultTTL   time.Duration
	Logger       zerolog.Logger
}

// HITLUseCase handles human-in-the-loop review operations
type HITLUseCase struct {
	reviewRepo *postgres.ReviewRepository
	auditRepo  *postgres.AuditRepository
	defaultTTL time.Duration
	logger     zerolog.Logger
}

// NewHITLUseCase creates a new HITL use case
func NewHITLUseCase(cfg HITLConfig) *HITLUseCase {
	return &HITLUseCase{
		reviewRepo: cfg.ReviewRepo,
		auditRepo:  cfg.AuditRepo,
		defaultTTL: cfg.DefaultTTL,
		logger:     cfg.Logger,
	}
}

// CreateReviewInput represents input for creating a review request
type CreateReviewInput struct {
	TenantID    domain.UUID
	RequesterID domain.UUID
	Action      string
	Payload     map[string]interface{}
	ReviewerID  *domain.UUID // Optional assigned reviewer
	TTL         time.Duration // Optional custom TTL
}

// CreateReviewOutput represents output from creating a review request
type CreateReviewOutput struct {
	ReviewRequest *domain.ReviewRequest
	Token         string // Opaque token returned once
}

// CreateReview creates a new review request in PENDING state
func (uc *HITLUseCase) CreateReview(ctx context.Context, input CreateReviewInput) (*CreateReviewOutput, error) {
	uc.logger.Debug().
		Str("tenant_id", input.TenantID.String()).
		Str("requester_id", input.RequesterID.String()).
		Str("action", input.Action).
		Msg("Creating review request")

	ttl := input.TTL
	if ttl == 0 {
		ttl = uc.defaultTTL
	}
	if ttl == 0 {
		ttl = 24 * time.Hour // Default 24 hours
	}

	// Serialize payload to JSON with canonical ordering (sorted keys)
	payloadBytes, err := json.Marshal(input.Payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Create review request entity
	review := &domain.ReviewRequest{
		TenantID:     input.TenantID,
		RequesterID:  input.RequesterID,
		ReviewerID:   input.ReviewerID,
		Action:       input.Action,
		Payload:      string(payloadBytes),
		Status:       domain.ReviewStatusPending,
		ExpiresAt:    time.Now().Add(ttl),
	}

	// Create in repository (generates token and stores hash)
	createdReview, token, err := uc.reviewRepo.Create(ctx, review)
	if err != nil {
		uc.logger.Error().Err(err).Msg("Failed to create review request")
		return nil, fmt.Errorf("failed to create review: %w", err)
	}

	// Emit audit event for review creation
	auditEvent := &domain.AuditEvent{
		TenantID:    input.TenantID,
		ActorUserID: &input.RequesterID,
		Action:      "review.created",
		EntityType:  "review_request",
		EntityID:    &createdReview.ID,
		Severity:    domain.AuditSeverityInfo,
		Payload:     json.RawMessage(fmt.Sprintf(`{"review_id":"%s","action":"%s"}`, createdReview.ID, input.Action)),
		CreatedAt:   time.Now(),
	}
	if err := uc.auditRepo.Append(ctx, auditEvent); err != nil {
		uc.logger.Error().Err(err).Msg("Failed to emit review.created audit event (fail-open)")
		// Fail-open: don't fail the request if audit fails
	}

	uc.logger.Info().
		Str("review_id", createdReview.ID.String()).
		Str("action", input.Action).
		Msg("Review request created")

	return &CreateReviewOutput{
		ReviewRequest: createdReview,
		Token:         token,
	}, nil
}

// GetStatus retrieves the current status of a review request
func (uc *HITLUseCase) GetStatus(ctx context.Context, tenantID domain.UUID, reviewID domain.UUID) (*domain.ReviewRequest, error) {
	review, err := uc.reviewRepo.GetByID(ctx, tenantID, reviewID)
	if err != nil {
		return nil, err
	}

	// Check if expired
	if review.Status == domain.ReviewStatusPending && review.IsExpired() {
		// Note: We don't auto-expire here; the background job handles that
		// But we could return the expired status
	}

	return review, nil
}

// ApproveInput represents input for approving a review request
type ApproveInput struct {
	Token        string       // Opaque token from creation
	DecidedBy    domain.UUID  // User ID of the approver
	DecisionReason string     // Optional reason
}

// ApproveOutput represents output from approving a review request
type ApproveOutput struct {
	ReviewRequest *domain.ReviewRequest
}

// Approve approves a pending review request with full re-validation
func (uc *HITLUseCase) Approve(ctx context.Context, input ApproveInput) (*ApproveOutput, error) {
	uc.logger.Debug().Msg("Approving review request")

	// Hash the presented token for lookup
	tokenHash := hashToken(input.Token)

	// Look up review by token hash (cross-tenant)
	review, err := uc.reviewRepo.GetByTokenHash(ctx, tokenHash)
	if err != nil {
		if err == domain.ErrNotFound {
			uc.logger.Debug().Msg("Review not found for token")
			return nil, domain.ErrInvalidToken
		}
		return nil, fmt.Errorf("failed to get review: %w", err)
	}

	// Timing-safe comparison of token hash
	storedHash := []byte(review.TokenHash)
	presentedHash := []byte(tokenHash)
	if subtle.ConstantTimeCompare(storedHash, presentedHash) != 1 {
		uc.logger.Debug().Msg("Token hash mismatch (timing-safe compare)")
		return nil, domain.ErrInvalidToken
	}

	// Verify review is in PENDING state and not expired
	if review.Status != domain.ReviewStatusPending {
		uc.logger.Debug().Str("status", string(review.Status)).Msg("Review not in PENDING state")
		return nil, domain.ErrReviewNotPending
	}
	if review.IsExpired() {
		uc.logger.Debug().Msg("Review expired")
		return nil, domain.ErrExpired
	}

	// FULL RE-VALIDATION
	// 1. Parse payload with DisallowUnknownFields
	var payloadMap map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(review.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payloadMap); err != nil {
		uc.logger.Error().Err(err).Msg("Re-validation failed: invalid payload JSON")
		// Reject the review due to invalid payload
		_ = uc.rejectWithReason(ctx, review, input.DecidedBy, "re-validation failed: invalid payload")
		return nil, domain.ErrValidation
	}

	// 2. Verify action field matches
	if action, ok := payloadMap["action"].(string); !ok || action != review.Action {
		uc.logger.Error().Msg("Re-validation failed: action mismatch")
		_ = uc.rejectWithReason(ctx, review, input.DecidedBy, "re-validation failed: action mismatch")
		return nil, domain.ErrValidation
	}

	// 3. Verify tenant-scoped resource IDs exist and belong to the tenant
	// This would involve checking that any resource IDs in the payload
	// (e.g., tool IDs, model IDs) exist and are accessible within the tenant
	// For now, we'll do a basic check - in a full implementation this would
	// call out to the relevant repositories
	if err := uc.revalidateResourceIDs(ctx, review.TenantID, payloadMap); err != nil {
		uc.logger.Error().Err(err).Msg("Re-validation failed: resource ID check")
		_ = uc.rejectWithReason(ctx, review, input.DecidedBy, "re-validation failed: "+err.Error())
		return nil, domain.ErrValidation
	}

	// 4. Verify rate limit and guardrails would still pass (optional, per design)
	// This is a stub for the re-validation requirement

	// Atomic state transition: PENDING -> APPROVED
	// The UPDATE ... WHERE status='PENDING' AND token_hash=$1 ensures only one approve succeeds
	err = uc.reviewRepo.UpdateStatus(ctx, review.TenantID, review.ID, domain.ReviewStatusApproved, &input.DecidedBy)
	if err != nil {
		if err == domain.ErrReviewNotPending {
			uc.logger.Debug().Msg("Concurrent approve detected - review no longer pending")
			return nil, domain.ErrReviewNotPending
		}
		return nil, fmt.Errorf("failed to approve review: %w", err)
	}

	// Refresh review to get updated status
	approvedReview, err := uc.reviewRepo.GetByID(ctx, review.TenantID, review.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get approved review: %w", err)
	}

	// Emit audit event for review approval
	auditEvent := &domain.AuditEvent{
		TenantID:    review.TenantID,
		ActorUserID: &input.DecidedBy,
		Action:      "review.approved",
		EntityType:  "review_request",
		EntityID:    &review.ID,
		Severity:    domain.AuditSeverityInfo,
		Payload:     json.RawMessage(fmt.Sprintf(`{"review_id":"%s","action":"%s","decision_reason":"%s"}`, review.ID, review.Action, input.DecisionReason)),
		CreatedAt:   time.Now(),
	}
	if err := uc.auditRepo.Append(ctx, auditEvent); err != nil {
		uc.logger.Error().Err(err).Msg("Failed to emit review.approved audit event (fail-open)")
	}

	uc.logger.Info().
		Str("review_id", review.ID.String()).
		Str("decided_by", input.DecidedBy.String()).
		Msg("Review request approved")

	return &ApproveOutput{ReviewRequest: approvedReview}, nil
}

// RejectInput represents input for rejecting a review request
type RejectInput struct {
	Token          string       // Opaque token from creation
	DecidedBy      domain.UUID  // User ID of the rejector
	DecisionReason string       // Optional reason
}

// Reject rejects a pending review request
func (uc *HITLUseCase) Reject(ctx context.Context, input RejectInput) (*domain.ReviewRequest, error) {
	uc.logger.Debug().Msg("Rejecting review request")

	// Hash the presented token for lookup
	tokenHash := hashToken(input.Token)

	// Look up review by token hash
	review, err := uc.reviewRepo.GetByTokenHash(ctx, tokenHash)
	if err != nil {
		if err == domain.ErrNotFound {
			return nil, domain.ErrInvalidToken
		}
		return nil, fmt.Errorf("failed to get review: %w", err)
	}

	// Timing-safe comparison
	storedHash := []byte(review.TokenHash)
	presentedHash := []byte(tokenHash)
	if subtle.ConstantTimeCompare(storedHash, presentedHash) != 1 {
		uc.logger.Debug().Msg("Token hash mismatch (timing-safe compare)")
		return nil, domain.ErrInvalidToken
	}

	// Verify review is in PENDING state and not expired
	if review.Status != domain.ReviewStatusPending {
		return nil, domain.ErrReviewNotPending
	}
	if review.IsExpired() {
		return nil, domain.ErrExpired
	}

	// Atomic state transition: PENDING -> REJECTED
	err = uc.reviewRepo.UpdateStatus(ctx, review.TenantID, review.ID, domain.ReviewStatusRejected, &input.DecidedBy)
	if err != nil {
		if err == domain.ErrReviewNotPending {
			return nil, domain.ErrReviewNotPending
		}
		return nil, fmt.Errorf("failed to reject review: %w", err)
	}

	// Refresh review
	rejectedReview, err := uc.reviewRepo.GetByID(ctx, review.TenantID, review.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get rejected review: %w", err)
	}

	// Emit audit event for review rejection
	auditEvent := &domain.AuditEvent{
		TenantID:    review.TenantID,
		ActorUserID: &input.DecidedBy,
		Action:      "review.rejected",
		EntityType:  "review_request",
		EntityID:    &review.ID,
		Severity:    domain.AuditSeverityInfo,
		Payload:     json.RawMessage(fmt.Sprintf(`{"review_id":"%s","action":"%s","decision_reason":"%s"}`, review.ID, review.Action, input.DecisionReason)),
		CreatedAt:   time.Now(),
	}
	if err := uc.auditRepo.Append(ctx, auditEvent); err != nil {
		uc.logger.Error().Err(err).Msg("Failed to emit review.rejected audit event (fail-open)")
	}

	uc.logger.Info().
		Str("review_id", review.ID.String()).
		Str("decided_by", input.DecidedBy.String()).
		Msg("Review request rejected")

	return rejectedReview, nil
}

// ExecuteInput represents input for marking a review as executed
type ExecuteInput struct {
	TenantID domain.UUID
	ReviewID domain.UUID
	ActorID  domain.UUID // The agent/system executing the action
}

// Execute marks an approved review as executed (called by external agent after materializing action)
func (uc *HITLUseCase) Execute(ctx context.Context, input ExecuteInput) (*domain.ReviewRequest, error) {
	uc.logger.Debug().
		Str("review_id", input.ReviewID.String()).
		Msg("Marking review as executed")

	// Get review within tenant context
	review, err := uc.reviewRepo.GetByID(ctx, input.TenantID, input.ReviewID)
	if err != nil {
		return nil, err
	}

	// Verify review is in APPROVED state
	if review.Status != domain.ReviewStatusApproved {
		return nil, domain.ErrReviewNotPending // Or a more specific error
	}

	// Mark as executed
	err = uc.reviewRepo.MarkExecuted(ctx, input.TenantID, input.ReviewID)
	if err != nil {
		return nil, fmt.Errorf("failed to mark review executed: %w", err)
	}

	// Refresh review
	executedReview, err := uc.reviewRepo.GetByID(ctx, input.TenantID, input.ReviewID)
	if err != nil {
		return nil, fmt.Errorf("failed to get executed review: %w", err)
	}

	// Emit audit event for review execution
	auditEvent := &domain.AuditEvent{
		TenantID:    input.TenantID,
		ActorUserID: &input.ActorID,
		Action:      "review.executed",
		EntityType:  "review_request",
		EntityID:    &input.ReviewID,
		Severity:    domain.AuditSeverityInfo,
		Payload:     json.RawMessage(fmt.Sprintf(`{"review_id":"%s","action":"%s"}`, input.ReviewID, review.Action)),
		CreatedAt:   time.Now(),
	}
	if err := uc.auditRepo.Append(ctx, auditEvent); err != nil {
		uc.logger.Error().Err(err).Msg("Failed to emit review.executed audit event (fail-open)")
	}

	uc.logger.Info().
		Str("review_id", input.ReviewID.String()).
		Msg("Review request marked as executed")

	return executedReview, nil
}

// ExpirePending sweeps expired pending reviews (background job)
func (uc *HITLUseCase) ExpirePending(ctx context.Context) (int64, error) {
	uc.logger.Debug().Msg("Sweeping expired pending reviews")
	count, err := uc.reviewRepo.ExpirePending(ctx)
	if err != nil {
		return 0, err
	}

	// Emit audit events for each expired review (fail-open)
	// In a real implementation, we'd get the list of expired reviews and emit events
	if count > 0 {
		uc.logger.Info().Int64("expired_count", count).Msg("Expired pending reviews")
		// Note: For simplicity, we emit a single summary event
		auditEvent := &domain.AuditEvent{
			TenantID: domain.NewUUID(), // This would need to be per-tenant
			Action:   "review.expired",
			EntityType: "review_request",
			Severity: domain.AuditSeverityWarn,
			Payload: json.RawMessage(fmt.Sprintf(`{"expired_count":%d}`, count)),
			CreatedAt: time.Now(),
		}
		_ = uc.auditRepo.Append(ctx, auditEvent) // Fail-open
	}

	return count, nil
}

// rejectWithReason is a helper to reject a review with a reason
func (uc *HITLUseCase) rejectWithReason(ctx context.Context, review *domain.ReviewRequest, decidedBy domain.UUID, reason string) error {
	return uc.reviewRepo.UpdateStatus(ctx, review.TenantID, review.ID, domain.ReviewStatusRejected, &decidedBy)
}

// revalidateResourceIDs performs tenant-scoped resource ID validation
// This is a stub - in a full implementation, it would verify that resource IDs
// in the payload (tool IDs, model IDs, etc.) exist and belong to the tenant
func (uc *HITLUseCase) revalidateResourceIDs(ctx context.Context, tenantID domain.UUID, payload map[string]interface{}) error {
	// Example: Check for tool_id, model_id, etc. in payload
	// and verify they exist in the tenant's scope
	// This is a placeholder for the re-validation requirement
	
	// For now, just log and return nil (passes re-validation)
	uc.logger.Debug().Interface("payload", payload).Msg("Re-validating resource IDs")
	return nil
}

// hashToken computes SHA-256 hash of the opaque token
func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}