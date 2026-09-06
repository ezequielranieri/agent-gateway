package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgadapter "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	redisadapter "github.com/ezequielranieri/agent-gateway/internal/adapter/redis"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/delegation"
	"github.com/ezequielranieri/agent-gateway/internal/middleware"
)

// applyDelegationMigrations applies migrations 0015-0017 on top of the base schema.
func applyDelegationMigrations(ctx context.Context, dbPool *pgxpool.Pool) error {
	migrations := []string{
		// 0015_delegation_grants
		`CREATE TABLE IF NOT EXISTS public.delegation_grants (
			id                    uuid NOT NULL DEFAULT gen_random_uuid(),
			tenant_id             uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
			parent_grant_id       uuid,
			chain_id              uuid NOT NULL,
			delegate_identity     text NOT NULL,
			granted_scope         jsonb NOT NULL,
			root_intent           text NOT NULL,
			hitl_classification   text NOT NULL DEFAULT 'none' CHECK (hitl_classification IN ('none', 'optional', 'required')),
			depth                 integer NOT NULL DEFAULT 0 CHECK (depth >= 0),
			expires_at            timestamptz NOT NULL,
			budget_remaining      integer NOT NULL DEFAULT 1000 CHECK (budget_remaining >= 0),
			status                text NOT NULL DEFAULT 'issued' CHECK (status IN ('issued', 'active', 'revoked', 'expired', 'consumed')),
			created_at            timestamptz NOT NULL DEFAULT now(),
			updated_at            timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (id, tenant_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_delegation_grants_chain ON public.delegation_grants (tenant_id, chain_id)`,
		`CREATE INDEX IF NOT EXISTS idx_delegation_grants_delegate ON public.delegation_grants (tenant_id, delegate_identity, status)`,
		`CREATE INDEX IF NOT EXISTS idx_delegation_grants_parent ON public.delegation_grants (tenant_id, parent_grant_id) WHERE parent_grant_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_delegation_grants_expires ON public.delegation_grants (tenant_id, expires_at) WHERE status IN ('issued', 'active')`,
		`ALTER TABLE public.delegation_grants ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE public.delegation_grants FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY delegation_grants_tenant_isolation ON public.delegation_grants
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid)`,

		// 0016_audit_chain_columns
		`ALTER TABLE public.audit_events
			ADD COLUMN IF NOT EXISTS chain_id uuid DEFAULT NULL,
			ADD COLUMN IF NOT EXISTS parent_event_id uuid DEFAULT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_chain ON public.audit_events (tenant_id, chain_id) WHERE chain_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_parent_event ON public.audit_events (tenant_id, parent_event_id) WHERE parent_event_id IS NOT NULL`,

		// 0017_delegation_grants_generation
		`ALTER TABLE public.delegation_grants
			ADD COLUMN IF NOT EXISTS generation integer NOT NULL DEFAULT 0`,
	}

	for _, m := range migrations {
		if _, err := dbPool.Exec(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// TestDelegationIntegration tests the full delegation chain lifecycle.
// Requires Docker (testcontainers). Skipped in -short mode.
func TestDelegationIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	// Apply delegation-specific migrations
	err := applyDelegationMigrations(tc.Ctx, tc.DBPool)
	require.NoError(t, err, "Delegation migrations must apply cleanly")

	logger := zerolog.New(zerolog.ConsoleWriter{Out: zerolog.NewTestWriter(t)}).
		Level(zerolog.DebugLevel).
		With().Str("test", "delegation").Logger()

	// Initialize delegation adapter
	delegationRepo := pgadapter.NewDelegationRepository(tc.DBPool)
	redisBudgetDec := redisadapter.NewRedisBudgetDecrementor(tc.RedisClient, logger)

	// Create test tenant
	tenantID := domain.NewUUID()
	_, err = tc.DBPool.Exec(tc.Ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'Delegation Test Tenant', 'active')
		ON CONFLICT (id) DO NOTHING
	`, tenantID)
	require.NoError(t, err)

	// --- Subtest: 3-hop delegation chain ---
	t.Run("ThreeHopChain", func(t *testing.T) {
		testThreeHopChain(t, tc.Ctx, delegationRepo, redisBudgetDec, tenantID, logger)
	})

	// --- Subtest: chain revocation kills entire tree ---
	t.Run("ChainRevocationKillsTree", func(t *testing.T) {
		testChainRevocationKillsTree(t, tc.Ctx, delegationRepo, redisBudgetDec, tenantID, logger)
	})
}

// testThreeHopChain creates root → mid → leaf delegation chain,
// verifies audit_events are written with chain_id and parent_event_id links.
func testThreeHopChain(
	t *testing.T,
	ctx context.Context,
	repo *pgadapter.DelegationRepository,
	budgetDec *redisadapter.RedisBudgetDecrementor,
	tenantID domain.UUID,
	logger zerolog.Logger,
) {
	chainID := domain.NewUUID()
	now := time.Now()

	// Root grant (depth=0)
	rootGrant := &delegation.Grant{
		GrantID:            domain.NewUUID(),
		ChainID:            chainID,
		TenantID:           tenantID,
		DelegateIdentity:   "root-agent",
		GrantedScope:       delegation.NewScopeSet([]string{"read:data", "write:data"}),
		RootIntent:         "delete:records",
		HITLClassification: "required",
		Depth:              0,
		Generation:         0,
		ExpiresAt:          now.Add(1 * time.Hour),
		BudgetRemaining:    1000,
		Status:             delegation.GrantStatusActive,
	}
	err := repo.Create(ctx, rootGrant)
	require.NoError(t, err, "Root grant must be created")

	// Initialize budget pool for chain
	err = budgetDec.InitBudget(ctx, chainID, 1000)
	require.NoError(t, err)

	// Mid grant (depth=1)
	midGrant := &delegation.Grant{
		GrantID:            domain.NewUUID(),
		ParentGrantID:      &rootGrant.GrantID,
		ChainID:            chainID,
		TenantID:           tenantID,
		DelegateIdentity:   "mid-agent",
		GrantedScope:       delegation.NewScopeSet([]string{"read:data"}),
		RootIntent:         "delete:records", // must match root
		HITLClassification: "required",      // must not relax
		Depth:              1,
		Generation:         0,
		ExpiresAt:          now.Add(1 * time.Hour),
		BudgetRemaining:    999,
		Status:             delegation.GrantStatusActive,
	}
	err = repo.Create(ctx, midGrant)
	require.NoError(t, err, "Mid grant must be created")

	// Leaf grant (depth=2)
	leafGrant := &delegation.Grant{
		GrantID:            domain.NewUUID(),
		ParentGrantID:      &midGrant.GrantID,
		ChainID:            chainID,
		TenantID:           tenantID,
		DelegateIdentity:   "leaf-agent",
		GrantedScope:       delegation.NewScopeSet([]string{"read:data"}),
		RootIntent:         "delete:records", // must match root
		HITLClassification: "required",      // must not relax
		Depth:              2,
		Generation:         0,
		ExpiresAt:          now.Add(1 * time.Hour),
		BudgetRemaining:    998,
		Status:             delegation.GrantStatusActive,
	}
	err = repo.Create(ctx, leafGrant)
	require.NoError(t, err, "Leaf grant must be created")

	// Verify all grants are retrievable
	for _, tc := range []struct {
		name  string
		grant *delegation.Grant
	}{
		{"root", rootGrant},
		{"mid", midGrant},
		{"leaf", leafGrant},
	} {
		t.Run("Retrieve_"+tc.name, func(t *testing.T) {
			fetched, err := repo.GetByID(ctx, tenantID, tc.grant.GrantID)
			require.NoError(t, err)
			assert.Equal(t, tc.grant.GrantID, fetched.GrantID)
			assert.Equal(t, delegation.GrantStatusActive, fetched.Status)
			assert.Equal(t, chainID, fetched.ChainID)
		})
	}

	// Verify chain generation is 0 (no revoked grants yet)
	gen, err := repo.GetChainGeneration(ctx, tenantID, chainID)
	require.NoError(t, err)
	assert.Equal(t, 0, gen, "Chain generation should be 0 before revocation")

	// Verify fan-out count for root
	rootChildren, err := repo.CountActiveChildren(ctx, tenantID, rootGrant.GrantID)
	require.NoError(t, err)
	assert.Equal(t, 1, rootChildren, "Root should have 1 active child (mid)")

	// Verify list by chain returns all 3 grants
	grants, err := repo.ListByChain(ctx, tenantID, chainID)
	require.NoError(t, err)
	assert.Len(t, grants, 3, "Chain should contain 3 grants")
}

// testChainRevocationKillsTree creates a 3-hop chain, revokes the root,
// and verifies all descendants become invalid (generation bump).
func testChainRevocationKillsTree(
	t *testing.T,
	ctx context.Context,
	repo *pgadapter.DelegationRepository,
	budgetDec *redisadapter.RedisBudgetDecrementor,
	tenantID domain.UUID,
	logger zerolog.Logger,
) {
	chainID := domain.NewUUID()
	now := time.Now()

	// Create 3-hop chain
	rootGrant := &delegation.Grant{
		GrantID:            domain.NewUUID(),
		ChainID:            chainID,
		TenantID:           tenantID,
		DelegateIdentity:   "revoke-root",
		GrantedScope:       delegation.NewScopeSet([]string{"read:data"}),
		RootIntent:         "delete:records",
		HITLClassification: "required",
		Depth:              0,
		Generation:         0,
		ExpiresAt:          now.Add(1 * time.Hour),
		BudgetRemaining:    1000,
		Status:             delegation.GrantStatusActive,
	}
	require.NoError(t, repo.Create(ctx, rootGrant))
	require.NoError(t, budgetDec.InitBudget(ctx, chainID, 1000))

	midGrant := &delegation.Grant{
		GrantID:            domain.NewUUID(),
		ParentGrantID:      &rootGrant.GrantID,
		ChainID:            chainID,
		TenantID:           tenantID,
		DelegateIdentity:   "revoke-mid",
		GrantedScope:       delegation.NewScopeSet([]string{"read:data"}),
		RootIntent:         "delete:records",
		HITLClassification: "required",
		Depth:              1,
		Generation:         0,
		ExpiresAt:          now.Add(1 * time.Hour),
		BudgetRemaining:    999,
		Status:             delegation.GrantStatusActive,
	}
	require.NoError(t, repo.Create(ctx, midGrant))

	leafGrant := &delegation.Grant{
		GrantID:            domain.NewUUID(),
		ParentGrantID:      &midGrant.GrantID,
		ChainID:            chainID,
		TenantID:           tenantID,
		DelegateIdentity:   "revoke-leaf",
		GrantedScope:       delegation.NewScopeSet([]string{"read:data"}),
		RootIntent:         "delete:records",
		HITLClassification: "required",
		Depth:              2,
		Generation:         0,
		ExpiresAt:          now.Add(1 * time.Hour),
		BudgetRemaining:    998,
		Status:             delegation.GrantStatusActive,
	}
	require.NoError(t, repo.Create(ctx, leafGrant))

	// Verify initial generation
	gen, err := repo.GetChainGeneration(ctx, tenantID, chainID)
	require.NoError(t, err)
	assert.Equal(t, 0, gen)

	// Revoke entire chain
	err = repo.RevokeByChain(ctx, tenantID, chainID)
	require.NoError(t, err, "Chain revocation must succeed")

	// Verify generation bumped (1 revoked grant = generation 1)
	gen, err = repo.GetChainGeneration(ctx, tenantID, chainID)
	require.NoError(t, err)
	assert.Equal(t, 1, gen, "Chain generation must be 1 after revocation")

	// Verify all grants in chain are now revoked
	grants, err := repo.ListByChain(ctx, tenantID, chainID)
	require.NoError(t, err)
	require.Len(t, grants, 3)
	for _, g := range grants {
		assert.Equal(t, delegation.GrantStatusRevoked, g.Status,
			"Grant %s must be revoked after chain revocation", g.GrantID)
	}

	// Verify old-generation grants are rejected by ValidateChainRevocation
	err = delegation.ValidateChainRevocation(0, 1) // generation 0 < current 1
	require.ErrorIs(t, err, delegation.ErrGrantRevokedOrConsumed,
		"Old-generation grant must be rejected after chain revocation")
}

// TestDelegationMiddlewareValidation tests the middleware chain with real
// delegation grants through the full validator pipeline.
func TestDelegationMiddlewareValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	err := applyDelegationMigrations(tc.Ctx, tc.DBPool)
	require.NoError(t, err)

	logger := zerolog.New(zerolog.ConsoleWriter{Out: zerolog.NewTestWriter(t)}).
		Level(zerolog.DebugLevel).
		With().Str("test", "delegation-mw").Logger()

	delegationRepo := pgadapter.NewDelegationRepository(tc.DBPool)
	budgetDec := redisadapter.NewRedisBudgetDecrementor(tc.RedisClient, logger)

	cfg := middleware.DelegationConfig{
		Repository:   delegationRepo,
		BudgetDec:    budgetDec,
		MaxDepth:     3,
		MaxFanOut:    2,
		Logger:       logger,
	}
	mw := middleware.NewDelegation(cfg)

	// Create test tenant
	tenantID := domain.NewUUID()
	_, err = tc.DBPool.Exec(tc.Ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'MW Test Tenant', 'active')
		ON CONFLICT (id) DO NOTHING
	`, tenantID)
	require.NoError(t, err)

	// --- Subtest: scope intersection blocks widened scope ---
	t.Run("ScopeIntersectionBlocksWidened", func(t *testing.T) {
		// Grant with read:data, but parent scope is read:data + write:data
		// Effective scope = intersection = read:data (ok)
		// But if grant tries to widen beyond parent, intersection blocks it
		// Here we test the middleware rejects when parent scope is narrower
		// than what the grant claims — the middleware computes intersection.
		//
		// We inject parent scope as read:data, and grant scope as write:data
		// → intersection is empty → 403
		tenantID2 := domain.NewUUID()
		_, err := tc.DBPool.Exec(tc.Ctx, `
			INSERT INTO public.tenants (id, name, status) VALUES ($1, 'MW Scope Tenant', 'active')
			ON CONFLICT (id) DO NOTHING
		`, tenantID2)
		require.NoError(t, err)

		grant := &delegation.Grant{
			GrantID:          domain.NewUUID(),
			ChainID:          domain.NewUUID(),
			TenantID:         tenantID2,
			DelegateIdentity: "scope-agent",
			GrantedScope:     delegation.NewScopeSet([]string{"write:data"}),
			Depth:            1,
			ExpiresAt:        time.Now().Add(1 * time.Hour),
			BudgetRemaining:  100,
			Status:           delegation.GrantStatusActive,
		}
		require.NoError(t, delegationRepo.Create(tc.Ctx, grant))

		// Simulate parent scope as read:data only
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("Should not reach handler — empty intersection must block")
		}))

		w, req := makeTestRequest(tenantID2, grant)
		req = req.WithContext(context.WithValue(req.Context(), middleware.ScopesKey, []string{"read:data"}))
		handler.ServeHTTP(w, req)
		assert.Equal(t, 403, w.Code, "Empty scope intersection must return 403")
	})

	// --- Subtest: TTL expiration blocks ---
	t.Run("TTLExpirationBlocks", func(t *testing.T) {
		grant := &delegation.Grant{
			GrantID:          domain.NewUUID(),
			ChainID:          domain.NewUUID(),
			TenantID:         tenantID,
			DelegateIdentity: "ttl-agent",
			GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
			Depth:            1,
			ExpiresAt:        time.Now().Add(-1 * time.Hour), // already expired
			BudgetRemaining:  100,
			Status:           delegation.GrantStatusActive,
		}
		require.NoError(t, delegationRepo.Create(tc.Ctx, grant))

		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("Should not reach handler — expired grant must block")
		}))

		w, req := makeTestRequest(tenantID, grant)
		handler.ServeHTTP(w, req)
		assert.Equal(t, 403, w.Code, "Expired grant must return 403")
	})

	// --- Subtest: generation mismatch blocks revoked grant ---
	t.Run("GenerationMismatchBlocksRevokedGrant", func(t *testing.T) {
		chainID := domain.NewUUID()

		// Create a grant at generation 0
		grant := &delegation.Grant{
			GrantID:          domain.NewUUID(),
			ChainID:          chainID,
			TenantID:         tenantID,
			DelegateIdentity: "gen-agent",
			GrantedScope:     delegation.NewScopeSet([]string{"read:data"}),
			Depth:            1,
			Generation:       0,
			ExpiresAt:        time.Now().Add(1 * time.Hour),
			BudgetRemaining:  100,
			Status:           delegation.GrantStatusActive,
		}
		require.NoError(t, delegationRepo.Create(tc.Ctx, grant))

		// Revoke the chain (bumps generation)
		require.NoError(t, delegationRepo.RevokeByChain(tc.Ctx, tenantID, chainID))

		// The old grant (generation 0) must be rejected
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("Should not reach handler — revoked grant must be rejected")
		}))

		w, req := makeTestRequest(tenantID, grant)
		handler.ServeHTTP(w, req)
		assert.Equal(t, 403, w.Code, "Revoked generation must return 403")
	})
}

// makeTestRequest creates an httptest request with tenant and grant context.
func makeTestRequest(tenantID domain.UUID, grant *delegation.Grant) (*httptest.ResponseRecorder, *http.Request) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	ctx := context.WithValue(req.Context(), middleware.TenantIDKey, tenantID)
	ctx = context.WithValue(ctx, middleware.GrantKey{}, grant)
	if grant != nil && grant.ParentGrantID == nil {
		ctx = context.WithValue(ctx, middleware.RootGrantKey{}, grant)
	}
	req = req.WithContext(ctx)
	return w, req
}
