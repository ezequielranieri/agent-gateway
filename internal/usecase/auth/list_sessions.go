package auth

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// ListSessions lists active sessions for a user
func (uc *AuthUseCase) ListSessions(ctx context.Context, tenantID, userID domain.UUID) ([]domain.Session, error) {
	return uc.userRepo.ListSessions(ctx, tenantID, userID)
}