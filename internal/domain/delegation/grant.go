package delegation

import (
	"sort"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// ScopeSet is a sorted, deduplicated set of scope strings (e.g., "read:data", "write:data").
type ScopeSet []string

// NewScopeSet creates a new ScopeSet from a slice of scope strings.
// The result is sorted lexicographically and deduplicated.
func NewScopeSet(scopes []string) ScopeSet {
	if len(scopes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(scopes))
	unique := make(ScopeSet, 0, len(scopes))
	for _, s := range scopes {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			unique = append(unique, s)
		}
	}
	sort.Strings(unique)
	return unique
}

// Intersection computes the set intersection of two ScopeSets.
// Returns ErrScopeIntersectionEmpty if the result is empty.
func (a ScopeSet) Intersection(b ScopeSet) (ScopeSet, error) {
	if len(a) == 0 || len(b) == 0 {
		return nil, ErrScopeIntersectionEmpty
	}
	setB := make(map[string]struct{}, len(b))
	for _, s := range b {
		setB[s] = struct{}{}
	}
	var result ScopeSet
	for _, s := range a {
		if _, ok := setB[s]; ok {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return nil, ErrScopeIntersectionEmpty
	}
	sort.Strings(result)
	return result, nil
}

// Grant represents a delegation grant envelope.
// It carries the full lifecycle context for a single delegation hop,
// including scope, HITL classification, budget, and chain metadata.
type Grant struct {
	GrantID           domain.UUID  `json:"grant_id"`
	ParentGrantID     *domain.UUID `json:"parent_grant_id,omitempty"`
	ChainID           domain.UUID  `json:"chain_id"`
	TenantID          domain.UUID  `json:"tenant_id"`
	DelegateIdentity  string       `json:"delegate_identity"`
	GrantedScope      ScopeSet     `json:"granted_scope"`
	RootIntent        string       `json:"root_intent"`
	HITLClassification string     `json:"hitl_classification"`
	Depth             int          `json:"depth"`
	ExpiresAt         time.Time    `json:"expires_at"`
	BudgetRemaining   int          `json:"budget_remaining"`
	Status            GrantStatus  `json:"status"`
}

// HasParent returns true if this grant was delegated from a parent grant.
func (g *Grant) HasParent() bool {
	return g.ParentGrantID != nil
}

// IsExpired returns true if the grant's TTL has elapsed.
func (g *Grant) IsExpired() bool {
	return time.Now().After(g.ExpiresAt)
}

// EffectiveScope returns the granted scope (intersection is computed at delegation time by the gateway).
func (g *Grant) EffectiveScope() ScopeSet {
	return g.GrantedScope
}
