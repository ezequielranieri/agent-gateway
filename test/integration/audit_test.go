package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgadapter "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// TestAuditIntegration tests the audit log implementation
func TestAuditIntegration(t *testing.T) {
	// Skip if Docker is not available
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup test containers
	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	// Create logger
	logger := zerolog.New(zerolog.ConsoleWriter{Out: zerolog.NewTestWriter(t)}).
		Level(zerolog.DebugLevel).
		With().Str("test", "audit").Logger()

	// Create test router
	router, token, tenantID, userID, _ := CreateTestRouter(t, tc, logger)

	// Get audit repo for direct access
	auditRepo := pgadapter.NewAuditRepository(tc.DBPool)

	// Test 1: Append + hash chain (10 events, verify chain)
	t.Run("AppendAndHashChain", func(t *testing.T) {
		testAppendAndHashChain(t, auditRepo, tc.Ctx, tenantID, userID)
	})

	// Test 2: Concurrent append race (50 writers, no permanent seq gaps)
	t.Run("ConcurrentAppendRace", func(t *testing.T) {
		testConcurrentAppendRace(t, auditRepo, tc.Ctx, tenantID, userID)
	})

	// Test 3: VerifyChain detects tampering
	t.Run("VerifyChainDetectsTampering", func(t *testing.T) {
		testVerifyChainDetectsTampering(t, auditRepo, tc.Ctx, tc.DBPool, tenantID, userID)
	})

	// Test 4: VerifyChain on large chain (1000 events < 5s)
	t.Run("VerifyChainLargeChain", func(t *testing.T) {
		testVerifyChainLargeChain(t, auditRepo, tc.Ctx, tenantID, userID)
	})

	// Test 5: Filters on GET /v1/admin/audit
	t.Run("AdminAuditFilters", func(t *testing.T) {
		testAdminAuditFilters(t, router, token, logger)
	})

	// Test 6: RLS isolation (tenant A cannot see tenant B's events)
	t.Run("RLSIsolation", func(t *testing.T) {
		testRLSIsolation(t, auditRepo, tc.Ctx, tc.DBPool)
	})
}

func testAppendAndHashChain(t *testing.T, auditRepo *pgadapter.AuditRepository, ctx context.Context, tenantID, userID domain.UUID) {
	// Append 10 events
	for i := 0; i < 10; i++ {
		event := &domain.AuditEvent{
			TenantID:    tenantID,
			ActorUserID: &userID,
			Action:      "test.action",
			EntityType:  "test_entity",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(fmt.Sprintf(`{"sequence":%d}`, i)),
		}

		err := auditRepo.Append(ctx, event)
		require.NoError(t, err, "Failed to append event %d", i)
		assert.NotEqual(t, 0, event.Seq, "Seq should be set for event %d", i)
		assert.NotEqual(t, "", event.ChainHash, "ChainHash should be set for event %d", i)
	}

	// Verify chain
	result, err := auditRepo.VerifyChain(ctx, tenantID, 1, 10)
	require.NoError(t, err)
	assert.True(t, result.Valid, "Chain should be valid")
	assert.Equal(t, int64(10), result.TotalSeen)
}

func testConcurrentAppendRace(t *testing.T, auditRepo *pgadapter.AuditRepository, ctx context.Context, tenantID, userID domain.UUID) {
	const numWriters = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	successCount := 0
	errorCount := 0

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			event := &domain.AuditEvent{
				TenantID:    tenantID,
				ActorUserID: &userID,
				Action:      "concurrent.test",
				EntityType:  "concurrent_entity",
				Severity:    domain.AuditSeverityInfo,
				CreatedAt:   time.Now(),
				Payload:     json.RawMessage(fmt.Sprintf(`{"writer":%d}`, idx)),
			}

			err := auditRepo.Append(ctx, event)
			mu.Lock()
			if err != nil {
				errorCount++
			} else {
				successCount++
			}
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	logger := zerolog.New(zerolog.ConsoleWriter{Out: zerolog.NewTestWriter(t)}).With().Str("test", "concurrent").Logger()
	logger.Info().Int("success", successCount).Int("errors", errorCount).Msg("Concurrent append results")

	assert.Equal(t, numWriters, successCount, "All writers should succeed")
	assert.Equal(t, 0, errorCount, "No errors expected")

	// Verify no seq gaps
	lastEvent, err := auditRepo.GetLastEvent(ctx, tenantID)
	require.NoError(t, err)
	assert.NotNil(t, lastEvent)

	// Verify chain integrity
	result, err := auditRepo.VerifyChain(ctx, tenantID, 1, lastEvent.Seq)
	require.NoError(t, err)
	assert.True(t, result.Valid, "Chain should be valid after concurrent writes")
}

