package middleware

import (
	"context"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/delegation"
)

// GrantKey is the context key for the delegation grant.
type GrantKey struct{}

// RootGrantKey is the context key for the root grant in the delegation chain.
type RootGrantKey struct{}

// AncestorsKey is the context key for the ancestor chain (slice of UUIDs).
type AncestorsKey struct{}

// DelegationRepository defines the port for delegation grant persistence.
type DelegationRepository interface {
	GetByID(ctx context.Context, tenantID domain.UUID, grantID domain.UUID) (*delegation.Grant, error)
	CountActiveChildren(ctx context.Context, tenantID domain.UUID, parentGrantID domain.UUID) (int, error)
	GetChainGeneration(ctx context.Context, tenantID domain.UUID, chainID domain.UUID) (int, error)
}

// DelegationBudgetDecrementor defines the port for atomic budget decrement.
type DelegationBudgetDecrementor interface {
	// DecrementBudget atomically decrements the budget for a chain.
	// Returns the remaining budget after decrement, or error if exhausted.
	DecrementBudget(ctx context.Context, chainID domain.UUID, amount int) (int, error)
}

// DelegationConfig holds configuration for the delegation middleware.
type DelegationConfig struct {
	Repository   DelegationRepository
	BudgetDec    DelegationBudgetDecrementor
	MaxDepth     int
	MaxFanOut    int
	Logger       zerolog.Logger
	FailOpen     bool
}

