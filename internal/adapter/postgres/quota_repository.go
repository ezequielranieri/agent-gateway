package postgres

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	postgressqlc "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres/sqlc"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// QuotaRepository implements the quota repository using SQLC
type QuotaRepository struct {
	queries *postgressqlc.Queries
	pool    *pgxpool.Pool
}

// NewQuotaRepository creates a new quota repository
func NewQuotaRepository(pool *pgxpool.Pool) *QuotaRepository {
	return &QuotaRepository{
		queries: postgressqlc.New(pool),
		pool:    pool,
	}
}

// Create creates a new quota
func (r *QuotaRepository) Create(ctx context.Context, tenantID domain.UUID, quota *domain.Quota) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.CreateQuotaParams{
			TenantID:         uuid.UUID(tenantID),
			Scope:            string(quota.Scope),
			ScopeID:          uuid.UUID(quota.ScopeID),
			RequestsPerMin:   int32(quota.RequestsPerMin),
			TokensPerMin:     int32(quota.TokensPerMin),
			ToolExecsPerMin:  int32(quota.ToolExecsPerMin),
		}
		result, err := r.queries.CreateQuota(ctx, params)
		if err != nil {
			return err
		}
		quota.ID = domain.UUID(result.ID)
		quota.TenantID = domain.UUID(result.TenantID)
		quota.CreatedAt = result.CreatedAt
		quota.UpdatedAt = result.UpdatedAt
		return nil
	})
}

// GetByScope retrieves a quota by scope
func (r *QuotaRepository) GetByScope(ctx context.Context, tenantID domain.UUID, scope domain.QuotaScope, scopeID domain.UUID) (*domain.Quota, error) {
	var quota *domain.Quota
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.GetQuotaByScopeParams{
			TenantID: uuid.UUID(tenantID),
			Scope:    string(scope),
			ScopeID:  uuid.UUID(scopeID),
		}
		result, err := r.queries.GetQuotaByScope(ctx, params)
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.ErrNotFound
			}
			return err
		}
		quota = &domain.Quota{
			ID:              domain.UUID(result.ID),
			TenantID:        domain.UUID(result.TenantID),
			Scope:           domain.QuotaScope(result.Scope),
			ScopeID:         domain.UUID(result.ScopeID),
			RequestsPerMin:  int(result.RequestsPerMin),
			TokensPerMin:    int(result.TokensPerMin),
			ToolExecsPerMin: int(result.ToolExecsPerMin),
			CreatedAt:       result.CreatedAt,
			UpdatedAt:       result.UpdatedAt,
		}
		return nil
	})
	return quota, err
}

// ListByTenant lists all quotas for a tenant
func (r *QuotaRepository) ListByTenant(ctx context.Context, tenantID domain.UUID) ([]domain.Quota, error) {
	var quotas []domain.Quota
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		results, err := r.queries.ListQuotasByTenant(ctx, uuid.UUID(tenantID))
		if err != nil {
			return err
		}
		quotas = make([]domain.Quota, len(results))
		for i, q := range results {
			quotas[i] = domain.Quota{
				ID:              domain.UUID(q.ID),
				TenantID:        domain.UUID(q.TenantID),
				Scope:           domain.QuotaScope(q.Scope),
				ScopeID:         domain.UUID(q.ScopeID),
				RequestsPerMin:  int(q.RequestsPerMin),
				TokensPerMin:    int(q.TokensPerMin),
				ToolExecsPerMin: int(q.ToolExecsPerMin),
				CreatedAt:       q.CreatedAt,
				UpdatedAt:       q.UpdatedAt,
			}
		}
		return nil
	})
	return quotas, err
}

// Update updates a quota
func (r *QuotaRepository) Update(ctx context.Context, tenantID domain.UUID, quota *domain.Quota) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.UpdateQuotaParams{
			TenantID:         uuid.UUID(tenantID),
			Scope:            string(quota.Scope),
			ScopeID:          uuid.UUID(quota.ScopeID),
			RequestsPerMin:   int32(quota.RequestsPerMin),
			TokensPerMin:     int32(quota.TokensPerMin),
			ToolExecsPerMin:  int32(quota.ToolExecsPerMin),
		}
		result, err := r.queries.UpdateQuota(ctx, params)
		if err != nil {
			return err
		}
		quota.UpdatedAt = result.UpdatedAt
		return nil
	})
}

// Delete deletes a quota
func (r *QuotaRepository) Delete(ctx context.Context, tenantID domain.UUID, scope domain.QuotaScope, scopeID domain.UUID) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.DeleteQuotaParams{
			TenantID: uuid.UUID(tenantID),
			Scope:    string(scope),
			ScopeID:  uuid.UUID(scopeID),
		}
		return r.queries.DeleteQuota(ctx, params)
	})
}