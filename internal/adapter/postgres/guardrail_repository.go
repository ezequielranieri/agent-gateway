package postgres

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
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
// Runs inside WithTenant (tenant-bound transaction)
func (r *GuardrailViolationRepository) Create(ctx context.Context, violation *domain.GuardrailViolation) error {
	return WithTenant(ctx, r.pool, violation.TenantID, func(ctx context.Context) error {
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

		created, err := r.queries.CreateGuardrailViolation(ctx, params)
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
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		row := r.pool.QueryRow(ctx, `
			SELECT id, tenant_id, request_id, direction, rule_id, severity, payload_excerpt, metadata, created_at
			FROM public.guardrail_violations
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, id)

		var i postgressqlc.GuardrailViolation
		err := row.Scan(
			&i.ID, &i.TenantID, &i.RequestID, &i.Direction,
			&i.RuleID, &i.Severity, &i.PayloadExcerpt, &i.Metadata, &i.CreatedAt,
		)
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.ErrNotFound
			}
			return err
		}

		violation = convertSQLCGuardrailViolation(i)
		return nil
	})
	return violation, err
}

// List retrieves guardrail violations with filters
func (r *GuardrailViolationRepository) List(ctx context.Context, filter GuardrailViolationFilter) ([]*domain.GuardrailViolation, error) {
	var violations []*domain.GuardrailViolation
	err := WithTenant(ctx, r.pool, filter.TenantID, func(ctx context.Context) error {
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

		rows, err := r.queries.ListGuardrailViolations(ctx, params)
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