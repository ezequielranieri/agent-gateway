package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/delegation"
)

// DelegationRepository handles persistence of delegation grants.
// All operations run inside WithTenant for RLS tenant binding.
type DelegationRepository struct {
	pool *pgxpool.Pool
}

// NewDelegationRepository creates a new delegation repository.
func NewDelegationRepository(pool *pgxpool.Pool) *DelegationRepository {
	return &DelegationRepository{pool: pool}
}

// delegationGrantRow represents a row from the delegation_grants table.
type delegationGrantRow struct {
	ID                uuid.UUID
	ParentGrantID     pgtype.UUID
	ChainID           uuid.UUID
	TenantID          uuid.UUID
	DelegateIdentity  string
	GrantedScope      []byte // jsonb
	RootIntent        string
	HITLClassification string
	Depth             int32
	Generation        int32
	ExpiresAt         time.Time
	BudgetRemaining   int32
	Status            string
	CreatedAt         time.Time
}

// grantToInsertParams converts a domain Grant to insert parameters.
func grantToInsertParams(g *delegation.Grant) delegationGrantRow {
	var parentID pgtype.UUID
	if g.ParentGrantID != nil {
		parentID = pgtype.UUID{Bytes: uuid.UUID(*g.ParentGrantID), Valid: true}
	}

	var scopeJSON []byte
	if g.GrantedScope != nil {
		scopeJSON, _ = json.Marshal(g.GrantedScope)
	}

	return delegationGrantRow{
		ID:                uuid.UUID(g.GrantID),
		ParentGrantID:     parentID,
		ChainID:           uuid.UUID(g.ChainID),
		TenantID:          uuid.UUID(g.TenantID),
		DelegateIdentity:  g.DelegateIdentity,
		GrantedScope:      scopeJSON,
		RootIntent:        g.RootIntent,
		HITLClassification: g.HITLClassification,
		Depth:             int32(g.Depth),
		Generation:        int32(g.Generation),
		ExpiresAt:         g.ExpiresAt,
		BudgetRemaining:   int32(g.BudgetRemaining),
		Status:            string(g.Status),
	}
}

// rowToGrant converts a database row to a domain Grant.
func rowToGrant(row delegationGrantRow) *delegation.Grant {
	var parentID *domain.UUID
	if row.ParentGrantID.Valid {
		id := domain.UUID(row.ParentGrantID.Bytes)
		parentID = &id
	}

	var scope delegation.ScopeSet
	if row.GrantedScope != nil {
		_ = json.Unmarshal(row.GrantedScope, &scope)
	}

	return &delegation.Grant{
		GrantID:            domain.UUID(row.ID),
		ParentGrantID:      parentID,
		ChainID:            domain.UUID(row.ChainID),
		TenantID:           domain.UUID(row.TenantID),
		DelegateIdentity:   row.DelegateIdentity,
		GrantedScope:       scope,
		RootIntent:         row.RootIntent,
		HITLClassification: row.HITLClassification,
		Depth:              int(row.Depth),
		Generation:         int(row.Generation),
		ExpiresAt:          row.ExpiresAt,
		BudgetRemaining:    int(row.BudgetRemaining),
		Status:             delegation.GrantStatus(row.Status),
	}
}

// Create inserts a new delegation grant.
// Runs inside WithTenant (tenant-bound transaction).
func (r *DelegationRepository) Create(ctx context.Context, grant *delegation.Grant) error {
	return WithTenant(ctx, r.pool, grant.TenantID, func(ctx context.Context) error {
		params := grantToInsertParams(grant)

		_, err := r.pool.Exec(ctx, `
			INSERT INTO public.delegation_grants
				(id, parent_grant_id, chain_id, tenant_id, delegate_identity,
				 granted_scope, root_intent, hitl_classification,
				 depth, generation, expires_at, budget_remaining, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		`,
			params.ID, params.ParentGrantID, params.ChainID, params.TenantID,
			params.DelegateIdentity, params.GrantedScope, params.RootIntent,
			params.HITLClassification, params.Depth, params.Generation,
			params.ExpiresAt, params.BudgetRemaining, params.Status,
		)
		if err != nil {
			return mapDelegationError(err)
		}
		return nil
	})
}

