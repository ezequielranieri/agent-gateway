package postgres

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	postgressqlc "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres/sqlc"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// GuardrailViolationRepository handles persistence of guardrail violations
type GuardrailViolationRepository struct {
	queries *postgressqlc.Queries
	pool    *pgxpool.Pool
}

// NewGuardrailViolationRepository creates a new guardrail violation repository
func NewGuardrailViolationRepository(pool *pgxpool.Pool) *GuardrailViolationRepository {
	return &GuardrailViolationRepository{
		queries: postgressqlc.New(pool),
		pool:    pool,
	}
}

// Create stores a guardrail violation
// Runs inside WithTenantTx (tenant-bound transaction)
func (r *GuardrailViolationRepository) Create(ctx context.Context, violation *domain.GuardrailViolation) error {
	return WithTenantTx(ctx, r.pool, violation.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := r.queries.WithTx(tx)
		var requestID pgtype.UUID
		if violation.RequestID != nil {
			requestID = pgtype.UUID{Bytes: uuid.UUID(*violation.RequestID), Valid: true}
		}

		// Convert context to JSON
		metadata := []byte(violation.Context)
		if len(metadata) == 0 {
			metadata = []byte("{}")
		}

		params := postgressqlc.CreateGuardrailViolationParams{
			TenantID:       uuid.UUID(violation.TenantID),
			RequestID:      requestID,
			Direction:      string(violation.Phase),
			RuleID:         violation.Rule,
			Severity:       violation.Severity,
			PayloadExcerpt: violation.Message,
			Metadata:       metadata,
		}

		created, err := q.CreateGuardrailViolation(ctx, params)
		if err != nil {
			return err
		}

		violation.ID = domain.UUID(created.ID)
		violation.CreatedAt = created.CreatedAt
		return nil
	})
}

// GetByID retrieves a guardrail violation by ID
func (r *GuardrailViolationRepository) GetByID(ctx context.Context, tenantID domain.UUID, id domain.UUID) (*domain.GuardrailViolation, error) {
	var violation *domain.GuardrailViolation
	err := WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := r.queries.WithTx(tx)
		result, err := q.GetGuardrailViolationByID(ctx, postgressqlc.GetGuardrailViolationByIDParams{
			TenantID: uuid.UUID(tenantID),
			ID:       uuid.UUID(id),
		})
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.ErrNotFound
			}
			return err
		}

		violation = convertSQLCGuardrailViolation(result)
		return nil
	})
	return violation, err
}

// List retrieves guardrail violations with filters
func (r *GuardrailViolationRepository) List(ctx context.Context, filter GuardrailViolationFilter) ([]*domain.GuardrailViolation, error) {
	var violations []*domain.GuardrailViolation
	err := WithTenantTx(ctx, r.pool, filter.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := r.queries.WithTx(tx)
		params := postgressqlc.ListGuardrailViolationsParams{
			TenantID: uuid.UUID(filter.TenantID),
			Column2:  filter.Direction,
			Column3:  filter.RuleID,
			Column4:  filter.Severity,
			Column5:  uuid.Nil,
			Limit:    int32(filter.Limit),
			Offset:   int32(filter.Offset),
		}

		if filter.RequestID != nil {
			params.Column5 = uuid.UUID(*filter.RequestID)
		}

		rows, err := q.ListGuardrailViolations(ctx, params)
		if err != nil {
			return err
		}

		violations = make([]*domain.GuardrailViolation, len(rows))
		for i, row := range rows {
			violations[i] = convertSQLCGuardrailViolation(row)
		}
		return nil
	})
	return violations, err
}

// GuardrailViolationFilter represents filter options for querying violations
type GuardrailViolationFilter struct {
	TenantID  domain.UUID
	Direction string
	RuleID    string
	Severity  string
	RequestID *domain.UUID
	Limit     int
	Offset    int
}

// convertSQLCGuardrailViolation converts a SQLC GuardrailViolation to domain GuardrailViolation
func convertSQLCGuardrailViolation(v postgressqlc.GuardrailViolation) *domain.GuardrailViolation {
	var requestID *domain.UUID
	if v.RequestID.Valid {
		id := domain.UUID(v.RequestID.Bytes)
		requestID = &id
	}

	return &domain.GuardrailViolation{
		ID:          domain.UUID(v.ID),
		TenantID:    domain.UUID(v.TenantID),
		RequestID:   requestID,
		Phase:       domain.GuardrailPhase(v.Direction),
		Rule:        v.RuleID,
		Severity:    v.Severity,
		Message:     v.PayloadExcerpt,
		Context:     string(v.Metadata),
		CreatedAt:   v.CreatedAt,
	}
}