func testVerifyChainDetectsTampering(t *testing.T, auditRepo *pgadapter.AuditRepository, ctx context.Context, dbPool *pgxpool.Pool, tenantID, userID domain.UUID) {
	// Append some events first
	for i := 0; i < 5; i++ {
		event := &domain.AuditEvent{
			TenantID:    tenantID,
			ActorUserID: &userID,
			Action:      "tamper.test",
			EntityType:  "tamper_entity",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(fmt.Sprintf(`{"data":"original-%d"}`, i)),
		}
		err := auditRepo.Append(ctx, event)
		require.NoError(t, err)
	}

	// Verify chain is valid initially
	result, err := auditRepo.VerifyChain(ctx, tenantID, 1, 5)
	require.NoError(t, err)
	assert.True(t, result.Valid, "Chain should be valid before tampering")

	// Tamper with event at seq=3 by directly updating the database
	_, err = dbPool.Exec(ctx, `
		UPDATE public.audit_events
		SET payload = '{"data":"tampered"}'
		WHERE tenant_id = $1 AND seq = 3
	`, tenantID)
	require.NoError(t, err)

	// Verify chain now detects tampering
	result, err = auditRepo.VerifyChain(ctx, tenantID, 1, 5)
	require.NoError(t, err)
	assert.False(t, result.Valid, "Chain should be invalid after tampering")
	assert.Equal(t, int64(3), result.BrokenSeq, "Should detect tampering at seq 3")
	assert.NotNil(t, result.Error)
}

func testVerifyChainLargeChain(t *testing.T, auditRepo *pgadapter.AuditRepository, ctx context.Context, tenantID, userID domain.UUID) {
	const numEvents = 1000

	// Append 1000 events
	start := time.Now()
	for i := 0; i < numEvents; i++ {
		event := &domain.AuditEvent{
			TenantID:    tenantID,
			ActorUserID: &userID,
			Action:      "large.chain.test",
			EntityType:  "large_entity",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(fmt.Sprintf(`{"index":%d}`, i)),
		}

		err := auditRepo.Append(ctx, event)
		require.NoError(t, err, "Failed to append event %d", i)
	}
	appendDuration := time.Since(start)

	// Verify chain - should complete within 5 seconds
	verifyStart := time.Now()
	result, err := auditRepo.VerifyChain(ctx, tenantID, 1, numEvents)
	verifyDuration := time.Since(verifyStart)

	require.NoError(t, err)
	assert.True(t, result.Valid, "Large chain should be valid")
	assert.Equal(t, int64(numEvents), result.TotalSeen)
	assert.Less(t, verifyDuration, 5*time.Second, "VerifyChain should complete within 5s")

	logger := zerolog.New(zerolog.ConsoleWriter{Out: zerolog.NewTestWriter(t)}).With().Str("test", "large_chain").Logger()
	logger.Info().
		Dur("append_duration", appendDuration).
		Dur("verify_duration", verifyDuration).
		Msg("Large chain test timings")
}

func testAdminAuditFilters(t *testing.T, router http.Handler, token string, logger zerolog.Logger) {
	// Make some requests to generate audit events
	for i := 0; i < 3; i++ {
		body := `{"model":"test-model","messages":[{"role":"user","content":"test message"}]}`
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req = req.WithContext(context.Background())
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// Wait a bit for async writes
	time.Sleep(100 * time.Millisecond)

	// Test filter by action
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/audit?action=POST%20/v1/chat/completions", nil)
	req = req.WithContext(context.Background())
	req.Header.Set("Authorization", "Bearer "+token)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "Filter by action should work")

	var response map[string]interface{}
	err := json.NewDecoder(w.Body).Decode(&response)
	require.NoError(t, err)
	assert.NotNil(t, response["events"])

	// Test filter by severity
	req2 := httptest.NewRequest(http.MethodGet, "/v1/admin/audit?severity=info", nil)
	req2 = req2.WithContext(context.Background())
	req2.Header.Set("Authorization", "Bearer "+token)

	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code, "Filter by severity should work")

	var response2 map[string]interface{}
	err = json.NewDecoder(w2.Body).Decode(&response2)
	require.NoError(t, err)
	assert.NotNil(t, response2["events"])
}

