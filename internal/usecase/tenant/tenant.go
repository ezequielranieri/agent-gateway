package tenant

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// UseCase handles tenant operations
type UseCase struct {
	repo TenantRepository
}

// TenantRepository defines the interface for tenant persistence
type TenantRepository interface {
	Create(ctx context.Context, tenant *domain.Tenant) error
	GetByID(ctx context.Context, id domain.UUID) (*domain.Tenant, error)
	GetByName(ctx context.Context, name string) (*domain.Tenant, error)
	UpdateStatus(ctx context.Context, id domain.UUID, status domain.TenantStatus) error
	ListTenants(ctx context.Context, limit, offset int) ([]domain.Tenant, error)
}

// NewUseCase creates a new tenant use case
func NewUseCase(repo TenantRepository) *UseCase {
	return &UseCase{repo: repo}
}

// CreateInput represents the input for creating a tenant
type CreateInput struct {
	Name string
}

// CreateOutput represents the output for creating a tenant
type CreateOutput struct {
	Tenant *domain.Tenant
}

// Create creates a new tenant
func (uc *UseCase) Create(ctx context.Context, input CreateInput) (*CreateOutput, error) {
	// Check if tenant already exists
	existing, err := uc.repo.GetByName(ctx, input.Name)
	if err == nil && existing != nil {
		return nil, domain.ErrConflict
	}

	tenant := &domain.Tenant{
		ID:        domain.NewUUID(),
		Name:      input.Name,
		Status:    domain.TenantStatusActive,
		CreatedAt: domain.Now(),
	}

	if err := uc.repo.Create(ctx, tenant); err != nil {
		return nil, err
	}

	return &CreateOutput{Tenant: tenant}, nil
}

// GetInput represents the input for getting a tenant
type GetInput struct {
	ID domain.UUID
}

// GetOutput represents the output for getting a tenant
type GetOutput struct {
	Tenant *domain.Tenant
}

// Get retrieves a tenant by ID
func (uc *UseCase) Get(ctx context.Context, input GetInput) (*GetOutput, error) {
	tenant, err := uc.repo.GetByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	return &GetOutput{Tenant: tenant}, nil
}

// ListInput represents the input for listing tenants
type ListInput struct {
	Limit  int
	Offset int
}

// ListOutput represents the output for listing tenants
type ListOutput struct {
	Tenants []domain.Tenant
}

// List lists tenants with pagination
func (uc *UseCase) List(ctx context.Context, input ListInput) (*ListOutput, error) {
	if input.Limit <= 0 {
		input.Limit = 50
	}
	if input.Limit > 100 {
		input.Limit = 100
	}
	if input.Offset < 0 {
		input.Offset = 0
	}

	tenants, err := uc.repo.ListTenants(ctx, input.Limit, input.Offset)
	if err != nil {
		return nil, err
	}
	return &ListOutput{Tenants: tenants}, nil
}

// UpdateStatusInput represents the input for updating tenant status
type UpdateStatusInput struct {
	ID     domain.UUID
	Status domain.TenantStatus
}

// UpdateStatusOutput represents the output for updating tenant status
type UpdateStatusOutput struct {
	Tenant *domain.Tenant
}

// UpdateStatus updates a tenant's status
func (uc *UseCase) UpdateStatus(ctx context.Context, input UpdateStatusInput) (*UpdateStatusOutput, error) {
	tenant, err := uc.repo.GetByID(ctx, input.ID)
	if err != nil {
		return nil, err
	}

	tenant.Status = input.Status
	if err := uc.repo.UpdateStatus(ctx, input.ID, input.Status); err != nil {
		return nil, err
	}

	return &UpdateStatusOutput{Tenant: tenant}, nil
}

// DeleteInput represents the input for deleting a tenant
type DeleteInput struct {
	ID domain.UUID
}

// Delete deletes a tenant
func (uc *UseCase) Delete(ctx context.Context, input DeleteInput) error {
	_, err := uc.repo.GetByID(ctx, input.ID)
	if err != nil {
		return err
	}
	// Note: Actual delete not implemented in repository yet
	return domain.ErrNotImplemented
}