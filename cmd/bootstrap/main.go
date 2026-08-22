package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/ezequielranieri/agent-gateway/internal/config"
	"golang.org/x/crypto/argon2"
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

	// Hash password with Argon2id
	// OWASP parameters: m=65536 (64 MiB), t=1, p=4, 32-byte key, 16-byte salt
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		logger.Fatal().Err(err).Msg("Failed to generate salt")
	}

	hash := argon2.IDKey([]byte(*password), salt, 1, 64*1024, 4, 32)

	// Encode in PHC format: $argon2id$v=19$m=65536,t=1,p=4$salt$hash
	phcHash := fmt.Sprintf("$argon2id$v=19$m=65536,t=1,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))

	// Initialize database pool
	dbPool, err := initDB(cfg.Database, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize database")
	}
	defer dbPool.Close()

	// Insert Super Admin
	ctx := context.Background()
	var exists bool
	err = dbPool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM public.super_admins WHERE email = $1)
	`, *email).Scan(&exists)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to check existing Super Admin")
	}

	if exists {
		logger.Fatal().Msgf("Super Admin with email %s already exists", *email)
	}

	_, err = dbPool.Exec(ctx, `
		INSERT INTO public.super_admins (email, password_hash)
		VALUES ($1, $2)
	`, *email, phcHash)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create Super Admin")
	}

	logger.Info().Str("email", *email).Msg("Super Admin created successfully")
	fmt.Printf("Super Admin created: %s\n", *email)
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