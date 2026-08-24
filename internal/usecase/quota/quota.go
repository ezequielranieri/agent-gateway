package quota

import (
	"context"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// UseCase handles quota operations within a tenant
type UseCase struct {
	repo QuotaRepository
}

// QuotaRepository defines the interface for quota persistence
type QuotaRepository interface {
	Create(ctx context.Context, tenantID domain.UUID, quota *domain.Quota) error
	GetByScope(ctx context.Context, tenantID domain.UUID, scope domain.QuotaScope, scopeID domain.UUID) (*domain.Quota, error)
	ListByTenant(ctx context.Context, tenantID domain.UUID) ([]domain.Quota, error)
	Update(ctx context.Context, tenantID domain.UUID, quota *domain.Quota) error
	Delete(ctx context.Context, tenantID domain.UUID, scope domain.QuotaScope, scopeID domain.UUID) error
}

// NewUseCase creates a new quota use case
func NewUseCase(repo QuotaRepository) *UseCase {
	return &UseCase{repo: repo}
}

// CreateInput represents the input for creating a quota
type CreateInput struct {
	TenantID         domain.UUID
	Scope            domain.QuotaScope
	ScopeID          domain.UUID
	RequestsPerMin   int
	TokensPerMin     int
	ToolExecsPerMin  int
}

// CreateOutput represents the output for creating a quota
type CreateOutput struct {
	Quota *domain.Quota
}

// Create creates a new quota for a tenant
func (uc *UseCase) Create(ctx context.Context, input CreateInput) (*CreateOutput, error) {
	// Check if quota already exists
	existing, err := uc.repo.GetByScope(ctx, input.TenantID, input.Scope, input.ScopeID)
	if err == nil && existing != nil {
		return nil, domain.ErrConflict
	}

	quota := &domain.Quota{
		ID:              domain.NewUUID(),
		TenantID:        input.TenantID,
		Scope:           input.Scope,
		ScopeID:         input.ScopeID,
		RequestsPerMin:  input.RequestsPerMin,
		TokensPerMin:    input.TokensPerMin,
		ToolExecsPerMin: input.ToolExecsPerMin,
		CreatedAt:       domain.Now(),
		UpdatedAt:       domain.Now(),
	}

	if err := uc.repo.Create(ctx, input.TenantID, quota); err != nil {
		return nil, err
	}

	return &CreateOutput{Quota: quota}, nil
}

// GetByScopeInput represents the input for getting a quota by scope
type GetByScopeInput struct {
	TenantID domain.UUID
	Scope    domain.QuotaScope
	ScopeID  domain.UUID
}

// GetByScopeOutput represents the output for getting a quota by scope
type GetByScopeOutput struct {
	Quota *domain.Quota
}

// GetByScope retrieves a quota by scope
func (uc *UseCase) GetByScope(ctx context.Context, input GetByScopeInput) (*GetByScopeOutput, error) {
	quota, err := uc.repo.GetByScope(ctx, input.TenantID, input.Scope, input.ScopeID)
	if err != nil {
		return nil, err
	}
	return &GetByScopeOutput{Quota: quota}, nil
}

// ListByTenantInput represents the input for listing quotas by tenant
type ListByTenantInput struct {
	TenantID domain.UUID
}

// ListByTenantOutput represents the output for listing quotas by tenant
type ListByTenantOutput struct {
	Quotas []domain.Quota
}

// ListByTenant lists all quotas for a tenant
func (uc *UseCase) ListByTenant(ctx context.Context, input ListByTenantInput) (*ListByTenantOutput, error) {
	quotas, err := uc.repo.ListByTenant(ctx, input.TenantID)
	if err != nil {
		return nil, err
	}
	return &ListByTenantOutput{Quotas: quotas}, nil
}

// UpdateInput represents the input for updating a quota
type UpdateInput struct {
	TenantID         domain.UUID
	Scope            domain.QuotaScope
	ScopeID          domain.UUID
	RequestsPerMin   int
	TokensPerMin     int
	ToolExecsPerMin  int
}

// UpdateOutput represents the output for updating a quota
type UpdateOutput struct {
	Quota *domain.Quota
}

// Update updates a quota
func (uc *UseCase) Update(ctx context.Context, input UpdateInput) (*UpdateOutput, error) {
	quota, err := uc.repo.GetByScope(ctx, input.TenantID, input.Scope, input.ScopeID)
	if err != nil {
		return nil, err
	}

	quota.RequestsPerMin = input.RequestsPerMin
	quota.TokensPerMin = input.TokensPerMin
	quota.ToolExecsPerMin = input.ToolExecsPerMin
	quota.UpdatedAt = domain.Now()

	if err := uc.repo.Update(ctx, input.TenantID, quota); err != nil {
		return nil, err
	}

	return &UpdateOutput{Quota: quota}, nil
}

// DeleteInput represents the input for deleting a quota
type DeleteInput struct {
	TenantID domain.UUID
	Scope    domain.QuotaScope
	ScopeID  domain.UUID
}

// Delete deletes a quota
func (uc *UseCase) Delete(ctx context.Context, input DeleteInput) error {
	return uc.repo.Delete(ctx, input.TenantID, input.Scope, input.ScopeID)
}