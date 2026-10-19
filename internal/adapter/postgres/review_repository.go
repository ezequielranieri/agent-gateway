package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	err := WithTenantTx(ctx, r.pool, req.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := r.queries.WithTx(tx)
		params := postgressqlc.CreateReviewRequestParams{
			TenantID:    uuid.UUID(req.TenantID),
			RequesterID: uuid.UUID(req.RequesterID),
			ReviewerID:  pgtype.UUID{}, // Optional
			Action:      req.Action,
			Payload:     json.RawMessage(req.Payload),
			TokenHash:   tokenHash, // Store as bytea (raw 32 bytes)
			ExpiresAt:   req.ExpiresAt,
		}

		sqlcReview, err := q.CreateReviewRequest(ctx, params)
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
	err := WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := r.queries.WithTx(tx)
		sqlcReview, err := q.GetReviewRequestByID(ctx, postgressqlc.GetReviewRequestByIDParams{
			TenantID: uuid.UUID(tenantID),
			ID:       uuid.UUID(id),
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.ErrNotFound
			}
			return fmt.Errorf("failed to get review request: %w", err)
		}
		review = convertSQLCReviewRequest(sqlcReview)
		return nil
	})
	return review, err
}

// GetByTokenHashWithTenant retrieves a review request by token hash within a specific tenant.
// Runs inside WithTenantTx so RLS is enforced. Returns ErrNotFound if not found in that tenant.
func (r *ReviewRepository) GetByTokenHashWithTenant(ctx context.Context, tenantID domain.UUID, tokenHash []byte) (*domain.ReviewRequest, error) {
	var review *domain.ReviewRequest
	err := WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := r.queries.WithTx(tx)
		sqlcReview, err := q.GetReviewRequestByTokenHashWithTenant(ctx, postgressqlc.GetReviewRequestByTokenHashWithTenantParams{
			TenantID:  uuid.UUID(tenantID),
			TokenHash: tokenHash,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.ErrNotFound
			}
			return fmt.Errorf("failed to get review request by token hash: %w", err)
		}
		review = convertSQLCReviewRequest(sqlcReview)
		return nil
	})
	return review, err
}

// UpdateStatus atomically updates the review status with row lock
func (r *ReviewRepository) UpdateStatus(ctx context.Context, tenantID domain.UUID, id domain.UUID, status domain.ReviewStatus, decidedBy *domain.UUID) error {
	return WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := r.queries.WithTx(tx)
		var err error
		switch status {
		case domain.ReviewStatusApproved:
			params := postgressqlc.ApproveReviewRequestParams{
				TenantID:       uuid.UUID(tenantID),
				ID:             uuid.UUID(id),
				DecidedBy:      pgtype.UUID{Bytes: uuid.UUID(*decidedBy), Valid: decidedBy != nil},
				DecisionReason: pgtype.Text{}, // Could add reason if needed
			}
			_, err = q.ApproveReviewRequest(ctx, params)
		case domain.ReviewStatusRejected:
			params := postgressqlc.RejectReviewRequestParams{
				TenantID:       uuid.UUID(tenantID),
				ID:             uuid.UUID(id),
				DecidedBy:      pgtype.UUID{Bytes: uuid.UUID(*decidedBy), Valid: decidedBy != nil},
				DecisionReason: pgtype.Text{},
			}
			_, err = q.RejectReviewRequest(ctx, params)
		default:
			// For EXPIRED, we use the sweep function or direct update
			_, err = tx.Exec(ctx, `
				UPDATE public.review_requests
				SET status = $3, updated_at = now()
				WHERE tenant_id = $1 AND id = $2 AND status = 'PENDING'
			`, uuid.UUID(tenantID), uuid.UUID(id), string(status))
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return domain.ErrReviewNotPending
			}
			return fmt.Errorf("failed to update review status: %w", err)
		}
		return nil
	})
}

// MarkExecuted marks a review request as executed (only from APPROVED state)
func (r *ReviewRepository) MarkExecuted(ctx context.Context, tenantID domain.UUID, id domain.UUID) error {
	return WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `
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

// ExpirePending sweeps expired pending reviews to EXPIRED status across all tenants.
// Reads tenant IDs from the global tenants table and sweeps each tenant within its own transaction.
// If a tenant fails, the error is recorded and sweeping continues for remaining tenants.
// Returns the total count of expired reviews and an aggregated error if any tenant failed.
func (r *ReviewRepository) ExpirePending(ctx context.Context) (int64, error) {
	var totalExpired int64
	var errs []error

	// Read all active tenant IDs from the global tenants table (no RLS)
	rows, err := r.pool.Query(ctx, `SELECT id FROM public.tenants WHERE status = 'active'`)
	if err != nil {
		return 0, fmt.Errorf("failed to get active tenants: %w", err)
	}
	defer rows.Close()

	var tenantIDs []uuid.UUID
	for rows.Next() {
		var tenantID uuid.UUID
		if err := rows.Scan(&tenantID); err != nil {
			return totalExpired, err
		}
		tenantIDs = append(tenantIDs, tenantID)
	}
	if err := rows.Err(); err != nil {
		return totalExpired, err
	}

	// Sweep each tenant within its own WithTenantTx transaction
	for _, tenantID := range tenantIDs {
		var rowsAffected int64
		err := WithTenantTx(ctx, r.pool, domain.UUID(tenantID), func(ctx context.Context, tx pgx.Tx) error {
			q := r.queries.WithTx(tx)
			// Use SweepExpiredReviewsCount which returns the number of affected rows
			count, err := q.SweepExpiredReviewsCount(ctx, tenantID)
			if err != nil {
				return fmt.Errorf("failed to sweep expired reviews for tenant %s: %w", tenantID, err)
			}
			rowsAffected = count
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("tenant %s: %w", tenantID, err))
			continue
		}
		totalExpired += rowsAffected
	}

	if len(errs) > 0 {
		// Aggregate errors
		errMsgs := make([]string, len(errs))
		for i, e := range errs {
			errMsgs[i] = e.Error()
		}
		return totalExpired, fmt.Errorf("errors sweeping expired reviews: %v", errMsgs)
	}

	return totalExpired, nil
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