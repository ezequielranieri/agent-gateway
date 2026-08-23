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

// RoleRepository implements the role repository using SQLC
type RoleRepository struct {
	queries *postgressqlc.Queries
	pool    *pgxpool.Pool
}

// NewRoleRepository creates a new role repository
func NewRoleRepository(pool *pgxpool.Pool) *RoleRepository {
	return &RoleRepository{
		queries: postgressqlc.New(pool),
		pool:    pool,
	}
}

// Create creates a new role (global catalog)
func (r *RoleRepository) Create(ctx context.Context, role *domain.Role) error {
	params := postgressqlc.CreateRoleParams{
		Name:        role.Name,
		Description: pgtype.Text{String: role.Description, Valid: role.Description != ""},
	}
	result, err := r.queries.CreateRole(ctx, params)
	if err != nil {
		return err
	}
	role.ID = domain.UUID(result.ID)
	role.CreatedAt = result.CreatedAt
	return nil
}

// GetByID retrieves a role by ID
func (r *RoleRepository) GetByID(ctx context.Context, id domain.UUID) (*domain.Role, error) {
	result, err := r.queries.GetRoleByID(ctx, uuid.UUID(id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &domain.Role{
		ID:          domain.UUID(result.ID),
		Name:        result.Name,
		Description: result.Description.String,
		CreatedAt:   result.CreatedAt,
	}, nil
}

// ListByTenant lists roles that have permissions assigned in a tenant
func (r *RoleRepository) ListByTenant(ctx context.Context, tenantID domain.UUID) ([]domain.Role, error) {
	var roles []domain.Role
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		results, err := r.queries.ListRolesByTenant(ctx, uuid.UUID(tenantID))
		if err != nil {
			return err
		}
		roles = make([]domain.Role, len(results))
		for i, rr := range results {
			roles[i] = domain.Role{
				ID:          domain.UUID(rr.ID),
				Name:        rr.Name,
				Description: rr.Description.String,
				CreatedAt:   rr.CreatedAt,
			}
		}
		return nil
	})
	return roles, err
}

// AssignPermissions assigns permissions to a role within a tenant
func (r *RoleRepository) AssignPermissions(ctx context.Context, tenantID domain.UUID, roleID domain.UUID, permissions []string) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.AssignRolePermissionsParams{
			RoleID:   uuid.UUID(roleID),
			TenantID: uuid.UUID(tenantID),
			Column3:  permissions,
		}
		return r.queries.AssignRolePermissions(ctx, params)
	})
}

// GetPermissions retrieves permissions for a role within a tenant
func (r *RoleRepository) GetPermissions(ctx context.Context, tenantID domain.UUID, roleID domain.UUID) ([]string, error) {
	var perms []string
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.GetRolePermissionsParams{
			RoleID:   uuid.UUID(roleID),
			TenantID: uuid.UUID(tenantID),
		}
		results, err := r.queries.GetRolePermissions(ctx, params)
		if err != nil {
			return err
		}
		perms = make([]string, len(results))
		for i, p := range results {
			perms[i] = p
		}
		return nil
	})
	return perms, err
}

// Delete deletes a role (global catalog)
func (r *RoleRepository) Delete(ctx context.Context, id domain.UUID) error {
	return r.queries.DeleteRole(ctx, uuid.UUID(id))
}

// RevokePermissions revokes all permissions for a role within a tenant
func (r *RoleRepository) RevokePermissions(ctx context.Context, tenantID domain.UUID, roleID domain.UUID) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.RevokeRolePermissionsParams{
			RoleID:   uuid.UUID(roleID),
			TenantID: uuid.UUID(tenantID),
		}
		return r.queries.RevokeRolePermissions(ctx, params)
	})
}