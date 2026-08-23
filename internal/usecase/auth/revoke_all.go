package auth

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// RevokeAllSessions revokes all sessions for a user (admin action)
func (uc *AuthUseCase) RevokeAllSessions(ctx context.Context, tenantID, userID domain.UUID) error {
	return uc.userRepo.RevokeAllSessions(ctx, tenantID, userID)
}