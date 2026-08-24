package user

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// UseCase handles user operations within a tenant
type UseCase struct {
	repo UserRepository
}

// UserRepository defines the interface for user persistence
type UserRepository interface {
	Create(ctx context.Context, tenantID domain.UUID, user *domain.User) error
	GetByEmail(ctx context.Context, tenantID domain.UUID, email string) (*domain.User, error)
	GetByID(ctx context.Context, tenantID domain.UUID, id domain.UUID) (*domain.User, error)
	Update(ctx context.Context, tenantID domain.UUID, user *domain.User) error
	ListSessions(ctx context.Context, tenantID domain.UUID, userID domain.UUID) ([]domain.Session, error)
	RevokeAllSessions(ctx context.Context, tenantID domain.UUID, userID domain.UUID) error
}

// NewUseCase creates a new user use case
func NewUseCase(repo UserRepository) *UseCase {
	return &UseCase{repo: repo}
}

// CreateInput represents the input for creating a user
type CreateInput struct {
	TenantID domain.UUID
	Email    string
	Password string
	Role     string // optional, defaults to "operator"
}

// CreateOutput represents the output for creating a user
type CreateOutput struct {
	User *domain.User
}

// Create creates a new user within a tenant
func (uc *UseCase) Create(ctx context.Context, input CreateInput) (*CreateOutput, error) {
	// Check if user already exists
	existing, err := uc.repo.GetByEmail(ctx, input.TenantID, input.Email)
	if err == nil && existing != nil {
		return nil, domain.ErrConflict
	}

	// Hash password would be done in auth use case
	// For now, we assume password is already hashed
	user := &domain.User{
		ID:           domain.NewUUID(),
		TenantID:     input.TenantID,
		Email:        input.Email,
		PasswordHash: "", // Will be set by auth use case
		Status:       domain.UserStatusActive,
		CreatedAt:    domain.Now(),
		UpdatedAt:    domain.Now(),
	}

	if err := uc.repo.Create(ctx, input.TenantID, user); err != nil {
		return nil, err
	}

	return &CreateOutput{User: user}, nil
}

// GetByEmailInput represents the input for getting a user by email
type GetByEmailInput struct {
	TenantID domain.UUID
	Email    string
}

// GetByEmailOutput represents the output for getting a user by email
type GetByEmailOutput struct {
	User *domain.User
}

// GetByEmail retrieves a user by email within a tenant
func (uc *UseCase) GetByEmail(ctx context.Context, input GetByEmailInput) (*GetByEmailOutput, error) {
	user, err := uc.repo.GetByEmail(ctx, input.TenantID, input.Email)
	if err != nil {
		return nil, err
	}
	return &GetByEmailOutput{User: user}, nil
}

// GetByIDInput represents the input for getting a user by ID
type GetByIDInput struct {
	TenantID domain.UUID
	ID       domain.UUID
}

// GetByIDOutput represents the output for getting a user by ID
type GetByIDOutput struct {
	User *domain.User
}

// GetByID retrieves a user by ID within a tenant
func (uc *UseCase) GetByID(ctx context.Context, input GetByIDInput) (*GetByIDOutput, error) {
	user, err := uc.repo.GetByID(ctx, input.TenantID, input.ID)
	if err != nil {
		return nil, err
	}
	return &GetByIDOutput{User: user}, nil
}

// UpdateInput represents the input for updating a user
type UpdateInput struct {
	TenantID domain.UUID
	User     *domain.User
}

// UpdateOutput represents the output for updating a user
type UpdateOutput struct {
	User *domain.User
}

// Update updates a user within a tenant
func (uc *UseCase) Update(ctx context.Context, input UpdateInput) (*UpdateOutput, error) {
	user := input.User
	user.UpdatedAt = domain.Now()
	if err := uc.repo.Update(ctx, input.TenantID, user); err != nil {
		return nil, err
	}
	return &UpdateOutput{User: user}, nil
}

// ListSessionsInput represents the input for listing user sessions
type ListSessionsInput struct {
	TenantID domain.UUID
	UserID   domain.UUID
}

// ListSessionsOutput represents the output for listing user sessions
type ListSessionsOutput struct {
	Sessions []domain.Session
}

// ListSessions lists active sessions for a user
func (uc *UseCase) ListSessions(ctx context.Context, input ListSessionsInput) (*ListSessionsOutput, error) {
	sessions, err := uc.repo.ListSessions(ctx, input.TenantID, input.UserID)
	if err != nil {
		return nil, err
	}
	return &ListSessionsOutput{Sessions: sessions}, nil
}

// RevokeAllSessionsInput represents the input for revoking all sessions
type RevokeAllSessionsInput struct {
	TenantID domain.UUID
	UserID   domain.UUID
}

// RevokeAllSessions revokes all sessions for a user
func (uc *UseCase) RevokeAllSessions(ctx context.Context, input RevokeAllSessionsInput) error {
	return uc.repo.RevokeAllSessions(ctx, input.TenantID, input.UserID)
}

// DeleteInput represents the input for deleting a user
type DeleteInput struct {
	TenantID domain.UUID
	ID       domain.UUID
}

// Delete deletes a user (soft delete)
func (uc *UseCase) Delete(ctx context.Context, input DeleteInput) error {
	user, err := uc.repo.GetByID(ctx, input.TenantID, input.ID)
	if err != nil {
		return err
	}
	user.Status = domain.UserStatusDeleted
	user.UpdatedAt = domain.Now()
	// Update would be called here
	return domain.ErrNotImplemented
}