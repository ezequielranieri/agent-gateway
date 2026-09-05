package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/delegation"
)

// mockDelegationRepo implements DelegationRepository for testing.
type mockDelegationRepo struct {
	countActiveChildren int
	chainGeneration     int
}

func (m *mockDelegationRepo) GetByID(_ context.Context, _ domain.UUID, _ domain.UUID) (*delegation.Grant, error) {
	return nil, nil
}

func (m *mockDelegationRepo) CountActiveChildren(_ context.Context, _ domain.UUID, _ domain.UUID) (int, error) {
	return m.countActiveChildren, nil
}

func (m *mockDelegationRepo) GetChainGeneration(_ context.Context, _ domain.UUID, _ domain.UUID) (int, error) {
	return m.chainGeneration, nil
}

// mockBudgetDec implements DelegationBudgetDecrementor for testing.
type mockBudgetDec struct {
	remaining int
	err       error
}

func (m *mockBudgetDec) DecrementBudget(_ context.Context, _ domain.UUID, _ int) (int, error) {
	return m.remaining, m.err
}

// trackingBudgetDec implements DelegationBudgetDecrementor and tracks calls.
type trackingBudgetDec struct {
	remaining int
	callCount *int
}

func (m *trackingBudgetDec) DecrementBudget(_ context.Context, _ domain.UUID, _ int) (int, error) {
	*m.callCount++
	return m.remaining, nil
}

func logger() zerolog.Logger {
	return zerolog.Nop()
}

// testDelegationConfig returns a valid DelegationConfig for tests.
// All required production dependencies are provided.
func testDelegationConfig() DelegationConfig {
	return DelegationConfig{
		Logger:     logger(),
		MaxDepth:   5,
		MaxFanOut:  10,
		BudgetDec:  &mockBudgetDec{remaining: 100},
		Repository: &mockDelegationRepo{chainGeneration: 0},
	}
}

