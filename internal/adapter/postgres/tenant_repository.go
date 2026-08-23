package postgres

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	postgressqlc "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres/sqlc"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// TenantRepository implements the tenant repository using SQLC
type TenantRepository struct {
	queries *postgressqlc.Queries
	pool    *pgxpool.Pool
}

// NewTenantRepository creates a new tenant repository
func NewTenantRepository(pool *pgxpool.Pool) *TenantRepository {
	return &TenantRepository{
		queries: postgressqlc.New(pool),
		pool:    pool,
	}
}

// Create creates a new tenant
func (r *TenantRepository) Create(ctx context.Context, tenant *domain.Tenant) error {
	result, err := r.queries.CreateTenant(ctx, tenant.Name)
	if err != nil {
		return err
	}
	tenant.ID = domain.UUID(result.ID)
	tenant.CreatedAt = result.CreatedAt
	return nil
}

// GetByID retrieves a tenant by ID
func (r *TenantRepository) GetByID(ctx context.Context, id domain.UUID) (*domain.Tenant, error) {
	result, err := r.queries.GetTenantByID(ctx, uuid.UUID(id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &domain.Tenant{
		ID:        domain.UUID(result.ID),
		Name:      result.Name,
		Status:    domain.TenantStatus(result.Status),
		CreatedAt: result.CreatedAt,
	}, nil
}

// GetByName retrieves a tenant by name
func (r *TenantRepository) GetByName(ctx context.Context, name string) (*domain.Tenant, error) {
	result, err := r.queries.GetTenantByName(ctx, name)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &domain.Tenant{
		ID:        domain.UUID(result.ID),
		Name:      result.Name,
		Status:    domain.TenantStatus(result.Status),
		CreatedAt: result.CreatedAt,
	}, nil
}

// UpdateStatus updates a tenant's status
func (r *TenantRepository) UpdateStatus(ctx context.Context, id domain.UUID, status domain.TenantStatus) error {
	params := postgressqlc.UpdateTenantStatusParams{
		ID:     uuid.UUID(id),
		Status: string(status),
	}
	_, err := r.queries.UpdateTenantStatus(ctx, params)
	return err
}

// ListTenants lists tenants with pagination
func (r *TenantRepository) ListTenants(ctx context.Context, limit, offset int) ([]domain.Tenant, error) {
	params := postgressqlc.ListTenantsParams{
		Limit:  int32(limit),
		Offset: int32(offset),
	}
	results, err := r.queries.ListTenants(ctx, params)
	if err != nil {
		return nil, err
	}
	tenants := make([]domain.Tenant, len(results))
	for i, t := range results {
		tenants[i] = domain.Tenant{
			ID:        domain.UUID(t.ID),
			Name:      t.Name,
			Status:    domain.TenantStatus(t.Status),
			CreatedAt: t.CreatedAt,
		}
	}
	return tenants, nil
}