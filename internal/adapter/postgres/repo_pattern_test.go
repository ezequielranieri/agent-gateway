package postgres

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoBrokenTenantPattern ensures no repository uses the broken WithTenant/WithTenantBackground
// pattern that passes only ctx to the callback, which causes queries to run on the pool
// instead of the transaction with the tenant GUC set.
func TestNoBrokenTenantPattern(t *testing.T) {
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
							filepath.Join(pos.Filename, funcName) + 
							":" + pos.String() + 
							": uses r.queries." + sel.Sel.Name + "(ctx, ...) inside WithTenant callback - queries run on pool without tenant GUC")
						return false
					}
				}

				// Check for pool.Query/Exec
				if selExpr, ok := sel.X.(*ast.SelectorExpr); ok {
					if selExpr.Sel.Name == "pool" {
						if sel.Sel.Name == "Query" || sel.Sel.Name == "QueryRow" || sel.Sel.Name == "Exec" {
							pos := fset.Position(call.Pos())
							violations = append(violations,
								filepath.Join(pos.Filename, funcName) +
								":" + pos.String() +
								": uses pool." + sel.Sel.Name + "(ctx, ...) inside WithTenant callback - runs on pool without tenant GUC")
							return false
						}
					}
				}

				return true
			})

			return true
		})
	}

	if len(violations) > 0 {
		t.Errorf("Found %d broken tenant patterns:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}