package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	// Read original payload of seq=3 before tampering
	var originalPayload json.RawMessage
	err = pgadapter.WithTenantTx(ctx, dbPool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT payload FROM public.audit_events WHERE tenant_id = $1 AND seq = 3`, uuid.UUID(tenantID))
		return row.Scan(&originalPayload)
	})
	require.NoError(t, err, "Failed to read original payload for seq=3")

	// Tamper with event at seq=3 by directly updating the database
	err = pgadapter.WithTenantTx(ctx, dbPool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE public.audit_events
			SET payload = '{"data":"tampered"}'
			WHERE tenant_id = $1 AND seq = 3
		`, uuid.UUID(tenantID))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("expected 1 row affected, got %d", tag.RowsAffected())
		}
		return nil
	})
	require.NoError(t, err)

	// Restore original payload after test (cleanup)
	t.Cleanup(func() {
		restoreErr := pgadapter.WithTenantTx(ctx, dbPool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `
				UPDATE public.audit_events
				SET payload = $1
				WHERE tenant_id = $2 AND seq = 3
			`, originalPayload, uuid.UUID(tenantID))
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("restore: expected 1 row affected, got %d", tag.RowsAffected())
			}
			return nil
		})
		if restoreErr != nil {
			t.Logf("Failed to restore original payload: %v", restoreErr)
		}
	})

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

