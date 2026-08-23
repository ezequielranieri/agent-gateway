package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/crypto"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/jwt"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// AuthUseCase handles authentication operations
type AuthUseCase struct {
	userRepo     *postgres.UserRepository
	refreshRepo  *postgres.RefreshTokenRepository
	jwtService   *jwt.AuthService
	argon2Params *crypto.Argon2idParams
}

// NewAuthUseCase creates a new auth use case
func NewAuthUseCase(
	userRepo *postgres.UserRepository,
	refreshRepo *postgres.RefreshTokenRepository,
	jwtService *jwt.AuthService,
) *AuthUseCase {
	return &AuthUseCase{
		userRepo:     userRepo,
		refreshRepo:  refreshRepo,
		jwtService:   jwtService,
		argon2Params: crypto.DefaultParams(),
	}
}

// LoginRequest represents a login request
type LoginRequest struct {
	Email     string
	Password  string
	TenantID  domain.UUID
	UserAgent string
	IP        string
}

// LoginResponse represents a login response
type LoginResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// Login authenticates a user and returns access + refresh tokens
func (uc *AuthUseCase) Login(ctx context.Context, req LoginRequest) (*LoginResponse, error) {
	// Get user by email
	user, err := uc.userRepo.GetByEmail(ctx, req.TenantID, req.Email)
	if err != nil {
		return nil, domain.ErrInvalidCredentials
	}

	// Verify password
	if err := crypto.VerifyPassword(req.Password, user.PasswordHash); err != nil {
		return nil, domain.ErrInvalidCredentials
	}

	// Check user status
	if !user.IsActive() {
		return nil, domain.ErrForbidden
	}

	// Generate access token
	claims := jwt.Claims{
		UserID:   user.ID.String(),
		TenantID: req.TenantID.String(),
		Role:     "admin", // TODO: Get actual role from user_roles
		Scopes:   []string{"*"},
	}
	accessToken, err := uc.jwtService.IssueAccessToken(claims)
	if err != nil {
		return nil, err
	}

	// Generate refresh token
	refreshToken := generateOpaqueToken()
	refreshTokenHash := postgres.HashToken(refreshToken)

	familyID := domain.NewUUID()
	expiresAt := time.Now().Add(7 * 24 * time.Hour) // 7 days

	refreshTokenEntity := &domain.RefreshToken{
		UserID:     user.ID,
		TenantID:   req.TenantID,
		TokenHash:  refreshTokenHash,
		FamilyID:   familyID,
		ExpiresAt:  expiresAt,
		UserAgent:  req.UserAgent,
		IP:         req.IP,
	}

	if err := uc.refreshRepo.Store(ctx, refreshTokenEntity); err != nil {
		return nil, err
	}

	return &LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    900, // 15 minutes
	}, nil
}

// generateOpaqueToken generates a cryptographically secure random token
func generateOpaqueToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}