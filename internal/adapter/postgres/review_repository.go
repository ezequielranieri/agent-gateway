package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	postgressqlc "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres/sqlc"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// ReviewRepository implements the review request repository using SQLC
type ReviewRepository struct {
	queries *postgressqlc.Queries
	pool    *pgxpool.Pool
}

// NewReviewRepository creates a new review repository
func NewReviewRepository(pool *pgxpool.Pool) *ReviewRepository {
	return &ReviewRepository{
		queries: postgressqlc.New(pool),
		pool:    pool,
	}
}

// hashToken computes SHA-256 hash of the opaque token as raw bytes
func hashToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

// Create creates a new review request and returns the opaque token
// The token is generated here and returned once to the caller
func (r *ReviewRepository) Create(ctx context.Context, req *domain.ReviewRequest) (*domain.ReviewRequest, string, error) {
	// Generate opaque token (32 random bytes = 64 hex chars)
	token := generateOpaqueToken()
	tokenHash := hashToken(token) // []byte (32 bytes)

	var created *domain.ReviewRequest
	err := WithTenant(ctx, r.pool, req.TenantID, func(ctx context.Context) error {
		params := postgressqlc.CreateReviewRequestParams{
			TenantID:    uuid.UUID(req.TenantID),
			RequesterID: uuid.UUID(req.RequesterID),
			ReviewerID:  pgtype.UUID{}, // Optional
			Action:      req.Action,
			Payload:     json.RawMessage(req.Payload),
			TokenHash:   tokenHash, // Store as bytea (raw 32 bytes)
			ExpiresAt:   req.ExpiresAt,
		}

		sqlcReview, err := r.queries.CreateReviewRequest(ctx, params)
		if err != nil {
			return fmt.Errorf("failed to create review request: %w", err)
		}

		created = convertSQLCReviewRequest(sqlcReview)
		return nil
	})

	if err != nil {
		return nil, "", err
	}

	return created, token, nil
}

// generateOpaqueToken generates a cryptographically secure random token (32 bytes = 64 hex chars)
func generateOpaqueToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// This should never happen in practice
		panic(fmt.Sprintf("failed to generate random token: %v", err))
	}
	return hex.EncodeToString(b)
}

// GetByID retrieves a review request by ID within the tenant context
func (r *ReviewRepository) GetByID(ctx context.Context, tenantID domain.UUID, id domain.UUID) (*domain.ReviewRequest, error) {
	var review *domain.ReviewRequest
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		sqlcReview, err := r.queries.GetReviewRequestByID(ctx, postgressqlc.GetReviewRequestByIDParams{
			TenantID: uuid.UUID(tenantID),
			ID:       uuid.UUID(id),
		})
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.ErrNotFound
			}
			return fmt.Errorf("failed to get review request: %w", err)
		}
		review = convertSQLCReviewRequest(sqlcReview)
		return nil
	})
	return review, err
}

