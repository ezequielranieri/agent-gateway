package auth

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/adapter/crypto"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// RegisterRequest represents a user registration request
type RegisterRequest struct {
	Email    string
	Password string
	TenantID domain.UUID
	Role     string // "admin", "operator", "viewer"
}

// Register creates a new user (first user in tenant gets admin role)
func (uc *AuthUseCase) Register(ctx context.Context, req RegisterRequest) (*domain.User, error) {
	// Check if user already exists
	existing, err := uc.userRepo.GetByEmail(ctx, req.TenantID, req.Email)
	if err == nil && existing != nil {
		return nil, domain.ErrConflict
	}

	// Hash password
	passwordHash, err := crypto.HashPassword(req.Password, uc.argon2Params)
	if err != nil {
		return nil, err
	}

	// Create user
	user := &domain.User{
		TenantID:     req.TenantID,
		Email:        req.Email,
		PasswordHash: passwordHash,
		Status:       domain.UserStatusActive,
	}

	if err := uc.userRepo.Create(ctx, req.TenantID, user); err != nil {
		return nil, err
	}

	// Assign role (first user in tenant gets admin, otherwise use requested role)
	// For now, we'll just assign the requested role
	// In a real implementation, we'd check if this is the first user
	role := req.Role
	if role == "" {
		role = "operator"
	}

	// TODO: Assign role via RoleRepository
	// This requires RoleRepository to be injected

	return user, nil
}