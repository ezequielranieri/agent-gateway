package delegation

import (
	"errors"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

const (
	defaultMaxDepth   = 5
	defaultMaxFanOut  = 10
)

// ValidateScopeIntersection computes effective_scope = parent_scope ∩ granted_scope.
// Delegation MUST NEVER widen scope. Gateway-computed only — agent input is never trusted.
// Returns ErrScopeIntersectionEmpty if the intersection is empty.
func ValidateScopeIntersection(parentScope, grantedScope ScopeSet) (ScopeSet, error) {
	return parentScope.Intersection(grantedScope)
}

// ValidateDepth blocks delegation when depth >= maxDepth.
// Returns ErrDepthLimitExceeded when the limit is breached.
func ValidateDepth(depth, maxDepth int) error {
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	if depth >= maxDepth {
		return ErrDepthLimitExceeded
	}
	return nil
}

// ValidateFanOut blocks delegation when activeChildren >= maxFanOut.
// Returns ErrFanOutLimitExceeded when the limit is breached.
func ValidateFanOut(activeChildren, maxFanOut int) error {
	if maxFanOut <= 0 {
		maxFanOut = defaultMaxFanOut
	}
	if activeChildren >= maxFanOut {
		return ErrFanOutLimitExceeded
	}
	return nil
}

// ValidateBudget blocks delegation when budgetRemaining <= 0.
// Returns ErrBudgetExhausted when the budget is depleted.
func ValidateBudget(budgetRemaining int) error {
	if budgetRemaining <= 0 {
		return ErrBudgetExhausted
	}
	return nil
}

// ValidateTTL blocks delegation when the grant is expired or in a terminal status.
// Returns ErrGrantRevokedOrConsumed for expired or terminal grants.
func ValidateTTL(grant *Grant) error {
	if grant == nil {
		return ErrGrantRevokedOrConsumed
	}
	if grant.Status.IsTerminal() {
		return ErrGrantRevokedOrConsumed
	}
	if time.Now().After(grant.ExpiresAt) {
		return ErrGrantRevokedOrConsumed
	}
	return nil
}

// ValidateCycle blocks delegation when the delegate already appears in the ancestry chain.
// Returns ErrCycleDetected when a cycle would be created.
func ValidateCycle(delegateID domain.UUID, ancestors []domain.UUID) error {
	for _, ancestor := range ancestors {
		if ancestor == delegateID {
			return ErrCycleDetected
		}
	}
	return nil
}

// ValidateCrossTenant blocks delegation across different tenant_ids.
// Returns ErrCrossTenantDelegationProhibited when tenant IDs differ.
func ValidateCrossTenant(parentTenantID, childTenantID domain.UUID) error {
	if parentTenantID != childTenantID {
		return ErrCrossTenantDelegationProhibited
	}
	return nil
}

// ValidateHITLPropagation verifies that root_intent and hitl_classification
// are preserved from the root grant. Middleware evaluates HITL against root
// intent — NEVER against a re-packaged current action.
//
// Rules:
//   - If root is nil, skip validation (root action, no parent to compare).
//   - root_intent MUST match between root and child.
//   - hitl_classification MUST NOT be relaxed (child can be stricter, never weaker).
func ValidateHITLPropagation(root, child *Grant) error {
	if root == nil {
		return nil
	}
	if root.RootIntent != child.RootIntent {
		return errors.New("root intent mismatch: HITL propagation violated")
	}
	// Classification must not be relaxed — child must be at least as strict as root
	if !classificationAtLeastAsStrict(root.HITLClassification, child.HITLClassification) {
		return errors.New("hitl classification relaxed: HITL propagation violated")
	}
	return nil
}

// classificationRank returns the strictness rank of an HITL classification.
// Higher rank = stricter.
func classificationRank(c string) int {
	switch c {
	case "required":
		return 3
	case "advisory":
		return 2
	case "none":
		return 1
	default:
		return 0
	}
}

// classificationAtLeastAsStrict returns true if child is at least as strict as parent.
func classificationAtLeastAsStrict(parent, child string) bool {
	return classificationRank(child) >= classificationRank(parent)
}

// ValidateChainRevocation checks the generation counter at each auth boundary.
// If the grant's generation is behind the current chain generation, the grant
// has been revoked. Returns ErrGrantRevokedOrConsumed when generation mismatch.
func ValidateChainRevocation(grantGeneration, currentChainGeneration int) error {
	if grantGeneration < currentChainGeneration {
		return ErrGrantRevokedOrConsumed
	}
	return nil
}


