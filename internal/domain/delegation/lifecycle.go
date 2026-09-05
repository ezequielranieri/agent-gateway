package delegation

import "errors"

// GrantStatus represents the lifecycle status of a delegation grant.
type GrantStatus string

const (
	GrantStatusIssued  GrantStatus = "issued"
	GrantStatusActive  GrantStatus = "active"
	GrantStatusRevoked GrantStatus = "revoked"
	GrantStatusExpired GrantStatus = "expired"
	GrantStatusConsumed GrantStatus = "consumed"
)

// IsTerminal returns true if the status is a terminal state (no further transitions allowed).
func (s GrantStatus) IsTerminal() bool {
	switch s {
	case GrantStatusRevoked, GrantStatusExpired, GrantStatusConsumed:
		return true
	default:
		return false
	}
}

// CanTransitionTo returns true if a transition from this status to the target status is allowed.
//
// Allowed transitions:
//
//	issued  -> active, revoked, expired
//	active  -> revoked, expired, consumed
//	revoked -> (none, terminal)
//	expired -> (none, terminal)
//	consumed -> (none, terminal)
func (s GrantStatus) CanTransitionTo(target GrantStatus) bool {
	if s.IsTerminal() {
		return false
	}
	if s == target {
		return false // no self-transitions
	}
	switch s {
	case GrantStatusIssued:
		return target == GrantStatusActive || target == GrantStatusRevoked || target == GrantStatusExpired
	case GrantStatusActive:
		return target == GrantStatusRevoked || target == GrantStatusExpired || target == GrantStatusConsumed
	default:
		return false
	}
}

// ApplyTransition attempts to transition the grant to the target status.
// Returns ErrInvalidGrantTransition if the transition is not allowed.
func (g *Grant) ApplyTransition(target GrantStatus) error {
	if !g.Status.CanTransitionTo(target) {
		return ErrInvalidGrantTransition
	}
	g.Status = target
	return nil
}

// Sentinel errors for delegation lifecycle and constraints.
var (
	// ErrScopeIntersectionEmpty is returned when the intersection of parent scope
	// and granted scope is empty, meaning the delegation would grant no permissions.
	ErrScopeIntersectionEmpty = errors.New("scope intersection is empty")

	// ErrCrossTenantDelegationProhibited is returned when delegation crosses tenant boundaries.
	ErrCrossTenantDelegationProhibited = errors.New("cross-tenant delegation is prohibited")

	// ErrDepthLimitExceeded is returned when delegation depth exceeds the configured maximum.
	ErrDepthLimitExceeded = errors.New("delegation depth limit exceeded")

	// ErrFanOutLimitExceeded is returned when a parent's active children count exceeds the limit.
	ErrFanOutLimitExceeded = errors.New("delegation fan-out limit exceeded")

	// ErrBudgetExhausted is returned when the inherited budget pool is depleted.
	ErrBudgetExhausted = errors.New("delegation budget exhausted")

	// ErrCycleDetected is returned when delegation would create a cycle in the grant chain.
	ErrCycleDetected = errors.New("delegation cycle detected")

	// ErrGrantRevokedOrConsumed is returned when a revoked, expired, or consumed grant is presented.
	ErrGrantRevokedOrConsumed = errors.New("grant is revoked or consumed")

	// ErrInvalidGrantTransition is returned when a grant status transition is not allowed.
	ErrInvalidGrantTransition = errors.New("invalid grant status transition")
)