// TestConcurrentAuditAppends tests that concurrent appends to the same tenant
// are properly serialized by the row-level lock (SELECT ... FOR UPDATE),
// producing contiguous seq numbers and a valid hash chain.
func TestConcurrentAuditAppends(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	ctx := tc.Ctx
	dbPool := tc.DBPool

	// Create tenant
	tenantID := domain.NewUUID()
	userID := domain.NewUUID()
	_, err := dbPool.Exec(ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'Test Tenant', 'active')
		ON CONFLICT (id) DO NOTHING
	`, tenantID)
	require.NoError(t, err)

	logger := zerolog.New(zerolog.ConsoleWriter{Out: zerolog.NewTestWriter(t)}).
		Level(zerolog.DebugLevel).
		With().Str("test", "concurrent_audit").Logger()

	auditRepo := pgadapter.NewAuditRepository(dbPool)

	const numGoroutines = 20
	const eventsPerGoroutine = 5
	const totalEvents = numGoroutines * eventsPerGoroutine

	// Use errgroup to capture first error and fail fast
	g, gctx := errgroup.WithContext(ctx)
	errs := make([]error, 0, numGoroutines)
	var errsMu sync.Mutex

	// Launch concurrent appends
	for goroutineID := 0; goroutineID < numGoroutines; goroutineID++ {
		goroutineID := goroutineID // capture loop var
		g.Go(func() error {
			for i := 0; i < eventsPerGoroutine; i++ {
				select {
				case <-gctx.Done():
					return gctx.Err()
				default:
				}
				event := &domain.AuditEvent{
					TenantID:    tenantID,
					ActorUserID: &userID,
					Action:      fmt.Sprintf("concurrent.test.g%d", goroutineID),
					EntityType:  "concurrent_test",
					Severity:    domain.AuditSeverityInfo,
					CreatedAt:   time.Now(),
					Payload:     json.RawMessage(fmt.Sprintf(`{"goroutine":%d, "index":%d}`, goroutineID, i)),
				}
				if err := auditRepo.Append(gctx, event); err != nil {
					errsMu.Lock()
					errs = append(errs, fmt.Errorf("goroutine %d event %d: %w", goroutineID, i, err))
					errsMu.Unlock()
					return err
				}
			}
			return nil
		})
	}

	t.Logf("Pool stats before wait: %+v", dbPool.Stat())

	if err := g.Wait(); err != nil {
		errsMu.Lock()
		defer errsMu.Unlock()
		if len(errs) > 0 {
			t.Fatalf("First append error: %v (total errors: %d)", errs[0], len(errs))
		}
		t.Fatalf("Append failed: %v", err)
	}

	t.Logf("Pool stats after wait: %+v", dbPool.Stat())

	// Verify chain integrity and contiguous sequence
	result, err := auditRepo.VerifyChain(ctx, tenantID, 1, int64(totalEvents))
	require.NoError(t, err)
	require.True(t, result.Valid, "Chain should be valid after concurrent appends")
	require.Equal(t, int64(totalEvents), result.TotalSeen, "All events should be present")

	// Verify sequence is contiguous (no gaps)
	events, err := auditRepo.Query(ctx, pgadapter.AuditFilter{
		TenantID: tenantID,
		Limit:    totalEvents,
		Offset:   0,
	})
	require.NoError(t, err)
	require.Equal(t, totalEvents, len(events))

	// Order by seq ascending to verify contiguity and chaining
	sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })

	// Genesis hash (32 zero bytes)
	var prevHash string = strings.Repeat("0", 64)
	// Verify each event
	for i, e := range events {
		// Contiguous sequence
		expected := int64(i + 1)
		require.Equal(t, expected, e.Seq, "Event %d should have seq %d, got %d", i, expected, e.Seq)

		// Prev hash matches previous block's chain hash (genesis for first)
		require.Equal(t, prevHash, e.PrevHash, "Event %d prev_hash should match previous event's chain hash", i)

		// Recompute hash and compare with stored chain hash
		computed := e.ComputeChainHash()
		require.Equal(t, e.ChainHash, computed, "Event %d chain hash mismatch", i)

		// CreatedAt non-decreasing (when sorted by seq)
		if i > 0 {
			// Truncate to microsecond and UTC for stable comparison
			curr := e.CreatedAt.UTC().Truncate(time.Microsecond)
			prev := events[i-1].CreatedAt.UTC().Truncate(time.Microsecond)
			assert.True(t, curr.Equal(prev) || curr.After(prev), "Event %d created_at should not decrease", i)
		}
		// Update prevHash for next iteration
		prevHash = e.ChainHash
	}

	// Verify all events belong to our tenant
	for _, e := range events {
		assert.Equal(t, tenantID, e.TenantID)
	}

	// Verify hash and prev_hash are 32 bytes (bytea) in the database
	err = pgadapter.WithTenantTx(ctx, dbPool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT seq, octet_length(hash) as hash_len, octet_length(prev_hash) as prev_len
			FROM public.audit_events
			WHERE tenant_id = $1
			ORDER BY seq
		`, tenantID)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var seq int64
			var hashLen, prevLen int
			require.NoError(t, rows.Scan(&seq, &hashLen, &prevLen))
			require.Equal(t, 32, hashLen, "seq %d: hash must be 32 bytes, got %d", seq, hashLen)
			if seq > 1 {
				require.Equal(t, 32, prevLen, "seq %d: prev_hash must be 32 bytes, got %d", seq, prevLen)
			} else {
				// Genesis prev_hash is 32 zero bytes
				require.Equal(t, 32, prevLen, "seq 1: prev_hash must be 32 bytes, got %d", prevLen)
			}
		}
		return rows.Err()
	})
	require.NoError(t, err)

	logger.Info().
		Int("total_events", totalEvents).
		Int("goroutines", numGoroutines).
		Int("events_per_goroutine", eventsPerGoroutine).
		Msg("Concurrent audit appends test completed successfully")
}

