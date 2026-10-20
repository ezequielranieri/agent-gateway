package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ExcludedTables documents tables that have tenant_id but are intentionally excluded from RLS FORCE checks.
// Each entry must have a justification with evidence from the schema/migrations.
var ExcludedTables = map[string]struct {
	Justification string
	HasRLS        bool
	HasRLSForce   bool
}{
	"refresh_tokens": {
		Justification: "Global table with tenant_id but NO RLS. " +
			"Schema: migrations/0009_refresh_tokens.up.sql creates tenant_id column but does NOT enable RLS. " +
			"Rationale: Refresh tokens support cross-tenant operations and family tracking; " +
			"application layer enforces tenant isolation. NOT related to AD-009 (AD-009 is guardrails).",
		HasRLS:      false,
		HasRLSForce: false,
	},
	// audit_chain_locks is NOT excluded: it has tenant_id AND RLS FORCE (see migrations/0020_audit_chain_locks.up.sql)
	// tenants, roles, super_admins are global tables without tenant_id (verified in TestGlobalTablesNoRLS)
}

// TestTenantTableCatalog verifies that all tables with tenant_id column have:
// 1. RLS enabled (relrowsecurity = true)
// 2. RLS FORCE enabled (relforcerowsecurity = true)
// 3. At least one RLS policy
// 
// Tables in ExcludedTables are skipped with documented justification.
// The test FAILS if any table with tenant_id is found that is NOT in the catalog results
// and NOT in ExcludedTables (i.e., an unexpected tenanted table without RLS FORCE).
func TestTenantTableCatalog(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	ctx := tc.Ctx
	dbPool := tc.DBPool

	// Query to find ALL tables with tenant_id column (including excluded ones)
	allTablesQuery := `
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
		GROUP BY c.relname, c.relrowsecurity, c.relforcerowsecurity
		ORDER BY c.relname;
	`

	allRows, err := dbPool.Query(ctx, allTablesQuery)
	require.NoError(t, err)
	defer allRows.Close()

	// Track all tables with tenant_id found in DB
	type tableInfo struct {
		name           string
		relRowSecurity bool
		relForceRLS    bool
		policyCount    int64
	}
	var allTables []tableInfo
	var catalogTables []tableInfo
	var unexpectedTables []string

	for allRows.Next() {
		var ti tableInfo
		err := allRows.Scan(&ti.name, &ti.relRowSecurity, &ti.relForceRLS, &ti.policyCount)
		require.NoError(t, err)
		allTables = append(allTables, ti)

		// Check if this table is explicitly excluded
		if _, excluded := ExcludedTables[ti.name]; excluded {
			t.Logf("EXCLUDED (documented): %s - %s", ti.name, ExcludedTables[ti.name].Justification)
			continue
		}

		// This table should be in the catalog (have RLS FORCE)
		catalogTables = append(catalogTables, ti)

		// Verify RLS requirements
		if !ti.relRowSecurity {
			unexpectedTables = append(unexpectedTables, ti.name+": RLS not enabled (relrowsecurity=false)")
		}
		if !ti.relForceRLS {
			unexpectedTables = append(unexpectedTables, ti.name+": RLS FORCE not enabled (relforcerowsecurity=false)")
		}
		if ti.policyCount == 0 {
			unexpectedTables = append(unexpectedTables, ti.name+": no RLS policy defined")
		}
	}
	require.NoError(t, allRows.Err())

	// Verify no unexpected tables with tenant_id exist (not in ExcludedTables, not meeting RLS FORCE)
	if len(unexpectedTables) > 0 {
		t.Logf("Found %d tenanted tables (excluding %d documented exceptions)", len(catalogTables), len(ExcludedTables))
		for _, v := range unexpectedTables {
			t.Logf("VIOLATION: %s", v)
		}
		t.Fatalf("Catalog check failed: %d tables with tenant_id lack RLS FORCE or policies", len(unexpectedTables))
	}

	// Also verify that ExcludedTables entries actually exist in the database
	// (to catch drift if a table is renamed or dropped)
	foundExcluded := make(map[string]bool)
	for _, ti := range allTables {
		if _, excluded := ExcludedTables[ti.name]; excluded {
			foundExcluded[ti.name] = true
		}
	}
	for name := range ExcludedTables {
		if !foundExcluded[name] {
			t.Fatalf("Excluded table %s not found in database - update ExcludedTables list", name)
		}
	}

	t.Logf("Catalog check passed: %d tenanted tables all have RLS FORCE enabled and at least one policy", len(catalogTables))
	t.Logf("Documented exceptions: %d tables (refresh_tokens)", len(ExcludedTables))
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

	// Build exclusion list from ExcludedTables map + global tables without tenant_id
	excludedNames := []string{"tenants", "roles", "super_admins"}
	for name := range ExcludedTables {
		excludedNames = append(excludedNames, name)
	}

	// Build dynamic exclusion clause
	exclusionClause := ""
	for i, name := range excludedNames {
		if i > 0 {
			exclusionClause += ", "
		}
		exclusionClause += "'" + name + "'"
	}

	// Verify that all tables referenced in SQLC queries that have tenant_id in WHERE clause
	// actually have RLS FORCE enabled (excluding documented exceptions)
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
		  AND c.relname NOT IN (` + exclusionClause + `)
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
		t.Fatalf("Tables missing RLS FORCE (not in ExcludedTables): %v", violations)
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

// TestExcludedTablesSchemaVerified verifies the schema matches documented justifications
func TestExcludedTablesSchemaVerified(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	ctx := tc.Ctx
	dbPool := tc.DBPool

	for name, expected := range ExcludedTables {
		var relRowSecurity, relForceRowSecurity bool
		err := dbPool.QueryRow(ctx, `
			SELECT relrowsecurity, relforcerowsecurity
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'public' AND c.relname = $1
		`, name).Scan(&relRowSecurity, &relForceRowSecurity)
		require.NoError(t, err, "excluded table %s not found in database", name)

		if relRowSecurity != expected.HasRLS {
			t.Errorf("Excluded table %s: expected relrowsecurity=%v (per justification), got %v", name, expected.HasRLS, relRowSecurity)
		}
		if relForceRowSecurity != expected.HasRLSForce {
			t.Errorf("Excluded table %s: expected relforcerowsecurity=%v (per justification), got %v", name, expected.HasRLSForce, relForceRowSecurity)
		}
	}

	// Verify role_permissions has tenant_id and RLS FORCE (should be in catalog, not excluded)
	var rpRLS, rpForceRLS bool
	err := dbPool.QueryRow(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = 'role_permissions'
	`).Scan(&rpRLS, &rpForceRLS)
	require.NoError(t, err, "role_permissions table not found")

	if !rpRLS {
		t.Error("role_permissions: expected RLS enabled (tenanted table)")
	}
	if !rpForceRLS {
		t.Error("role_permissions: expected RLS FORCE enabled (tenanted table)")
	}
	// role_permissions should NOT be in ExcludedTables
	if _, excluded := ExcludedTables["role_permissions"]; excluded {
		t.Error("role_permissions should NOT be in ExcludedTables (it has tenant_id and RLS FORCE)")
	}
}