// GetByID retrieves a delegation grant by ID within a tenant.
func (r *DelegationRepository) GetByID(ctx context.Context, tenantID domain.UUID, grantID domain.UUID) (*delegation.Grant, error) {
	var result *delegation.Grant
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		row := r.pool.QueryRow(ctx, `
			SELECT id, parent_grant_id, chain_id, tenant_id, delegate_identity,
				   granted_scope, root_intent, hitl_classification,
				   depth, generation, expires_at, budget_remaining, status, created_at
			FROM public.delegation_grants
			WHERE tenant_id = $1 AND id = $2
		`, uuid.UUID(tenantID), uuid.UUID(grantID))

		var r delegationGrantRow
		err := row.Scan(
			&r.ID, &r.ParentGrantID, &r.ChainID, &r.TenantID,
			&r.DelegateIdentity, &r.GrantedScope, &r.RootIntent,
			&r.HITLClassification, &r.Depth, &r.Generation, &r.ExpiresAt,
			&r.BudgetRemaining, &r.Status, &r.CreatedAt,
		)
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.ErrNotFound
			}
			return mapDelegationError(err)
		}

		result = rowToGrant(r)
		return nil
	})
	return result, err
}

// UpdateStatus updates a delegation grant's status.
func (r *DelegationRepository) UpdateStatus(ctx context.Context, tenantID domain.UUID, grantID domain.UUID, status delegation.GrantStatus) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		tag, err := r.pool.Exec(ctx, `
			UPDATE public.delegation_grants
			SET status = $1
			WHERE tenant_id = $2 AND id = $3
		`, string(status), uuid.UUID(tenantID), uuid.UUID(grantID))
		if err != nil {
			return mapDelegationError(err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}

// CountActiveChildren counts the number of active children for a parent grant.
func (r *DelegationRepository) CountActiveChildren(ctx context.Context, tenantID domain.UUID, parentGrantID domain.UUID) (int, error) {
	var count int
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		row := r.pool.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM public.delegation_grants
			WHERE tenant_id = $1 AND parent_grant_id = $2 AND status = 'active'
		`, uuid.UUID(tenantID), uuid.UUID(parentGrantID))
		return row.Scan(&count)
	})
	return count, err
}

// GetChainGeneration returns the current generation counter for a chain.
// The generation is the count of revoked grants in this chain. When a chain
// is revoked, the count increases, invalidating all existing grants issued
// at a lower generation at the next authorization boundary.
func (r *DelegationRepository) GetChainGeneration(ctx context.Context, tenantID domain.UUID, chainID domain.UUID) (int, error) {
	var generation int
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		row := r.pool.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM public.delegation_grants
			WHERE tenant_id = $1 AND chain_id = $2 AND status = 'revoked'
		`, uuid.UUID(tenantID), uuid.UUID(chainID))
		return row.Scan(&generation)
	})
	return generation, err
}

// ListByChain retrieves all grants in a delegation chain.
func (r *DelegationRepository) ListByChain(ctx context.Context, tenantID domain.UUID, chainID domain.UUID) ([]*delegation.Grant, error) {
	var grants []*delegation.Grant
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		rows, err := r.pool.Query(ctx, `
			SELECT id, parent_grant_id, chain_id, tenant_id, delegate_identity,
				   granted_scope, root_intent, hitl_classification,
				   depth, generation, expires_at, budget_remaining, status, created_at
			FROM public.delegation_grants
			WHERE tenant_id = $1 AND chain_id = $2
			ORDER BY depth ASC
		`, uuid.UUID(tenantID), uuid.UUID(chainID))
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var r delegationGrantRow
			if err := rows.Scan(
				&r.ID, &r.ParentGrantID, &r.ChainID, &r.TenantID,
				&r.DelegateIdentity, &r.GrantedScope, &r.RootIntent,
				&r.HITLClassification, &r.Depth, &r.Generation, &r.ExpiresAt,
				&r.BudgetRemaining, &r.Status, &r.CreatedAt,
			); err != nil {
				return err
			}
			grants = append(grants, rowToGrant(r))
		}
		return rows.Err()
	})
	return grants, err
}

// RevokeByChain marks all active grants in a chain as revoked.
// This increments the chain generation, invalidating all existing grants
// at the next authorization boundary.
func (r *DelegationRepository) RevokeByChain(ctx context.Context, tenantID domain.UUID, chainID domain.UUID) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		_, err := r.pool.Exec(ctx, `
			UPDATE public.delegation_grants
			SET status = 'revoked'
			WHERE tenant_id = $1 AND chain_id = $2 AND status = 'active'
		`, uuid.UUID(tenantID), uuid.UUID(chainID))
		if err != nil {
			return mapDelegationError(err)
		}
		return nil
	})
}

// mapDelegationError maps database errors to domain sentinel errors.
func mapDelegationError(err error) error {
	if err == nil {
		return nil
	}
	errStr := err.Error()
	if contains(errStr, "unique_violation") || contains(errStr, "duplicate key") {
		return domain.ErrConflict
	}
	return err
}
