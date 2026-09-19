package postgres

import (
	"context"
	"database/sql"
	"fmt"
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

	// Apply migrations using sql.DB (goose requires sql.DB)
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}

	// Apply migrations
	goose.SetBaseFS(nil)
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		pool.Close()
		sqlDB.Close()
		pgContainer.Terminate(context.Background())
	}

	return pool, sqlDB, cleanup
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