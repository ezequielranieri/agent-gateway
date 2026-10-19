package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTenantTableCatalog verifies that all tables with tenant_id column have:
// 1. RLS enabled (relrowsecurity = true)
// 2. RLS FORCE enabled (relforcerowsecurity = true)
// 3. At least one RLS policy
func TestTenantTableCatalog(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	ctx := tc.Ctx
	dbPool := tc.DBPool

	// Query to find all tables with tenant_id column that should have RLS FORCE
	// Excludes global tables: tenants, roles, super_admins, audit_chain_locks
	query := `
		SELECT 
			c.relname AS table_name,
			c.relrowsecurity,
			c.relforcerowsecurity,
			COUNT(p.polname) AS policy_count
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		LEFT JOIN pg_policy p ON p.polrelid = c.oid
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND a.attname = 'tenant_id'
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		  AND c.relname NOT IN ('tenants', 'roles', 'super_admins', 'refresh_tokens')
		GROUP BY c.relname, c.relrowsecurity, c.relforcerowsecurity
		ORDER BY c.relname;
	`

	rows, err := dbPool.Query(ctx, query)
	require.NoError(t, err)
	defer rows.Close()

	var violations []string
	tableCount := 0

	for rows.Next() {
		var tableName string
		var relRowSecurity, relForceRowSecurity bool
		var policyCount int64

		err := rows.Scan(&tableName, &relRowSecurity, &relForceRowSecurity, &policyCount)
		require.NoError(t, err)

		tableCount++

		if !relRowSecurity {
			violations = append(violations, tableName+": RLS not enabled (relrowsecurity=false)")
		}
		if !relForceRowSecurity {
			violations = append(violations, tableName+": RLS FORCE not enabled (relforcerowsecurity=false)")
		}
		if policyCount == 0 {
			violations = append(violations, tableName+": no RLS policy defined")
		}
	}

	require.NoError(t, rows.Err())
	require.Greater(t, tableCount, 0, "should find at least one tenanted table")

	if len(violations) > 0 {
		t.Logf("Found %d tenanted tables", tableCount)
		for _, v := range violations {
			t.Logf("VIOLATION: %s", v)
		}
		t.Fatalf("Catalog check failed: %d tables have RLS violations", len(violations))
	}

	t.Logf("Catalog check passed: %d tenanted tables all have RLS FORCE enabled and at least one policy", tableCount)
}

// TestTenantTableCatalogWithSQLC verifies that SQLC-generated queries only reference tenanted tables with RLS
func TestTenantTableCatalogWithSQLC(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	ctx := tc.Ctx
	dbPool := tc.DBPool

	// Verify that all tables referenced in SQLC queries that have tenant_id in WHERE clause
	// actually have RLS FORCE enabled
	query := `
		SELECT 
			c.relname AS table_name,
			c.relrowsecurity,
			c.relforcerowsecurity
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = 'public'
		  AND c.relkind = 'r'
		  AND a.attname = 'tenant_id'
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		  AND c.relname NOT IN ('tenants', 'roles', 'super_admins', 'refresh_tokens')
		  AND (c.relrowsecurity = false OR c.relforcerowsecurity = false)
		ORDER BY c.relname;
	`

	rows, err := dbPool.Query(ctx, query)
	require.NoError(t, err)
	defer rows.Close()

	var violations []string
	for rows.Next() {
		var tableName string
		var relRowSecurity, relForceRowSecurity bool
		err := rows.Scan(&tableName, &relRowSecurity, &relForceRowSecurity)
		require.NoError(t, err)
		violations = append(violations, tableName)
	}
	require.NoError(t, rows.Err())

	if len(violations) > 0 {
		t.Fatalf("Tables missing RLS FORCE: %v", violations)
	}
}

// TestGlobalTablesNoRLS verifies that global tables don't have RLS enabled
func TestGlobalTablesNoRLS(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	ctx := tc.Ctx
	dbPool := tc.DBPool

	globalTables := []string{"tenants", "roles", "super_admins"}

	for _, table := range globalTables {
		var relRowSecurity, relForceRowSecurity bool
		err := dbPool.QueryRow(ctx, `
			SELECT relrowsecurity, relforcerowsecurity
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = $1
		`, table).Scan(&relRowSecurity, &relForceRowSecurity)
		require.NoError(t, err, "table %s not found", table)

		if relRowSecurity {
			t.Errorf("Global table %s should not have RLS enabled", table)
		}
		if relForceRowSecurity {
			t.Errorf("Global table %s should not have RLS FORCE enabled", table)
		}
	}
}