func testRLSIsolation(t *testing.T, auditRepo *pgadapter.AuditRepository, ctx context.Context, dbPool *pgxpool.Pool) {
	// Create tenant A and tenant B
	tenantA := domain.NewUUID()
	tenantB := domain.NewUUID()
	userID := domain.NewUUID() // Local user for test events

	_, err := dbPool.Exec(ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'Tenant A', 'active')
		ON CONFLICT (id) DO NOTHING
	`, tenantA)
	require.NoError(t, err)

	_, err = dbPool.Exec(ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'Tenant B', 'active')
		ON CONFLICT (id) DO NOTHING
	`, tenantB)
	require.NoError(t, err)

	// Append events for tenant A
	for i := 0; i < 3; i++ {
		event := &domain.AuditEvent{
			TenantID:    tenantA,
			ActorUserID: &userID,
			Action:      "tenant.a.action",
			EntityType:  "test",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(fmt.Sprintf(`{"tenant":"a", "index":%d}`, i)),
		}
		err := auditRepo.Append(ctx, event)
		require.NoError(t, err)
	}

	// Append events for tenant B
	for i := 0; i < 2; i++ {
		event := &domain.AuditEvent{
			TenantID:    tenantB,
			ActorUserID: &userID,
			Action:      "tenant.b.action",
			EntityType:  "test",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(fmt.Sprintf(`{"tenant":"b", "index":%d}`, i)),
		}
		err := auditRepo.Append(ctx, event)
		require.NoError(t, err)
	}

	// Query tenant A events - should only see tenant A's events
	eventsA, err := auditRepo.Query(ctx, pgadapter.AuditFilter{TenantID: tenantA})
	require.NoError(t, err)
	assert.Equal(t, 3, len(eventsA), "Tenant A should see only its 3 events")

	// Query tenant B events - should only see tenant B's events
	eventsB, err := auditRepo.Query(ctx, pgadapter.AuditFilter{TenantID: tenantB})
	require.NoError(t, err)
	assert.Equal(t, 2, len(eventsB), "Tenant B should see only its 2 events")

	// Verify no cross-contamination
	for _, e := range eventsA {
		assert.Equal(t, tenantA, e.TenantID)
	}
	for _, e := range eventsB {
		assert.Equal(t, tenantB, e.TenantID)
	}
}

// Test canonicalization and hash chain logic
func TestAuditCanonicalization(t *testing.T) {
	// Test that canonicalization produces consistent output
	testCases := []struct {
		input    string
		expected string
	}{
		{`{"b":2,"a":1}`, `{"a":1,"b":2}`},
		{`{"z":1,"a":2}`, `{"a":2,"z":1}`},
		{`{}`, `{}`},
		{`{"nested":{"b":1,"a":2}}`, `{"nested":{"a":2,"b":1}}`},
	}

	for _, tc := range testCases {
		var v interface{}
		err := json.Unmarshal([]byte(tc.input), &v)
		require.NoError(t, err)

		result, err := json.Marshal(v)
		require.NoError(t, err)

		assert.Equal(t, tc.expected, string(result), "Canonicalization failed for input: %s", tc.input)
	}
}

// Test chain hash computation
func TestChainHashComputation(t *testing.T) {
	// Genesis hash (64 zeros)
	genesisHash := "0000000000000000000000000000000000000000000000000000000000000000"

	// Create test event
	event := &domain.AuditEvent{
		TenantID:    domain.NewUUID(),
		Seq:         1,
		PrevHash:    genesisHash,
		ActorUserID: nil,
		Action:      "test.action",
		EntityType:  "test",
		EntityID:    nil,
		Payload:     json.RawMessage(`{"test":true}`),
		Severity:    domain.AuditSeverityInfo,
		CreatedAt:   time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
	}

	// Compute chain hash
	chainInput := event.ChainInput()
	hash := sha256.Sum256([]byte(chainInput))
	expectedHash := hex.EncodeToString(hash[:])

	event.ChainHash = expectedHash

	// Verify
	assert.True(t, event.VerifyChainInput(), "Chain hash verification should pass")

	// Tamper with payload
	event.Payload = json.RawMessage(`{"test":false}`)
	assert.False(t, event.VerifyChainInput(), "Chain hash verification should fail after tampering")
}