package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// WithTenant executes a function within a tenant context using a pgx transaction.
// This uses SET LOCAL to ensure the tenant GUC is scoped to the transaction only.
// All repository methods MUST run inside WithTenant for RLS to work correctly.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID domain.UUID, fn func(ctx context.Context) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Set tenant GUC LOCAL to this transaction
	_, err = tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID.String())
	if err != nil {
		return err
	}

	// Run the function within the transaction
	if err := fn(ctx); err != nil {
		return err
	}

	// Commit the transaction
	return tx.Commit(ctx)
}