// GetByTokenHash retrieves a review request by token hash (cross-tenant lookup for approve/reject)
func (r *ReviewRepository) GetByTokenHash(ctx context.Context, tokenHash []byte) (*domain.ReviewRequest, error) {
	// We need to search across all tenants for the token hash
	// This is used for approve/reject where the token is presented without tenant context
	var review domain.ReviewRequest
	var tokenHashBytes []byte
	var decidedAt sql.NullTime
	var decidedBy sql.NullString
	var decisionReason sql.NullString
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, requester_id, reviewer_id, action, payload, status, token_hash, expires_at, decided_at, decided_by, decision_reason, created_at, updated_at
		FROM public.review_requests
		WHERE token_hash = $1
		LIMIT 1
	`, tokenHash).Scan(
		&review.ID,
		&review.TenantID,
		&review.RequesterID,
		&review.ReviewerID,
		&review.Action,
		&review.Payload,
		&review.Status,
		&tokenHashBytes,
		&review.ExpiresAt,
		&decidedAt,
		&decidedBy,
		&decisionReason,
		&review.CreatedAt,
		&review.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get review request by token hash: %w", err)
	}
	// token_hash is already stored as raw bytes, convert to hex for domain model
	review.TokenHash = hex.EncodeToString(tokenHashBytes)
	if review.DecidedAt != nil {
		// DecidedAt is already set by the scan
	}
	if review.DecidedBy != nil {
		// DecidedBy is already set by the scan
	}
	if review.DecisionReason != "" {
		// DecisionReason is already set by the scan
	}
	return &review, nil
}

// UpdateStatus atomically updates the review status with row lock
func (r *ReviewRepository) UpdateStatus(ctx context.Context, tenantID domain.UUID, id domain.UUID, status domain.ReviewStatus, decidedBy *domain.UUID) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		var err error
		switch status {
		case domain.ReviewStatusApproved:
			params := postgressqlc.ApproveReviewRequestParams{
				TenantID:       uuid.UUID(tenantID),
				ID:             uuid.UUID(id),
				DecidedBy:      pgtype.UUID{Bytes: uuid.UUID(*decidedBy), Valid: decidedBy != nil},
				DecisionReason: pgtype.Text{}, // Could add reason if needed
			}
			_, err = r.queries.ApproveReviewRequest(ctx, params)
		case domain.ReviewStatusRejected:
			params := postgressqlc.RejectReviewRequestParams{
				TenantID:       uuid.UUID(tenantID),
				ID:             uuid.UUID(id),
				DecidedBy:      pgtype.UUID{Bytes: uuid.UUID(*decidedBy), Valid: decidedBy != nil},
				DecisionReason: pgtype.Text{},
			}
			_, err = r.queries.RejectReviewRequest(ctx, params)
		default:
			// For EXPIRED, we use the sweep function or direct update
			_, err = r.pool.Exec(ctx, `
				UPDATE public.review_requests
				SET status = $3, updated_at = now()
				WHERE tenant_id = $1 AND id = $2 AND status = 'PENDING'
			`, uuid.UUID(tenantID), uuid.UUID(id), string(status))
		}
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.ErrReviewNotPending
			}
			return fmt.Errorf("failed to update review status: %w", err)
		}
		return nil
	})
}

// MarkExecuted marks a review request as executed (only from APPROVED state)
func (r *ReviewRepository) MarkExecuted(ctx context.Context, tenantID domain.UUID, id domain.UUID) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		result, err := r.pool.Exec(ctx, `
			UPDATE public.review_requests
			SET status = 'EXECUTED', updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND status = 'APPROVED'
		`, uuid.UUID(tenantID), uuid.UUID(id))
		if err != nil {
			return fmt.Errorf("failed to mark review executed: %w", err)
		}
		if result.RowsAffected() == 0 {
			return domain.ErrReviewNotPending // Or a more specific error
		}
		return nil
	})
}

// ExpirePending sweeps expired pending reviews to EXPIRED status
// Returns the number of reviews expired
func (r *ReviewRepository) ExpirePending(ctx context.Context) (int64, error) {
	var totalExpired int64
	
	// Get all tenant IDs that have pending reviews
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT tenant_id FROM public.review_requests WHERE status = 'PENDING'
	`)
	if err != nil {
		return 0, fmt.Errorf("failed to get tenants with pending reviews: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tenantID uuid.UUID
		if err := rows.Scan(&tenantID); err != nil {
			return totalExpired, err
		}

		err := WithTenant(ctx, r.pool, domain.UUID(tenantID), func(ctx context.Context) error {
			err := r.queries.SweepExpiredReviews(ctx, tenantID)
			if err != nil {
				return fmt.Errorf("failed to sweep expired reviews for tenant %s: %w", tenantID, err)
			}
			// Note: We can't easily get the count from the SQLC function, 
			// so we'd need to run a separate query or modify the SQL
			return nil
		})
		if err != nil {
			return totalExpired, err
		}
		
		// Count expired for this tenant
		var count int64
		err = r.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM public.review_requests 
			WHERE tenant_id = $1 AND status = 'EXPIRED' AND updated_at > now() - interval '1 minute'
		`, tenantID).Scan(&count)
		if err != nil {
			return totalExpired, err
		}
		totalExpired += count
	}

	return totalExpired, rows.Err()
}

// convertSQLCReviewRequest converts a SQLC ReviewRequest to domain ReviewRequest
func convertSQLCReviewRequest(e postgressqlc.ReviewRequest) *domain.ReviewRequest {
	var reviewerID *domain.UUID
	if e.ReviewerID.Valid {
		id := domain.UUID(e.ReviewerID.Bytes)
		reviewerID = &id
	}

	var decidedBy *domain.UUID
	if e.DecidedBy.Valid {
		id := domain.UUID(e.DecidedBy.Bytes)
		decidedBy = &id
	}

	var decisionReason string
	if e.DecisionReason.Valid {
		decisionReason = e.DecisionReason.String
	}

	var decidedAt *time.Time
	if e.DecidedAt.Valid {
		decidedAt = &e.DecidedAt.Time
	}

	return &domain.ReviewRequest{
		ID:             domain.UUID(e.ID),
		TenantID:       domain.UUID(e.TenantID),
		RequesterID:    domain.UUID(e.RequesterID),
		ReviewerID:     reviewerID,
		TokenHash:      string(e.TokenHash),
		Action:         extractActionFromPayload(e.Payload), // Extract action from payload JSON
		Payload:        string(e.Payload),
		Status:         domain.ReviewStatus(e.Status),
		ExpiresAt:      e.ExpiresAt,
		DecidedAt:      decidedAt,
		DecidedBy:      decidedBy,
		DecisionReason: decisionReason,
		CreatedAt:      e.CreatedAt,
	}
}

// extractActionFromPayload extracts the action field from the payload JSON
func extractActionFromPayload(payload []byte) string {
	// Simple extraction - in practice, we'd parse the JSON
	// For now, return a default or parse properly
	var m map[string]interface{}
	if err := json.Unmarshal(payload, &m); err != nil {
		return "unknown"
	}
	if action, ok := m["action"].(string); ok {
		return action
	}
	return "unknown"
}

// Need to add json import
// This will be fixed when we add the import above