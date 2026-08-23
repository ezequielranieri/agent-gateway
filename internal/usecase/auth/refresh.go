package auth

import (
	"context"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/jwt"
	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// RefreshRequest represents a refresh token request
type RefreshRequest struct {
	RefreshToken string
	UserAgent    string
	IP           string
}

// RefreshResponse represents a refresh token response
type RefreshResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
}

// Refresh rotates a refresh token and issues new access + refresh tokens
func (uc *AuthUseCase) Refresh(ctx context.Context, req RefreshRequest) (*RefreshResponse, error) {
	// Hash the provided refresh token
	tokenHash := postgres.HashToken(req.RefreshToken)

	// Find the refresh token
	storedToken, err := uc.refreshRepo.FindByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, domain.ErrInvalidToken
	}

	// Check if revoked
	if storedToken.Revoked {
		// Reuse detection: revoke entire family
		if err := uc.refreshRepo.RevokeFamily(ctx, storedToken.FamilyID); err != nil {
			return nil, err
		}
		return nil, domain.ErrReplayDetected
	}

	// Check if expired
	if storedToken.IsExpired() {
		return nil, domain.ErrExpired
	}

	// Get user to verify they still exist and are active
	user, err := uc.userRepo.GetByID(ctx, storedToken.TenantID, storedToken.UserID)
	if err != nil {
		return nil, domain.ErrInvalidToken
	}
	if !user.IsActive() {
		return nil, domain.ErrForbidden
	}

	// Generate new access token
	claims := jwt.Claims{
		UserID:   user.ID.String(),
		TenantID: storedToken.TenantID.String(),
		Role:     "admin", // TODO: Get actual role
		Scopes:   []string{"*"},
	}
	accessToken, err := uc.jwtService.IssueAccessToken(claims)
	if err != nil {
		return nil, err
	}

	// Generate new refresh token (rotate)
	newRefreshToken := generateOpaqueToken()
	newTokenHash := postgres.HashToken(newRefreshToken)

	newRefreshTokenEntity := &domain.RefreshToken{
		UserID:     user.ID,
		TenantID:   storedToken.TenantID,
		TokenHash:  newTokenHash,
		FamilyID:   storedToken.FamilyID,
		ExpiresAt:  time.Now().Add(7 * 24 * time.Hour),
		UserAgent:  req.UserAgent,
		IP:         req.IP,
	}

	// Rotate: persist new, revoke old
	if err := uc.refreshRepo.Rotate(ctx, storedToken.ID, newRefreshTokenEntity); err != nil {
		return nil, err
	}

	return &RefreshResponse{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		ExpiresIn:    900,
	}, nil
}