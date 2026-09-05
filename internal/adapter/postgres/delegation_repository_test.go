package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/delegation"
)

// TestDelegationGrantDomainRoundtrip verifies that a delegation.Grant
// can be mapped to/from the database representation.
// This is a unit test for the mapping logic — no DB required.
func TestDelegationGrantDomainRoundtrip(t *testing.T) {
	grantID := domain.NewUUID()
	parentID := domain.NewUUID()
	chainID := domain.NewUUID()
	tenantID := domain.NewUUID()

	grant := &delegation.Grant{
		GrantID:           grantID,
		ParentGrantID:     &parentID,
		ChainID:           chainID,
		TenantID:          tenantID,
		DelegateIdentity:  "agent-1",
		GrantedScope:      delegation.NewScopeSet([]string{"read:data", "write:data"}),
		RootIntent:        "delete:records",
		HITLClassification: "required",
		Depth:             2,
		Generation:        1,
		ExpiresAt:         time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
		BudgetRemaining:   50,
		Status:            delegation.GrantStatusActive,
	}

	// Map to DB params
	params := grantToInsertParams(grant)

	// Verify mapping
	assert.Equal(t, uuid.UUID(grantID), params.ID)
	assert.Equal(t, uuid.UUID(parentID), uuid.UUID(params.ParentGrantID.Bytes))
	assert.True(t, params.ParentGrantID.Valid)
	assert.Equal(t, uuid.UUID(chainID), uuid.UUID(params.ChainID))
	assert.Equal(t, uuid.UUID(tenantID), uuid.UUID(params.TenantID))
	assert.Equal(t, "agent-1", params.DelegateIdentity)
	assert.Equal(t, "delete:records", params.RootIntent)
	assert.Equal(t, "required", params.HITLClassification)
	assert.Equal(t, int32(2), params.Depth)
	assert.Equal(t, int32(1), params.Generation)
	assert.Equal(t, int32(50), params.BudgetRemaining)
	assert.Equal(t, string(delegation.GrantStatusActive), params.Status)

	// Map back from DB row
	row := delegationGrantRow{
		ID:               uuid.UUID(grantID),
		ParentGrantID:    params.ParentGrantID,
		ChainID:          uuid.UUID(chainID),
		TenantID:         uuid.UUID(tenantID),
		DelegateIdentity: "agent-1",
		GrantedScope:     params.GrantedScope,
		RootIntent:       "delete:records",
		HITLClassification: "required",
		Depth:            2,
		Generation:       1,
		ExpiresAt:        grant.ExpiresAt,
		BudgetRemaining:  50,
		Status:           string(delegation.GrantStatusActive),
		CreatedAt:        time.Now(),
	}

	result := rowToGrant(row)

	assert.Equal(t, grantID, result.GrantID)
	assert.Equal(t, &parentID, result.ParentGrantID)
	assert.Equal(t, chainID, result.ChainID)
	assert.Equal(t, tenantID, result.TenantID)
	assert.Equal(t, "agent-1", result.DelegateIdentity)
	assert.Equal(t, delegation.ScopeSet{"read:data", "write:data"}, result.GrantedScope)
	assert.Equal(t, "delete:records", result.RootIntent)
	assert.Equal(t, "required", result.HITLClassification)
	assert.Equal(t, 2, result.Depth)
	assert.Equal(t, 1, result.Generation)
	assert.Equal(t, 50, result.BudgetRemaining)
	assert.Equal(t, delegation.GrantStatusActive, result.Status)
}

func TestDelegationGrantMapping_NilParent(t *testing.T) {
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ParentGrantID:    nil,
		ChainID:          domain.NewUUID(),
		TenantID:         domain.NewUUID(),
		DelegateIdentity: "root-agent",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            0,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	params := grantToInsertParams(grant)
	assert.False(t, params.ParentGrantID.Valid)
}

func TestDelegationGrantMapping_EmptyScope(t *testing.T) {
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         domain.NewUUID(),
		DelegateIdentity: "agent-1",
		GrantedScope:     nil,
		Depth:            1,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	params := grantToInsertParams(grant)
	assert.Nil(t, params.GrantedScope)
}

func TestUniqueViolationMapping(t *testing.T) {
	// Test that the error mapping function works
	err := mapDelegationError(context.Canceled)
	require.ErrorIs(t, err, context.Canceled)

	// nil stays nil
	err = mapDelegationError(nil)
	require.NoError(t, err)
}
