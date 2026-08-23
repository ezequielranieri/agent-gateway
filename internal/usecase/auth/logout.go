package auth

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// LogoutRequest represents a logout request
type LogoutRequest struct {
	RefreshToken string
	RevokeAll    bool // If true, revoke all sessions for the user
}

// Logout revokes the current session (or all sessions)
func (uc *AuthUseCase) Logout(ctx context.Context, req LogoutRequest) error {
	tokenHash := postgres.HashToken(req.RefreshToken)

	storedToken, err := uc.refreshRepo.FindByTokenHash(ctx, tokenHash)
	if err != nil {
		return domain.ErrInvalidToken
	}

	if req.RevokeAll {
		// Revoke all sessions for this user
		return uc.refreshRepo.RevokeFamily(ctx, storedToken.FamilyID)
	}

	// Revoke just this token
	return uc.refreshRepo.Revoke(ctx, storedToken.ID)
}