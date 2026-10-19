package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// TxFunc is a function that runs within a tenant transaction.
// The transaction already has app.current_tenant GUC set LOCAL.
type TxFunc func(ctx context.Context, tx pgx.Tx) error

// WithTenantTx executes a function within a tenant context, passing the transaction.
// The transaction has app.current_tenant set LOCAL, so all queries run on this tx
// will be subject to RLS policies. This is the ONLY safe way to run tenant-scoped queries.
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

// WithTenantTxBackground executes a function within a tenant context using a pgx transaction
// with background context for the transaction (for metadata reads that shouldn't fail due to request deadline).
// The transaction has app.current_tenant set LOCAL, so all queries run on this tx
// will be subject to RLS policies.
func WithTenantTxBackground(ctx context.Context, pool *pgxpool.Pool, tenantID domain.UUID, fn TxFunc) error {
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

	// Run the function within the transaction, passing the transaction
	if err := fn(bgCtx, tx); err != nil {
		return err
	}

	// Commit the transaction
	return tx.Commit(bgCtx)
}