// NewDelegation creates a delegation validation middleware.
// It must be positioned AFTER TenantMW and BEFORE RateLimitMW in the chi chain.
//
// The middleware:
//  1. Extracts grant from context (if present — non-delegated requests pass through).
//  2. Validates grant lifecycle (TTL, status).
//  3. Validates scope intersection (gateway-computed, never widens).
//  4. Validates depth limit.
//  5. Validates fan-out limit.
//  6. Decrements budget via Redis atomic Lua.
//  7. Validates cycle detection.
//  8. Validates cross-tenant guard.
//  9. Validates chain revocation (generation counter).
//  10. Validates HITL propagation from root.
func NewDelegation(cfg DelegationConfig) func(http.Handler) http.Handler {
	// Fail-fast: required production dependencies MUST be provided.
	// Silent nil → skip is a security hole (budget enforcement, chain generation).
	if cfg.BudgetDec == nil {
		panic("delegation middleware: BudgetDec is required — cannot create middleware without budget enforcement")
	}
	if cfg.Repository == nil {
		panic("delegation middleware: Repository is required — cannot create middleware without chain generation checks")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logger := cfg.Logger.With().Str("middleware", "delegation").Logger()

			// Get tenant ID from context (set by TenantMW)
			tenantID, ok := GetTenantID(r)
			if !ok {
				// No tenant context — not a delegation request, pass through
				next.ServeHTTP(w, r)
				return
			}

			// Get grant from context (set by auth handler or upstream middleware)
			grant := GetGrantFromContext(r)
			if grant == nil {
				// No delegation grant — standard request, pass through
				next.ServeHTTP(w, r)
				return
			}

			// Validate tenant binding: grant must belong to this tenant
			if grant.TenantID != tenantID {
				logger.Warn().
					Str("grant_tenant", grant.TenantID.String()).
					Str("request_tenant", tenantID.String()).
					Msg("Cross-tenant delegation attempt")
				WriteError(w, r, http.StatusForbidden, delegation.ErrCrossTenantDelegationProhibited)
				return
			}

			// 1. Validate TTL and lifecycle status
			if err := delegation.ValidateTTL(grant); err != nil {
				logger.Warn().
					Err(err).
					Str("grant_id", grant.GrantID.String()).
					Msg("Grant TTL/lifecycle validation failed")
				WriteError(w, r, http.StatusForbidden, delegation.ErrGrantRevokedOrConsumed)
				return
			}

			// 2. Validate scope intersection (gateway-computed)
			parentScope := GetParentScopeFromContext(r)
			if len(parentScope) > 0 {
				if _, err := delegation.ValidateScopeIntersection(parentScope, grant.GrantedScope); err != nil {
					logger.Warn().
						Err(err).
						Str("grant_id", grant.GrantID.String()).
						Msg("Scope intersection validation failed")
					WriteError(w, r, http.StatusForbidden, delegation.ErrScopeIntersectionEmpty)
					return
				}
			}

			// 3. Validate depth limit
			if err := delegation.ValidateDepth(grant.Depth, cfg.MaxDepth); err != nil {
				logger.Warn().
					Err(err).
					Int("depth", grant.Depth).
					Msg("Depth limit exceeded")
				WriteError(w, r, http.StatusForbidden, delegation.ErrDepthLimitExceeded)
				return
			}

			// 4. Validate cross-tenant guard
			if err := delegation.ValidateCrossTenant(tenantID, grant.TenantID); err != nil {
				logger.Warn().
					Err(err).
					Msg("Cross-tenant delegation blocked")
				WriteError(w, r, http.StatusForbidden, delegation.ErrCrossTenantDelegationProhibited)
				return
			}

			// 5. Validate cycle detection
			ancestors := GetAncestorsFromContext(r)
			if err := delegation.ValidateCycle(delegateID(r), ancestors); err != nil {
				logger.Warn().
					Err(err).
					Msg("Delegation cycle detected")
				WriteError(w, r, http.StatusForbidden, delegation.ErrCycleDetected)
				return
			}

			// 6. Validate chain revocation (generation counter)
			currentGen, err := cfg.Repository.GetChainGeneration(r.Context(), tenantID, grant.ChainID)
			if err == nil {
				// Grant generation is set at issuance; compare with current chain generation.
				// If grant.Generation < currentGen, the chain was revoked after this grant
				// was issued — reject as replay of a pre-revocation grant.
				if err := delegation.ValidateChainRevocation(grant.Generation, currentGen); err != nil {
					logger.Warn().
						Err(err).
						Str("chain_id", grant.ChainID.String()).
						Int("grant_generation", grant.Generation).
						Int("current_generation", currentGen).
						Msg("Chain revocation detected")
					WriteError(w, r, http.StatusForbidden, delegation.ErrGrantRevokedOrConsumed)
					return
				}
			}
			// If GetChainGeneration fails, we skip this check (fail-open for availability)

			// 7. Validate HITL propagation from root
			rootGrant := GetRootGrantFromContext(r)
			if rootGrant != nil {
				if err := delegation.ValidateHITLPropagation(rootGrant, grant); err != nil {
					logger.Warn().
						Err(err).
						Str("grant_id", grant.GrantID.String()).
						Msg("HITL propagation validation failed")
					WriteError(w, r, http.StatusForbidden, err)
					return
				}
			}

			// 8. Validate fan-out limit (if this grant has children)
			if grant.HasParent() {
				activeChildren, err := cfg.Repository.CountActiveChildren(r.Context(), tenantID, grant.GrantID)
				if err == nil {
					if err := delegation.ValidateFanOut(activeChildren, cfg.MaxFanOut); err != nil {
						logger.Warn().
							Err(err).
							Int("active_children", activeChildren).
							Msg("Fan-out limit exceeded")
						WriteError(w, r, http.StatusForbidden, delegation.ErrFanOutLimitExceeded)
						return
					}
				}
				// If CountActiveChildren fails, we skip this check (fail-open for availability)
			}

			// 9. Decrement budget via Redis atomic Lua
			if grant.BudgetRemaining > 0 {
				remaining, err := cfg.BudgetDec.DecrementBudget(r.Context(), grant.ChainID, 1)
				if err != nil {
					logger.Error().Err(err).Msg("Budget decrement failed")
					if !cfg.FailOpen {
						WriteError(w, r, http.StatusForbidden, delegation.ErrBudgetExhausted)
						return
					}
				} else if remaining < 0 {
					WriteError(w, r, http.StatusForbidden, delegation.ErrBudgetExhausted)
					return
				}
			}

			// All validations passed — inject grant into context and continue
			ctx := context.WithValue(r.Context(), GrantKey{}, grant)
			if rootGrant != nil {
				ctx = context.WithValue(ctx, RootGrantKey{}, rootGrant)
			}
			ctx = context.WithValue(ctx, AncestorsKey{}, append(ancestors, delegateID(r)))

			// Set tenant GUC for RLS (delegation requests must be tenant-bound)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetGrantFromContext extracts the delegation grant from the request context.
func GetGrantFromContext(r *http.Request) *delegation.Grant {
	if grant, ok := r.Context().Value(GrantKey{}).(*delegation.Grant); ok {
		return grant
	}
	return nil
}

// GetRootGrantFromContext extracts the root grant from the request context.
func GetRootGrantFromContext(r *http.Request) *delegation.Grant {
	if grant, ok := r.Context().Value(RootGrantKey{}).(*delegation.Grant); ok {
		return grant
	}
	return nil
}

// GetAncestorsFromContext extracts the ancestor chain from the request context.
func GetAncestorsFromContext(r *http.Request) []domain.UUID {
	if ancestors, ok := r.Context().Value(AncestorsKey{}).([]domain.UUID); ok {
		return ancestors
	}
	return nil
}

// GetParentScopeFromContext extracts the parent scope from the request context.
// Returns nil if not present (non-delegated request).
func GetParentScopeFromContext(r *http.Request) delegation.ScopeSet {
	if scopes, ok := r.Context().Value(ScopesKey).([]string); ok {
		return delegation.NewScopeSet(scopes)
	}
	return nil
}

// delegateID extracts the delegate identity from the request.
// For delegation requests, this is the user ID from the auth context.
func delegateID(r *http.Request) domain.UUID {
	uid, _ := GetUserID(r)
	return uid
}

// SetGrantInContext injects a delegation grant into the request context.
// Use this in the auth handler when a delegation grant is presented.
func SetGrantInContext(ctx context.Context, grant *delegation.Grant) context.Context {
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	if grant != nil && !grant.HasParent() {
		// This is a root grant — set it as both grant and root
		ctx = context.WithValue(ctx, RootGrantKey{}, grant)
	}
	return ctx
}

// SetAncestorsInContext injects the ancestor chain into the request context.
func SetAncestorsInContext(ctx context.Context, ancestors []domain.UUID) context.Context {
	return context.WithValue(ctx, AncestorsKey{}, ancestors)
}