func TestAppendAcquiresChainLockRow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	ctx := tc.Ctx
	dbPool := tc.DBPool

	// create tenant only
	tenantID := domain.NewUUID()
	_, err := dbPool.Exec(ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'Test Tenant', 'active')
		ON CONFLICT (id) DO NOTHING
	`, tenantID.String())
	require.NoError(t, err)

	// userID can be random; no need to insert into users table
	userID := domain.NewUUID()

	auditRepo := pgadapter.NewAuditRepository(dbPool)

	event := &domain.AuditEvent{
		TenantID:    tenantID,
		ActorUserID: &userID,
		Action:      "test.lock",
		EntityType:  "test_entity",
		Severity:    domain.AuditSeverityInfo,
		CreatedAt:   time.Now(),
		Payload:     json.RawMessage(`{}`),
	}

	// Append acquires lock row (WITH tenant GUC)
	require.NoError(t, pgadapter.WithTenantTx(ctx, dbPool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		return auditRepo.AppendWithTx(ctx, tx, event)
	}))

	// Verify lock row exists when tenant GUC is set
	var lockExists bool
	err = pgadapter.WithTenantTx(ctx, dbPool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.audit_chain_locks WHERE tenant_id = $1)`, tenantID.String()).Scan(&exists)
		if err != nil {
			return err
		}
		lockExists = exists
		return nil
	})
	require.NoError(t, err)
	assert.True(t, lockExists, "Lock row should exist for tenant when GUC is set")

	// Verify lock row is NOT visible without tenant GUC (RLS isolates)
	var lockExistsWithoutGUC bool
	// Use a fresh connection to avoid any GUC contamination from previous transactions
	conn, err := dbPool.Acquire(ctx)
	require.NoError(t, err)

	// Reset app.current_tenant GUC to DEFAULT (unset) to avoid contamination from pool reuse
	_, err = conn.Exec(ctx, `RESET app.current_tenant`)
	require.NoError(t, err)

	err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.audit_chain_locks WHERE tenant_id = $1)`, tenantID.String()).Scan(&lockExistsWithoutGUC)
	conn.Release()
	require.NoError(t, err)
	assert.False(t, lockExistsWithoutGUC, "Lock row should NOT be visible without tenant GUC due to RLS")

	// Verify lock row is NOT visible with a different tenant GUC
	otherTenantID := domain.NewUUID()
	var lockExistsOther bool
	err = pgadapter.WithTenantTx(ctx, dbPool, otherTenantID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.audit_chain_locks WHERE tenant_id = $1)`, tenantID.String()).Scan(&exists)
		if err != nil {
			return err
		}
		lockExistsOther = exists
		return nil
	})
	require.NoError(t, err)
	assert.False(t, lockExistsOther, "Lock row should NOT be visible with different tenant GUC due to RLS")
}

