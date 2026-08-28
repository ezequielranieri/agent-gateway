package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// TxFunc is a function that runs within a tenant transaction.
type TxFunc func(ctx context.Context, tx pgx.Tx) error

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

// WithTenantTx executes a function within a tenant context, passing the transaction.
// Use this when you need to run queries on the transaction (not the pool) for RLS to work.
func WithTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID domain.UUID, fn TxFunc) error {
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

	// Run the function within the transaction, passing the transaction
	if err := fn(ctx, tx); err != nil {
		return err
	}

	// Commit the transaction
	return tx.Commit(ctx)
}

// WithTenantBackground executes a function within a tenant context using a pgx transaction
// with background context for the transaction (for metadata reads that shouldn't fail due to request deadline).
// The tenant GUC is set on the transaction for RLS to work correctly.
func WithTenantBackground(ctx context.Context, pool *pgxpool.Pool, tenantID domain.UUID, fn func(ctx context.Context) error) error {
	// Use background context for transaction operations to avoid request deadline issues
	// for metadata/config reads that shouldn't be subject to client request timeout.
	bgCtx := context.Background()
	tx, err := pool.Begin(bgCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(bgCtx)

	// Set tenant GUC LOCAL to this transaction
	_, err = tx.Exec(bgCtx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID.String())
	if err != nil {
		return err
	}

	// Run the function within the transaction
	if err := fn(bgCtx); err != nil {
		return err
	}

	// Commit the transaction
	return tx.Commit(bgCtx)
}