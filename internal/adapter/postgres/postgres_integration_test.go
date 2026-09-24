//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	pgmodule "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/types/network"
	"github.com/pressly/goose/v3"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func setupTestContainer(t *testing.T) (*pgxpool.Pool, *sql.DB, func()) {
	ctx := context.Background()

	// Start PostgreSQL container
	pgContainer, err := pgmodule.Run(ctx,
		"postgres:16-alpine",
		pgmodule.WithDatabase("agent_gateway"),
		pgmodule.WithUsername("postgres"),
		pgmodule.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(wait.ForSQL("5432/tcp", "pgx/v5", func(host string, port network.Port) string {
			return fmt.Sprintf("postgres://postgres:postgres@%s:%d/agent_gateway?sslmode=disable", host, port.Num())
		})),
	)
	if err != nil {
		t.Fatal(err)
	}

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	// Create gateway role (mirrors CI step "Create test role and set test DSN")
	// This role is referenced by migration 0014_pricing_tables.sql GRANT statements
	_, err = pool.Exec(ctx, `
		CREATE ROLE gateway WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD 'gateway';
		GRANT USAGE ON SCHEMA public TO gateway;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO gateway;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO gateway;
	`)
	if err != nil {
		t.Fatalf("Failed to create gateway role: %v", err)
	}

	// Apply migrations using sql.DB (goose requires sql.DB)
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}

	// Get absolute path to migrations directory - find repo root
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("Failed to find repo root: %v", err)
	}
	migrationsPath := filepath.Join(repoRoot, "migrations")

	// Apply migrations
	goose.SetBaseFS(nil)
	if err := goose.Up(sqlDB, migrationsPath); err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		pool.Close()
		sqlDB.Close()
		pgContainer.Terminate(context.Background())
	}

	return pool, sqlDB, cleanup
}

// findRepoRoot finds the repository root by looking for the migrations directory
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Walk up the directory tree looking for migrations directory OR go.mod
	for {
		migrationsPath := filepath.Join(dir, "migrations")
		if _, err := os.Stat(migrationsPath); err == nil {
			return dir, nil
		}
		goModPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find repository root (no migrations dir or go.mod)")
}

func TestPostgresContainerSetup(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	pool, sqlDB, cleanup := setupTestContainer(t)
	defer cleanup()

	// Verify connection
	var result int
	err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatal(err)
	}
	if result != 1 {
		t.Fatalf("Expected 1, got %d", result)
	}

	_ = sqlDB // suppress unused variable warning
	fmt.Println("PostgreSQL container setup successful!")
}