package delegation

import (
	"testing"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ScopeIntersection tests ---

func TestScopeIntersection_NormalPass(t *testing.T) {
	parent := NewScopeSet([]string{"read:data", "write:data"})
	granted := NewScopeSet([]string{"read:data", "delete:data"})

	effective, err := ValidateScopeIntersection(parent, granted)
	require.NoError(t, err)
	assert.Equal(t, ScopeSet{"read:data"}, effective)
}

func TestScopeIntersection_NarrowingPreservesRead(t *testing.T) {
	parent := NewScopeSet([]string{"read:data"})
	granted := NewScopeSet([]string{"read:data", "write:data"})

	effective, err := ValidateScopeIntersection(parent, granted)
	require.NoError(t, err)
	assert.Equal(t, ScopeSet{"read:data"}, effective)
}

func TestScopeIntersection_EmptyIntersection(t *testing.T) {
	parent := NewScopeSet([]string{"read:data"})
	granted := NewScopeSet([]string{"write:data"})

	_, err := ValidateScopeIntersection(parent, granted)
	require.ErrorIs(t, err, ErrScopeIntersectionEmpty)
}

func TestScopeIntersection_NeverWidens(t *testing.T) {
	parent := NewScopeSet([]string{"read:data"})
	granted := NewScopeSet([]string{"read:data", "write:data", "admin:all"})

	effective, err := ValidateScopeIntersection(parent, granted)
	require.NoError(t, err)
	// Must never be wider than parent
	assert.LessOrEqual(t, len(effective), len(parent))
	assert.Equal(t, ScopeSet{"read:data"}, effective)
}

func TestScopeIntersection_BothEmpty(t *testing.T) {
	_, err := ValidateScopeIntersection(nil, nil)
	require.ErrorIs(t, err, ErrScopeIntersectionEmpty)
}

func TestScopeIntersection_ParentEmpty(t *testing.T) {
	granted := NewScopeSet([]string{"read:data"})
	_, err := ValidateScopeIntersection(nil, granted)
	require.ErrorIs(t, err, ErrScopeIntersectionEmpty)
}

func TestScopeIntersection_GrantedEmpty(t *testing.T) {
	parent := NewScopeSet([]string{"read:data"})
	_, err := ValidateScopeIntersection(parent, nil)
	require.ErrorIs(t, err, ErrScopeIntersectionEmpty)
}

func TestScopeIntersection_ExactMatch(t *testing.T) {
	parent := NewScopeSet([]string{"read:data", "write:data"})
	granted := NewScopeSet([]string{"read:data", "write:data"})

	effective, err := ValidateScopeIntersection(parent, granted)
	require.NoError(t, err)
	assert.Equal(t, ScopeSet{"read:data", "write:data"}, effective)
}

// --- ValidateDepth tests ---

func TestValidateDepth_NormalPass(t *testing.T) {
	err := ValidateDepth(2, 5)
	require.NoError(t, err)
}

func TestValidateDepth_BoundaryAtMax(t *testing.T) {
	err := ValidateDepth(5, 5)
	require.ErrorIs(t, err, ErrDepthLimitExceeded)
}

func TestValidateDepth_ExceedsMax(t *testing.T) {
	err := ValidateDepth(10, 5)
	require.ErrorIs(t, err, ErrDepthLimitExceeded)
}

func TestValidateDepth_DefaultMax(t *testing.T) {
	err := ValidateDepth(4, 0) // 0 means use default 5
	require.NoError(t, err)

	err = ValidateDepth(5, 0)
	require.ErrorIs(t, err, ErrDepthLimitExceeded)
}

func TestValidateDepth_ZeroDepth(t *testing.T) {
	err := ValidateDepth(0, 5)
	require.NoError(t, err)
}

// --- ValidateFanOut tests ---

func TestValidateFanOut_NormalPass(t *testing.T) {
	err := ValidateFanOut(5, 10)
	require.NoError(t, err)
}

func TestValidateFanOut_AtMax(t *testing.T) {
	err := ValidateFanOut(10, 10)
	require.ErrorIs(t, err, ErrFanOutLimitExceeded)
}

func TestValidateFanOut_ExceedsMax(t *testing.T) {
	err := ValidateFanOut(20, 10)
	require.ErrorIs(t, err, ErrFanOutLimitExceeded)
}

func TestValidateFanOut_DefaultMax(t *testing.T) {
	err := ValidateFanOut(9, 0) // 0 means use default 10
	require.NoError(t, err)

	err = ValidateFanOut(10, 0)
	require.ErrorIs(t, err, ErrFanOutLimitExceeded)
}

func TestValidateFanOut_ZeroChildren(t *testing.T) {
	err := ValidateFanOut(0, 10)
	require.NoError(t, err)
}

// --- ValidateTTL tests ---

func TestValidateTTL_NormalPass(t *testing.T) {
	grant := &Grant{
		ExpiresAt: time.Now().Add(1 * time.Hour),
		Status:    GrantStatusActive,
	}
	err := ValidateTTL(grant)
	require.NoError(t, err)
}

func TestValidateTTL_Expired(t *testing.T) {
	grant := &Grant{
		ExpiresAt: time.Now().Add(-1 * time.Hour),
		Status:    GrantStatusActive,
	}
	err := ValidateTTL(grant)
	require.ErrorIs(t, err, ErrGrantRevokedOrConsumed)
}

func TestValidateTTL_TerminalStatus(t *testing.T) {
	grant := &Grant{
		ExpiresAt: time.Now().Add(1 * time.Hour),
		Status:    GrantStatusRevoked,
	}
	err := ValidateTTL(grant)
	require.ErrorIs(t, err, ErrGrantRevokedOrConsumed)
}

func TestValidateTTL_ConsumedStatus(t *testing.T) {
	grant := &Grant{
		ExpiresAt: time.Now().Add(1 * time.Hour),
		Status:    GrantStatusConsumed,
	}
	err := ValidateTTL(grant)
	require.ErrorIs(t, err, ErrGrantRevokedOrConsumed)
}

func TestValidateTTL_ExpiredStatus(t *testing.T) {
	grant := &Grant{
		ExpiresAt: time.Now().Add(-1 * time.Hour),
		Status:    GrantStatusExpired,
	}
	err := ValidateTTL(grant)
	require.ErrorIs(t, err, ErrGrantRevokedOrConsumed)
}

// --- ValidateCrossTenant tests ---

func TestValidateCrossTenant_SameTenant(t *testing.T) {
	tenantA := domain.NewUUID()
	err := ValidateCrossTenant(tenantA, tenantA)
	require.NoError(t, err)
}

func TestValidateCrossTenant_DifferentTenant(t *testing.T) {
	tenantA := domain.NewUUID()
	tenantB := domain.NewUUID()
	err := ValidateCrossTenant(tenantA, tenantB)
	require.ErrorIs(t, err, ErrCrossTenantDelegationProhibited)
}

// --- ValidateCycle tests ---

func TestValidateCycle_NoCycle(t *testing.T) {
	ancestorA := domain.NewUUID()
	ancestorB := domain.NewUUID()
	childID := domain.NewUUID()

	ancestors := []domain.UUID{ancestorA, ancestorB}
	err := ValidateCycle(childID, ancestors)
	require.NoError(t, err)
}

func TestValidateCycle_CycleDetected(t *testing.T) {
	ancestorA := domain.NewUUID()
	childID := ancestorA // child is already in ancestor chain

	ancestors := []domain.UUID{ancestorA}
	err := ValidateCycle(childID, ancestors)
	require.ErrorIs(t, err, ErrCycleDetected)
}

func TestValidateCycle_EmptyAncestors(t *testing.T) {
	childID := domain.NewUUID()
	err := ValidateCycle(childID, nil)
	require.NoError(t, err)
}

func TestValidateCycle_DeepChain(t *testing.T) {
	a := domain.NewUUID()
	b := domain.NewUUID()
	c := domain.NewUUID()

	// a -> b -> c, c tries to delegate to a
	err := ValidateCycle(a, []domain.UUID{a, b, c})
	require.ErrorIs(t, err, ErrCycleDetected)
}

func TestValidateCycle_DeepChainNoCycle(t *testing.T) {
	a := domain.NewUUID()
	b := domain.NewUUID()
	c := domain.NewUUID()
	d := domain.NewUUID()

	// a -> b -> c -> d, d delegates to new e
	err := ValidateCycle(d, []domain.UUID{a, b, c})
	require.NoError(t, err)
}

// --- ValidateHITLPropagation tests ---

func TestValidateHITLPropagation_RequiredClassification(t *testing.T) {
	root := &Grant{
		RootIntent:         "delete:records",
		HITLClassification: "required",
	}
	child := &Grant{
		RootIntent:         "delete:records",
		HITLClassification: "required",
	}

	err := ValidateHITLPropagation(root, child)
	require.NoError(t, err)
}

func TestValidateHITLPropagation_MismatchedRootIntent(t *testing.T) {
	root := &Grant{
		RootIntent:         "delete:records",
		HITLClassification: "required",
	}
	child := &Grant{
		RootIntent:         "read:records", // re-packaged to hide intent
		HITLClassification: "required",
	}

	err := ValidateHITLPropagation(root, child)
	require.Error(t, err)
}

func TestValidateHITLPropagation_MismatchedClassification(t *testing.T) {
	root := &Grant{
		RootIntent:         "delete:records",
		HITLClassification: "required",
	}
	child := &Grant{
		RootIntent:         "delete:records",
		HITLClassification: "none", // downgraded
	}

	err := ValidateHITLPropagation(root, child)
	require.Error(t, err)
}

func TestValidateHITLPropagation_NilRoot(t *testing.T) {
	child := &Grant{
		RootIntent:         "read:data",
		HITLClassification: "none",
	}

	err := ValidateHITLPropagation(nil, child)
	require.NoError(t, err) // no root to check against
}

func TestValidateHITLPropagation_ClassificationEscalation(t *testing.T) {
	// child has stricter HITL than root — that's fine (never relaxes)
	root := &Grant{
		RootIntent:         "read:data",
		HITLClassification: "advisory",
	}
	child := &Grant{
		RootIntent:         "read:data",
		HITLClassification: "required",
	}

	err := ValidateHITLPropagation(root, child)
	require.NoError(t, err) // stricter is OK
}

// --- ValidateBudget tests ---

func TestValidateBudget_NormalPass(t *testing.T) {
	err := ValidateBudget(50)
	require.NoError(t, err)
}

func TestValidateBudget_Exhausted(t *testing.T) {
	err := ValidateBudget(0)
	require.ErrorIs(t, err, ErrBudgetExhausted)
}

func TestValidateBudget_Negative(t *testing.T) {
	err := ValidateBudget(-1)
	require.ErrorIs(t, err, ErrBudgetExhausted)
}

func TestValidateBudget_BoundaryOne(t *testing.T) {
	err := ValidateBudget(1)
	require.NoError(t, err)
}

// --- ValidateChainRevocation tests ---

func TestValidateChainRevocation_GenerationMismatch(t *testing.T) {
	// Grant was issued at generation 1, but chain was revoked to generation 2
	err := ValidateChainRevocation(1, 2)
	require.ErrorIs(t, err, ErrGrantRevokedOrConsumed)
}

func TestValidateChainRevocation_GenerationMatch(t *testing.T) {
	err := ValidateChainRevocation(2, 2)
	require.NoError(t, err)
}

func TestValidateChainRevocation_GenerationAhead(t *testing.T) {
	// Grant generation is higher than current — should not happen in normal flow
	err := ValidateChainRevocation(3, 2)
	require.NoError(t, err) // generation ahead means not revoked
}

func TestValidateChainRevocation_BothZero(t *testing.T) {
	err := ValidateChainRevocation(0, 0)
	require.NoError(t, err)
}
