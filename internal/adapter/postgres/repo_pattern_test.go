package postgres

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoBrokenTenantPattern ensures no repository uses the broken WithTenant/WithTenantBackground
// pattern that passes only ctx to the callback, which causes queries to run on the pool
// instead of the transaction with the tenant GUC set.
func TestNoBrokenTenantPattern(t *testing.T) {
	// Derive tenanted tables from migrations (tables with FORCE ROW LEVEL SECURITY)
	tenantedTables := getTenantedTablesFromMigrations(t)

	repoDir := "internal/adapter/postgres"
	files, err := filepath.Glob(filepath.Join(repoDir, "*_repository.go"))
	if err != nil {
		t.Fatalf("failed to glob repository files: %v", err)
	}

	var violations []string

	for _, file := range files {
		// Skip this test file itself
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("failed to read %s: %v", file, err)
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", file, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			callExpr, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			// Check for WithTenant or WithTenantBackground calls
			sel, ok := callExpr.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			funcName := sel.Sel.Name
			if funcName != "WithTenant" && funcName != "WithTenantBackground" {
				return true
			}

			// Check if the callback is a func literal with only ctx parameter
			if len(callExpr.Args) < 3 {
				return true
			}

			fnLit, ok := callExpr.Args[2].(*ast.FuncLit)
			if !ok {
				return true
			}

			// Check if the function literal has exactly one parameter named "ctx"
			if fnLit.Type.Params == nil || len(fnLit.Type.Params.List) != 1 {
				return true
			}

			param := fnLit.Type.Params.List[0]
			if len(param.Names) != 1 || param.Names[0].Name != "ctx" {
				return true
			}

			// Check if the callback body uses r.queries.X(ctx, ...) or pool.Query/Exec
			ast.Inspect(fnLit.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}

				// Check for r.queries.Method(ctx, ...)
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}

				// Check if it's a method on queries (r.queries.Something)
				if selExpr, ok := sel.X.(*ast.SelectorExpr); ok {
					if selExpr.Sel.Name == "queries" {
						// This is r.queries.Something(ctx, ...) - BROKEN PATTERN
						pos := fset.Position(call.Pos())
						violations = append(violations,
							filepath.Join(pos.Filename, funcName)+
								":"+pos.String()+
								": uses r.queries."+sel.Sel.Name+"(ctx, ...) inside WithTenant callback - queries run on pool without tenant GUC")
						return false
					}
				}

				// Check for pool.Query/Exec
				if selExpr, ok := sel.X.(*ast.SelectorExpr); ok {
					if selExpr.Sel.Name == "pool" {
						if sel.Sel.Name == "Query" || sel.Sel.Name == "QueryRow" || sel.Sel.Name == "Exec" {
							pos := fset.Position(call.Pos())
							violations = append(violations,
								filepath.Join(pos.Filename, funcName)+
									":"+pos.String()+
									": uses pool."+sel.Sel.Name+"(ctx, ...) inside WithTenant callback - runs on pool without tenant GUC")
							return false
						}
					}
				}

				return true
			})

			return true
		})
	}

	// Also check for pool.Query/Exec on tenanted tables anywhere in repo files (not just in WithTenant callbacks)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("failed to read %s: %v", file, err)
		}

		// Check for pool.Query/Exec/QueryRow with SQL containing tenanted table names
		poolQueryRegex := regexp.MustCompile(`(?s)pool\.(Query|QueryRow|Exec)\([^)]*`)
		matches := poolQueryRegex.FindAllStringIndex(string(src), -1)
		for _, match := range matches {
			sqlSnippet := string(src[match[0]:min(match[1]+200, len(src))]) // Get some context after the call
			for table := range tenantedTables {
				if strings.Contains(strings.ToLower(sqlSnippet), strings.ToLower(table)) {
					fset := token.NewFileSet()
					_, _ = parser.ParseFile(fset, file, src, 0)
					pos := fset.Position(token.Pos(match[0]))
					violations = append(violations,
						filepath.Join(file, "pool")+":"+pos.String()+
							": uses pool.Query/Exec/QueryRow on tenanted table "+table+" outside WithTenantTx")
					break
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Errorf("Found %d broken tenant patterns:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

// getTenantedTablesFromMigrations parses migration files to find tables with FORCE ROW LEVEL SECURITY
func getTenantedTablesFromMigrations(t *testing.T) map[string]bool {
	tenantedTables := make(map[string]bool)

	migrationsDir := "migrations"
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil {
		t.Fatalf("failed to glob migration files: %v", err)
	}

	// Regex to find ALTER TABLE ... ENABLE ROW LEVEL SECURITY and FORCE ROW LEVEL SECURITY
	forceRLSRegex := regexp.MustCompile(`(?i)ALTER TABLE\s+(?:public\.)?(\w+)\s+ENABLE ROW LEVEL SECURITY\s*;.*FORCE ROW LEVEL SECURITY`)
	enableRLSRegex := regexp.MustCompile(`(?i)ALTER TABLE\s+(?:public\.)?(\w+)\s+ENABLE ROW LEVEL SECURITY`)

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("failed to read migration %s: %v", file, err)
		}

		// Find tables with FORCE ROW LEVEL SECURITY
		matches := forceRLSRegex.FindAllStringSubmatch(string(content), -1)
		for _, match := range matches {
			if len(match) > 1 {
				tenantedTables[strings.ToLower(match[1])] = true
			}
		}

		// Also find tables with just ENABLE ROW LEVEL SECURITY (might be tenanted)
		// Check if they have a tenant_id column in CREATE TABLE
		enableMatches := enableRLSRegex.FindAllStringSubmatch(string(content), -1)
		for _, match := range enableMatches {
			if len(match) > 1 {
				table := strings.ToLower(match[1])
				// Verify it has tenant_id column
				createTableRegex := regexp.MustCompile(`(?i)CREATE TABLE\s+(?:public\.)?` + regexp.QuoteMeta(table) + `\s*\(([^)]+)`)
				createMatch := createTableRegex.FindStringSubmatch(string(content))
				if createMatch != nil && strings.Contains(strings.ToLower(createMatch[1]), "tenant_id") {
					tenantedTables[table] = true
				}
			}
		}
	}

	// Known global tables that have RLS but are not tenanted (should not be in tenantedTables)
	globalTables := map[string]bool{
		"tenants":        true,
		"roles":          true,
		"super_admins":   true,
		"audit_chain_locks": true, // Has tenant_id but is a lock table
	}

	for table := range globalTables {
		delete(tenantedTables, table)
	}

	return tenantedTables
}