package main

import (
	"context"
	"os"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/crypto"
	"github.com/ezequielranieri/agent-gateway/internal/config"
)

var (
	app = kingpin.New("bootstrap", "Create the first Super Admin for agent-gateway")

	configPath = app.Flag("config", "Path to config file").Default("configs/config.yaml").String()
	email      = app.Flag("email", "Super Admin email").Required().String()
	password   = app.Flag("password", "Super Admin password (min 8 chars)").Required().String()
)

func main() {
	kingpin.MustParse(app.Parse(os.Args[1:]))

	// Configure logger
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	// Load configuration
	cfg := config.MustLoad(*configPath)

	logger := log.With().Str("service", "bootstrap").Logger()

	// Validate password length
	if len(*password) < 8 {
		logger.Fatal().Msg("Password must be at least 8 characters")
	}

	// Hash password with Argon2id adapter
	argon2Params := crypto.DefaultParams()
	phcHash, err := crypto.HashPassword(*password, argon2Params)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to hash password")
	}

	// Initialize database pool
	dbPool, err := initDB(cfg.Database, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize database")
	}
	defer dbPool.Close()

	// Insert Super Admin using bootstrap function
	ctx := context.Background()
	var superAdminID string
	err = dbPool.QueryRow(ctx, `
		SELECT bootstrap_super_admin($1, $2)
	`, *email, phcHash).Scan(&superAdminID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create Super Admin")
	}

	if superAdminID == "" {
		logger.Fatal().Msgf("Super Admin with email %s already exists", *email)
	}

	logger.Info().Str("email", *email).Str("id", superAdminID).Msg("Super Admin created successfully")
}

func initDB(cfg config.DatabaseConfig, logger zerolog.Logger) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, err
	}

	poolConfig.MaxConns = int32(cfg.MaxOpenConns)
	poolConfig.MinConns = int32(cfg.MaxIdleConns)
	poolConfig.MaxConnLifetime = cfg.ConnMaxLifetime
	poolConfig.MaxConnIdleTime = cfg.ConnMaxIdleTime

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ConnectTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, err
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}

	logger.Info().Msg("Database connected")
	return pool, nil
}