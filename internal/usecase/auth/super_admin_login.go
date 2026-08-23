package auth

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/crypto"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/jwt"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SuperAdminLoginInput represents the input for SuperAdmin login
type SuperAdminLoginInput struct {
	Email    string
	Password string
}

// SuperAdminLoginOutput represents the output for SuperAdmin login
type SuperAdminLoginOutput struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// SuperAdminLoginUseCase handles SuperAdmin authentication
type SuperAdminLoginUseCase struct {
	pool         *pgxpool.Pool
	tokenService jwt.TokenService
}

// NewSuperAdminLoginUseCase creates a new SuperAdminLoginUseCase
func NewSuperAdminLoginUseCase(pool *pgxpool.Pool, tokenService jwt.TokenService) *SuperAdminLoginUseCase {
	return &SuperAdminLoginUseCase{
		pool:         pool,
		tokenService: tokenService,
	}
}

// Execute runs the SuperAdmin login use case
func (uc *SuperAdminLoginUseCase) Execute(ctx context.Context, input SuperAdminLoginInput) (*SuperAdminLoginOutput, error) {
	// Find SuperAdmin by email
	var superAdminID domain.UUID
	var passwordHash string
	err := uc.pool.QueryRow(ctx, `
		SELECT id, password_hash FROM super_admins WHERE email = $1
	`, input.Email).Scan(&superAdminID, &passwordHash)
	if err != nil {
		return nil, domain.ErrInvalidCredentials
	}

	// Verify password using Argon2id
	if err := crypto.VerifyPassword(passwordHash, input.Password); err != nil {
		return nil, domain.ErrInvalidCredentials
	}

	// Issue access token (no tenant_id for SuperAdmin)
	accessToken, err := uc.tokenService.IssueSuperAdminToken(superAdminID)
	if err != nil {
		return nil, err
	}

	// Issue refresh token
	refreshToken, _, _, _, err := uc.tokenService.IssueRefreshToken(superAdminID)
	if err != nil {
		return nil, err
	}

	return &SuperAdminLoginOutput{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    900, // 15 minutes
	}, nil
}