func TestDelegationMW_NoGrantPassesThrough(t *testing.T) {
	cfg := testDelegationConfig()
	mw := NewDelegation(cfg)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	// Add tenant ID to context
	ctx := context.WithValue(req.Context(), TenantIDKey, domain.NewUUID())
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestDelegationMW_NoTenantPassesThrough(t *testing.T) {
	cfg := testDelegationConfig()
	mw := NewDelegation(cfg)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	// No tenant ID in context

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestDelegationMW_ValidGrantPassesThrough(t *testing.T) {
	cfg := testDelegationConfig()
	mw := NewDelegation(cfg)

	tenantID := domain.NewUUID()
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify grant is in context
		g := GetGrantFromContext(r)
		require.NotNil(t, g)
		assert.Equal(t, grant.GrantID, g.GrantID)
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestDelegationMW_CrossTenantBlocked(t *testing.T) {
	cfg := testDelegationConfig()
	mw := NewDelegation(cfg)

	tenantA := domain.NewUUID()
	tenantB := domain.NewUUID()
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         tenantB, // grant belongs to tenant B
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantA) // request is tenant A
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach handler")
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestDelegationMW_ExpiredGrantBlocked(t *testing.T) {
	cfg := testDelegationConfig()
	mw := NewDelegation(cfg)

	tenantID := domain.NewUUID()
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		ExpiresAt:        time.Now().Add(-1 * time.Hour), // expired
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach handler")
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestDelegationMW_DepthLimitExceeded(t *testing.T) {
	cfg := testDelegationConfig()
	cfg.MaxDepth = 3
	mw := NewDelegation(cfg)

	tenantID := domain.NewUUID()
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            3, // at max
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach handler")
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestDelegationMW_FanOutLimitExceeded(t *testing.T) {
	cfg := testDelegationConfig()
	cfg.MaxFanOut = 2
	cfg.Repository = &mockDelegationRepo{countActiveChildren: 2}
	mw := NewDelegation(cfg)

	tenantID := domain.NewUUID()
	parentID := domain.NewUUID()
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ParentGrantID:    &parentID,
		ChainID:          domain.NewUUID(),
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach handler")
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestDelegationMW_BudgetExhausted(t *testing.T) {
	cfg := testDelegationConfig()
	cfg.BudgetDec = &mockBudgetDec{remaining: -1}
	mw := NewDelegation(cfg)

	tenantID := domain.NewUUID()
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  1,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach handler")
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestDelegationMW_CycleDetected(t *testing.T) {
	cfg := testDelegationConfig()
	mw := NewDelegation(cfg)

	tenantID := domain.NewUUID()
	childID := domain.NewUUID()
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            2,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	// Ancestors include the child ID itself — creates a cycle
	ctx = context.WithValue(ctx, AncestorsKey{}, []domain.UUID{childID})
	// Set user ID to match the ancestor (cycle)
	ctx = context.WithValue(ctx, UserIDKey, childID)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach handler")
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestDelegationMW_RevokedGrantBlocked(t *testing.T) {
	cfg := testDelegationConfig()
	mw := NewDelegation(cfg)

	tenantID := domain.NewUUID()
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusRevoked, // revoked
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach handler")
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestDelegationMW_HITLMismatchBlocked(t *testing.T) {
	cfg := testDelegationConfig()
	mw := NewDelegation(cfg)

	tenantID := domain.NewUUID()
	rootGrant := &delegation.Grant{
		GrantID:             domain.NewUUID(),
		ChainID:             domain.NewUUID(),
		TenantID:            tenantID,
		RootIntent:          "delete:records",
		HITLClassification:  "required",
		Depth:               0,
		ExpiresAt:           time.Now().Add(1 * time.Hour),
		BudgetRemaining:     100,
		Status:              delegation.GrantStatusActive,
	}
	childGrant := &delegation.Grant{
		GrantID:             domain.NewUUID(),
		ParentGrantID:       &rootGrant.GrantID,
		ChainID:             rootGrant.ChainID,
		TenantID:            tenantID,
		DelegateIdentity:    "agent-1",
		GrantedScope:        delegation.NewScopeSet([]string{"read:records"}),
		RootIntent:          "read:records", // re-packaged
		HITLClassification:  "required",
		Depth:               1,
		ExpiresAt:           time.Now().Add(1 * time.Hour),
		BudgetRemaining:     100,
		Status:              delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, childGrant)
	ctx = context.WithValue(ctx, RootGrantKey{}, rootGrant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach handler")
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// --- Construction guard tests (Defect 1: fail-fast) ---

func TestNewDelegation_NilBudgetDecPanics(t *testing.T) {
	// Construction with nil BudgetDec must panic — silent fail-open is unacceptable.
	// A production middleware without budget enforcement is a security hole.
	require.Panics(t, func() {
		NewDelegation(DelegationConfig{
			Logger:   logger(),
			MaxDepth: 5,
			MaxFanOut: 10,
			BudgetDec: nil, // MUST NOT be nil in production
		})
	})
}

func TestNewDelegation_NilRepositoryPanics(t *testing.T) {
	// Construction with nil Repository must panic — chain generation and fan-out
	// checks require a repository. Silent skip is a security hole.
	require.Panics(t, func() {
		NewDelegation(DelegationConfig{
			Logger:     logger(),
			MaxDepth:   5,
			MaxFanOut:  10,
			BudgetDec:  &mockBudgetDec{remaining: 10},
			Repository: nil, // MUST NOT be nil in production
		})
	})
}

func TestNewDelegation_ValidConfigSucceeds(t *testing.T) {
	// Valid config with all required dependencies should not panic.
	require.NotPanics(t, func() {
		NewDelegation(DelegationConfig{
			Logger:     logger(),
			MaxDepth:   5,
			MaxFanOut:  10,
			BudgetDec:  &mockBudgetDec{remaining: 10},
			Repository: &mockDelegationRepo{chainGeneration: 0},
		})
	})
}

// --- Generation-based revocation tests (Defect 2) ---

func TestDelegationMW_OldGenerationAfterRevocationRejected(t *testing.T) {
	// Grant issued at generation 1, chain revoked → generation bumped to 2.
	// The OLD grant presented after revocation MUST be rejected.
	// This is the mutation-evasion test: replay of a pre-revocation grant.
	chainID := domain.NewUUID()
	tenantID := domain.NewUUID()

	cfg := DelegationConfig{
		Logger:     logger(),
		MaxDepth:   5,
		MaxFanOut:  10,
		BudgetDec:  &mockBudgetDec{remaining: 10},
		Repository: &mockDelegationRepo{chainGeneration: 2}, // chain revoked to gen 2
	}
	mw := NewDelegation(cfg)

	// Grant issued at generation 1 (before revocation)
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          chainID,
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		Generation:       1, // issued at gen 1
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Should not reach handler — old generation grant must be rejected")
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestDelegationMW_CurrentGenerationAccepted(t *testing.T) {
	// Grant issued at generation 2, chain generation is 2 → should PASS.
	chainID := domain.NewUUID()
	tenantID := domain.NewUUID()

	cfg := DelegationConfig{
		Logger:     logger(),
		MaxDepth:   5,
		MaxFanOut:  10,
		BudgetDec:  &mockBudgetDec{remaining: 10},
		Repository: &mockDelegationRepo{chainGeneration: 2},
	}
	mw := NewDelegation(cfg)

	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          chainID,
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		Generation:       2, // matches current chain generation
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestDelegationMW_BudgetDecrementorCalled(t *testing.T) {
	// Verify that the budget decrementor is actually called (not silently skipped).
	tenantID := domain.NewUUID()
	callCount := 0

TrackingDec := &trackingBudgetDec{remaining: 10, callCount: &callCount}
	cfg := DelegationConfig{
		Logger:     logger(),
		MaxDepth:   5,
		MaxFanOut:  10,
		BudgetDec:  TrackingDec,
		Repository: &mockDelegationRepo{chainGeneration: 0},
	}
	mw := NewDelegation(cfg)

	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		Generation:       0,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, 1, callCount, "BudgetDec.DecrementBudget must be called exactly once")
}

func TestDelegationMW_ChiIntegration(t *testing.T) {
	cfg := testDelegationConfig()

	r := chi.NewRouter()
	r.Use(NewDelegation(cfg))

	tenantID := domain.NewUUID()
	grant := &delegation.Grant{
		GrantID:          domain.NewUUID(),
		ChainID:          domain.NewUUID(),
		TenantID:         tenantID,
		DelegateIdentity: "agent-1",
		GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
		Depth:            1,
		ExpiresAt:        time.Now().Add(1 * time.Hour),
		BudgetRemaining:  100,
		Status:           delegation.GrantStatusActive,
	}

	r.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		g := GetGrantFromContext(r)
		require.NotNil(t, g)
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := context.WithValue(req.Context(), TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, GrantKey{}, grant)
	req = req.WithContext(ctx)

	r.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}
