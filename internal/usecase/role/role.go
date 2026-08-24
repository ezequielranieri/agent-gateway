package role

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// UseCase handles role operations
type UseCase struct {
	repo RoleRepository
}

// RoleRepository defines the interface for role persistence
type RoleRepository interface {
	Create(ctx context.Context, role *domain.Role) error
	GetByID(ctx context.Context, id domain.UUID) (*domain.Role, error)
	ListByTenant(ctx context.Context, tenantID domain.UUID) ([]domain.Role, error)
	AssignPermissions(ctx context.Context, tenantID domain.UUID, roleID domain.UUID, permissions []string) error
	GetPermissions(ctx context.Context, tenantID domain.UUID, roleID domain.UUID) ([]string, error)
	Delete(ctx context.Context, id domain.UUID) error
	RevokePermissions(ctx context.Context, tenantID domain.UUID, roleID domain.UUID) error
}

// NewUseCase creates a new role use case
func NewUseCase(repo RoleRepository) *UseCase {
	return &UseCase{repo: repo}
}

// CreateInput represents the input for creating a role
type CreateInput struct {
	Name        string
	Description string
}

// CreateOutput represents the output for creating a role
type CreateOutput struct {
	Role *domain.Role
}

// Create creates a new role in the global catalog
func (uc *UseCase) Create(ctx context.Context, input CreateInput) (*CreateOutput, error) {
	role := &domain.Role{
		ID:          domain.NewUUID(),
		Name:        input.Name,
		Description: input.Description,
		CreatedAt:   domain.Now(),
	}

	if err := uc.repo.Create(ctx, role); err != nil {
		return nil, err
	}

	return &CreateOutput{Role: role}, nil
}

// GetByIDInput represents the input for getting a role by ID
type GetByIDInput struct {
	ID domain.UUID
}

// GetByIDOutput represents the output for getting a role by ID
type GetByIDOutput struct {
	Role *domain.Role
}

// GetByID retrieves a role by ID
func (uc *UseCase) GetByID(ctx context.Context, input GetByIDInput) (*GetByIDOutput, error) {
	role, err := uc.repo.GetByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	return &GetByIDOutput{Role: role}, nil
}

// ListByTenantInput represents the input for listing roles by tenant
type ListByTenantInput struct {
	TenantID domain.UUID
}

// ListByTenantOutput represents the output for listing roles by tenant
type ListByTenantOutput struct {
	Roles []domain.Role
}

// ListByTenant lists roles that have permissions assigned in a tenant
func (uc *UseCase) ListByTenant(ctx context.Context, input ListByTenantInput) (*ListByTenantOutput, error) {
	roles, err := uc.repo.ListByTenant(ctx, input.TenantID)
	if err != nil {
		return nil, err
	}
	return &ListByTenantOutput{Roles: roles}, nil
}

// AssignPermissionsInput represents the input for assigning permissions
type AssignPermissionsInput struct {
	TenantID    domain.UUID
	RoleID      domain.UUID
	Permissions []string
}

// AssignPermissions assigns permissions to a role within a tenant
func (uc *UseCase) AssignPermissions(ctx context.Context, input AssignPermissionsInput) error {
	// Validate role exists
	_, err := uc.repo.GetByID(ctx, input.RoleID)
	if err != nil {
		return err
	}

	return uc.repo.AssignPermissions(ctx, input.TenantID, input.RoleID, input.Permissions)
}

// GetPermissionsInput represents the input for getting permissions
type GetPermissionsInput struct {
	TenantID domain.UUID
	RoleID   domain.UUID
}

// GetPermissionsOutput represents the output for getting permissions
type GetPermissionsOutput struct {
	Permissions []string
}

// GetPermissions retrieves permissions for a role within a tenant
func (uc *UseCase) GetPermissions(ctx context.Context, input GetPermissionsInput) (*GetPermissionsOutput, error) {
	perms, err := uc.repo.GetPermissions(ctx, input.TenantID, input.RoleID)
	if err != nil {
		return nil, err
	}
	return &GetPermissionsOutput{Permissions: perms}, nil
}

// RevokePermissionsInput represents the input for revoking permissions
type RevokePermissionsInput struct {
	TenantID domain.UUID
	RoleID   domain.UUID
}

// RevokePermissions revokes all permissions for a role within a tenant
func (uc *UseCase) RevokePermissions(ctx context.Context, input RevokePermissionsInput) error {
	return uc.repo.RevokePermissions(ctx, input.TenantID, input.RoleID)
}

// DeleteInput represents the input for deleting a role
type DeleteInput struct {
	ID domain.UUID
}

// Delete deletes a role from the global catalog
func (uc *UseCase) Delete(ctx context.Context, input DeleteInput) error {
	return uc.repo.Delete(ctx, input.ID)
}