// TestConcurrentAuditAppendsTwoTenants tests that two different tenants
// can append concurrently without blocking each other (different lock rows).
func TestConcurrentAuditAppendsTwoTenants(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	ctx := tc.Ctx
	dbPool := tc.DBPool

	// Create two tenants
	tenantA := domain.NewUUID()
	tenantB := domain.NewUUID()
	_, err := dbPool.Exec(ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'Tenant A', 'active'), ($2, 'Tenant B', 'active')
		ON CONFLICT (id) DO NOTHING
	`, tenantA, tenantB)
	require.NoError(t, err)

	userA := domain.NewUUID()
	userB := domain.NewUUID()

	auditRepo := pgadapter.NewAuditRepository(dbPool)

	logger := zerolog.New(zerolog.ConsoleWriter{Out: zerolog.NewTestWriter(t)}).
		Level(zerolog.DebugLevel).
		With().Str("test", "concurrent_audit_two_tenants").Logger()

	const numGoroutines = 10
	const eventsPerGoroutine = 5
	const totalPerTenant = numGoroutines * eventsPerGoroutine

	var wg sync.WaitGroup
	errChan := make(chan error, numGoroutines*2)

	// Launch concurrent appends for tenant A
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				event := &domain.AuditEvent{
					TenantID:    tenantA,
					ActorUserID: &userA,
					Action:      fmt.Sprintf("tenantA.g%d", goroutineID),
					EntityType:  "concurrent_test",
					Severity:    domain.AuditSeverityInfo,
					CreatedAt:   time.Now(),
					Payload:     json.RawMessage(fmt.Sprintf(`{"goroutine":%d, "index":%d}`, goroutineID, i)),
				}
				if err := auditRepo.Append(ctx, event); err != nil {
					errChan <- fmt.Errorf("tenantA goroutine %d event %d: %w", goroutineID, i, err)
					return
				}
			}
		}(g)
	}

	// Launch concurrent appends for tenant B
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				event := &domain.AuditEvent{
					TenantID:    tenantB,
					ActorUserID: &userB,
					Action:      fmt.Sprintf("tenantB.g%d", goroutineID),
					EntityType:  "concurrent_test",
					Severity:    domain.AuditSeverityInfo,
					CreatedAt:   time.Now(),
					Payload:     json.RawMessage(fmt.Sprintf(`{"goroutine":%d, "index":%d}`, goroutineID, i)),
				}
				if err := auditRepo.Append(ctx, event); err != nil {
					errChan <- fmt.Errorf("tenantB goroutine %d event %d: %w", goroutineID, i, err)
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errChan)

	// Check for any errors
	for err := range errChan {
		require.NoError(t, err)
	}

	// Verify tenant A chain
	resultA, err := auditRepo.VerifyChain(ctx, tenantA, 1, int64(totalPerTenant))
	require.NoError(t, err)
	assert.True(t, resultA.Valid, "Tenant A chain should be valid")
	assert.Equal(t, int64(totalPerTenant), resultA.TotalSeen, "Tenant A all events present")

	// Verify tenant B chain
	resultB, err := auditRepo.VerifyChain(ctx, tenantB, 1, int64(totalPerTenant))
	require.NoError(t, err)
	assert.True(t, resultB.Valid, "Tenant B chain should be valid")
	assert.Equal(t, int64(totalPerTenant), resultB.TotalSeen, "Tenant B all events present")

	logger.Info().
		Int("tenantA_events", totalPerTenant).
		Int("tenantB_events", totalPerTenant).
		Msg("Two-tenant concurrent audit appends test completed successfully")
}

// TestTenantLockIsolation tests that a lock held by tenant A does not block tenant B.
// It opens a transaction for tenant A, acquires the lock, and verifies that:
// 1. Tenant B can still append (different lock row)
// 2. Tenant A's second append in the same transaction succeeds (same lock)
// 3. After commit, tenant A's lock is released
func TestTenantLockIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tc := SetupTestContainers(t)
	defer tc.Teardown(t)

	ctx := tc.Ctx
	dbPool := tc.DBPool

	// Create two tenants
	tenantA := domain.NewUUID()
	tenantB := domain.NewUUID()
	_, err := dbPool.Exec(ctx, `
		INSERT INTO public.tenants (id, name, status) VALUES ($1, 'Tenant A', 'active'), ($2, 'Tenant B', 'active')
		ON CONFLICT (id) DO NOTHING
	`, tenantA, tenantB)
	require.NoError(t, err)

	userA := domain.NewUUID()
	userB := domain.NewUUID()

	auditRepo := pgadapter.NewAuditRepository(dbPool)

	// Step 1: Tenant A opens a transaction and acquires the lock
	var lockHeldA bool
	err = pgadapter.WithTenantTx(ctx, dbPool, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		// First append for tenant A - acquires lock
		eventA1 := &domain.AuditEvent{
			TenantID:    tenantA,
			ActorUserID: &userA,
			Action:      "tenantA.hold_lock",
			EntityType:  "lock_test",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(`{"step":1}`),
		}
		if err := auditRepo.AppendWithTx(ctx, tx, eventA1); err != nil {
			return err
		}
		lockHeldA = true

		// Step 2: While tenant A holds the lock, tenant B should be able to append
		eventB := &domain.AuditEvent{
			TenantID:    tenantB,
			ActorUserID: &userB,
			Action:      "tenantB.concurrent",
			EntityType:  "lock_test",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(`{"tenant":"B"}`),
		}
		if err := auditRepo.Append(ctx, eventB); err != nil {
			return fmt.Errorf("tenant B should not be blocked by tenant A's lock: %w", err)
		}

		// Step 3: Tenant A's second append in the same transaction should succeed
		eventA2 := &domain.AuditEvent{
			TenantID:    tenantA,
			ActorUserID: &userA,
			Action:      "tenantA.second",
			EntityType:  "lock_test",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(`{"step":2}`),
		}
		if err := auditRepo.AppendWithTx(ctx, tx, eventA2); err != nil {
			return fmt.Errorf("tenant A's second append should succeed: %w", err)
		}

		return nil
	})
	require.NoError(t, err)
	assert.True(t, lockHeldA, "Tenant A should have held the lock")

	// Step 4: After tenant A's transaction commits, verify both chains
	resultA, err := auditRepo.VerifyChain(ctx, tenantA, 1, 2)
	require.NoError(t, err)
	require.True(t, resultA.Valid, "Tenant A chain should be valid")
	require.Equal(t, int64(2), resultA.TotalSeen, "Tenant A should have 2 events")

	resultB, err := auditRepo.VerifyChain(ctx, tenantB, 1, 1)
	require.NoError(t, err)
	require.True(t, resultB.Valid, "Tenant B chain should be valid")
	require.Equal(t, int64(1), resultB.TotalSeen, "Tenant B should have 1 event")

	// Step 5: Tenant A can now append again (lock released after commit)
	eventA3 := &domain.AuditEvent{
		TenantID:    tenantA,
		ActorUserID: &userA,
		Action:      "tenantA.after_commit",
		EntityType:  "lock_test",
		Severity:    domain.AuditSeverityInfo,
		CreatedAt:   time.Now(),
		Payload:     json.RawMessage(`{"step":3}`),
	}
	require.NoError(t, auditRepo.Append(ctx, eventA3))

	resultA, err = auditRepo.VerifyChain(ctx, tenantA, 1, 3)
	require.NoError(t, err)
	require.True(t, resultA.Valid, "Tenant A chain should be valid after third append")
	require.Equal(t, int64(3), resultA.TotalSeen, "Tenant A should have 3 events")

	// Step 6: Key case - A holds lock in an open tx, another connection for A with timeout must expire
	// while B's appends succeed. Then A commits and its append succeeds.
	var lockHeldA2 bool
	err = pgadapter.WithTenantTx(ctx, dbPool, tenantA, func(ctx context.Context, tx pgx.Tx) error {
		// First append for tenant A - acquires lock
		eventA1 := &domain.AuditEvent{
			TenantID:    tenantA,
			ActorUserID: &userA,
			Action:      "tenantA.hold_lock_2",
			EntityType:  "lock_test",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(`{"step":1}`),
		}
		if err := auditRepo.AppendWithTx(ctx, tx, eventA1); err != nil {
			return err
		}
		lockHeldA2 = true

		// While A holds the lock in this tx, try to append from another connection with short timeout
		// This should timeout (context deadline exceeded) because A still holds the lock
		timeoutCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		eventATimeout := &domain.AuditEvent{
			TenantID:    tenantA,
			ActorUserID: &userA,
			Action:      "tenantA.should_timeout",
			EntityType:  "lock_test",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(`{"timeout":true}`),
		}
		err := auditRepo.Append(timeoutCtx, eventATimeout)
		t.Logf("timeout append err=%v", err)
		if err == nil {
			return fmt.Errorf("tenant A append from another connection should have timed out")
		}
		// With FOR UPDATE, the append must block and hit context deadline exceeded
		if !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("expected context deadline exceeded (blocked on lock), got: %w", err)
		}

		// But tenant B should still be able to append (different lock row)
		eventB := &domain.AuditEvent{
			TenantID:    tenantB,
			ActorUserID: &userB,
			Action:      "tenantB.concurrent",
			EntityType:  "lock_test",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(`{"tenant":"B"}`),
		}
		if err := auditRepo.Append(ctx, eventB); err != nil {
			return fmt.Errorf("tenant B should not be blocked by tenant A's lock: %w", err)
		}

		// A's second append in the same tx should succeed
		eventA2 := &domain.AuditEvent{
			TenantID:    tenantA,
			ActorUserID: &userA,
			Action:      "tenantA.second_2",
			EntityType:  "lock_test",
			Severity:    domain.AuditSeverityInfo,
			CreatedAt:   time.Now(),
			Payload:     json.RawMessage(`{"step":2}`),
		}
		if err := auditRepo.AppendWithTx(ctx, tx, eventA2); err != nil {
			return fmt.Errorf("tenant A's second append should succeed: %w", err)
		}

		return nil
	})
	require.NoError(t, err)
	require.True(t, lockHeldA2, "Tenant A should have held the lock")

	// After A's tx commits, A can now append again
	eventA4 := &domain.AuditEvent{
		TenantID:    tenantA,
		ActorUserID: &userA,
		Action:      "tenantA.after_commit_2",
		EntityType:  "lock_test",
		Severity:    domain.AuditSeverityInfo,
		CreatedAt:   time.Now(),
		Payload:     json.RawMessage(`{"step":3}`),
	}
	require.NoError(t, auditRepo.Append(ctx, eventA4))

	resultA, err = auditRepo.VerifyChain(ctx, tenantA, 1, 3)
	require.NoError(t, err)
	require.True(t, resultA.Valid, "Tenant A chain should be valid after third append")
	require.Equal(t, int64(3), resultA.TotalSeen, "Tenant A should have 3 events")

	resultB, err = auditRepo.VerifyChain(ctx, tenantB, 1, 2)
	require.NoError(t, err)
	require.True(t, resultB.Valid, "Tenant B chain should be valid")
	require.Equal(t, int64(2), resultB.TotalSeen, "Tenant B should have 2 events")
}
