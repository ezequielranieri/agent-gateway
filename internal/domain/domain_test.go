package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTenant_IsActive(t *testing.T) {
	activeTenant := &Tenant{
		ID:        NewUUID(),
		Name:      "Test Tenant",
		Status:    TenantStatusActive,
		CreatedAt: time.Now(),
	}
	assert.True(t, activeTenant.IsActive())

	suspendedTenant := &Tenant{
		ID:        NewUUID(),
		Name:      "Suspended Tenant",
		Status:    TenantStatusSuspended,
		CreatedAt: time.Now(),
	}
	assert.False(t, suspendedTenant.IsActive())
}

func TestUser_IsActive(t *testing.T) {
	activeUser := &User{
		ID:           NewUUID(),
		TenantID:     NewUUID(),
		Email:        "test@example.com",
		PasswordHash: "hash",
		Status:       UserStatusActive,
		CreatedAt:    time.Now(),
	}
	assert.True(t, activeUser.IsActive())

	suspendedUser := &User{
		ID:           NewUUID(),
		TenantID:     NewUUID(),
		Email:        "suspended@example.com",
		PasswordHash: "hash",
		Status:       UserStatusSuspended,
		CreatedAt:    time.Now(),
	}
	assert.False(t, suspendedUser.IsActive())

	deletedUser := &User{
		ID:           NewUUID(),
		TenantID:     NewUUID(),
		Email:        "deleted@example.com",
		PasswordHash: "hash",
		Status:       UserStatusDeleted,
		CreatedAt:    time.Now(),
	}
	assert.False(t, deletedUser.IsActive())
}

func TestPermission_String(t *testing.T) {
	p := Permission{
		Resource: "models",
		Action:   "call",
	}
	assert.Equal(t, "models:call", p.String())
}

func TestQuota_DefaultQuotas(t *testing.T) {
	q := DefaultQuotas()
	assert.Equal(t, 60, q.RequestsPerMin)
	assert.Equal(t, 10000, q.TokensPerMin)
	assert.Equal(t, 30, q.ToolExecsPerMin)
}

func TestAuditEvent_ChainInput(t *testing.T) {
	tenantID := NewUUID()
	userID := NewUUID()
	entityID := NewUUID()

	event := &AuditEvent{
		ID:          NewUUID(),
		TenantID:    tenantID,
		Seq:         1,
		PrevHash:    "0000000000000000000000000000000000000000000000000000000000000000",
		ActorUserID: &userID,
		Action:      "test_action",
		EntityType:  "test_entity",
		EntityID:    &entityID,
		Severity:    AuditSeverityInfo,
		CreatedAt:   time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
	}

	input := event.ChainInput()
	// Should contain all fields in the correct order
	assert.Contains(t, input, "0000000000000000000000000000000000000000000000000000000000000000")
	assert.Contains(t, input, "1")
	assert.Contains(t, input, tenantID.String())
	assert.Contains(t, input, userID.String())
	assert.Contains(t, input, "test_action")
	assert.Contains(t, input, "test_entity")
	assert.Contains(t, input, entityID.String())
	assert.Contains(t, input, "{}")
	assert.Contains(t, input, "2024-01-15T10:30:00Z")
}

func TestAuditEvent_ChainInput_WithPayload(t *testing.T) {
	tenantID := NewUUID()
	event := &AuditEvent{
		ID:       NewUUID(),
		TenantID: tenantID,
		Seq:      1,
		PrevHash: "0000000000000000000000000000000000000000000000000000000000000000",
		Action:   "test_action",
		EntityType: "test_entity",
		Payload:  []byte(`{"key":"value","number":42}`),
		Severity: AuditSeverityInfo,
		CreatedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
	}

	input := event.ChainInput()
	// Payload should be canonicalized (keys sorted)
	assert.Contains(t, input, `{"key":"value","number":42}`)
}

func TestReviewRequest_IsPending(t *testing.T) {
	pending := &ReviewRequest{
		ID:        NewUUID(),
		TenantID:  NewUUID(),
		RequesterID: NewUUID(),
		Status:    ReviewStatusPending,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	assert.True(t, pending.IsPending())

	approved := &ReviewRequest{
		ID:        NewUUID(),
		TenantID:  NewUUID(),
		RequesterID: NewUUID(),
		Status:    ReviewStatusApproved,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	assert.False(t, approved.IsPending())
}

func TestReviewRequest_IsExpired(t *testing.T) {
	expired := &ReviewRequest{
		ID:        NewUUID(),
		TenantID:  NewUUID(),
		RequesterID: NewUUID(),
		Status:    ReviewStatusPending,
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	assert.True(t, expired.IsExpired())

	notExpired := &ReviewRequest{
		ID:        NewUUID(),
		TenantID:  NewUUID(),
		RequesterID: NewUUID(),
		Status:    ReviewStatusPending,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	assert.False(t, notExpired.IsExpired())
}

func TestReviewRequest_CanTransitionTo(t *testing.T) {
	pending := &ReviewRequest{
		ID:        NewUUID(),
		TenantID:  NewUUID(),
		RequesterID: NewUUID(),
		Status:    ReviewStatusPending,
		ExpiresAt: time.Now().Add(time.Hour),
	}

	assert.True(t, pending.CanTransitionTo(ReviewStatusApproved))
	assert.True(t, pending.CanTransitionTo(ReviewStatusRejected))
	assert.True(t, pending.CanTransitionTo(ReviewStatusExpired))
	assert.False(t, pending.CanTransitionTo(ReviewStatusExecuted))
	assert.False(t, pending.CanTransitionTo(ReviewStatusPending))

	approved := &ReviewRequest{
		ID:        NewUUID(),
		TenantID:  NewUUID(),
		RequesterID: NewUUID(),
		Status:    ReviewStatusApproved,
		ExpiresAt: time.Now().Add(time.Hour),
	}

	assert.True(t, approved.CanTransitionTo(ReviewStatusExecuted))
	assert.False(t, approved.CanTransitionTo(ReviewStatusPending))
	assert.False(t, approved.CanTransitionTo(ReviewStatusApproved))

	executed := &ReviewRequest{
		ID:        NewUUID(),
		TenantID:  NewUUID(),
		RequesterID: NewUUID(),
		Status:    ReviewStatusExecuted,
		ExpiresAt: time.Now().Add(time.Hour),
	}

	assert.False(t, executed.CanTransitionTo(ReviewStatusPending))
	assert.False(t, executed.CanTransitionTo(ReviewStatusExecuted))
}

func TestSentinelErrors(t *testing.T) {
	// Ensure all sentinel errors are defined and non-nil
	require.NotNil(t, ErrNotFound)
	require.NotNil(t, ErrConflict)
	require.NotNil(t, ErrUnauthorized)
	require.NotNil(t, ErrForbidden)
	require.NotNil(t, ErrValidation)
	require.NotNil(t, ErrRateLimited)
	require.NotNil(t, ErrGuardrailViolation)
	require.NotNil(t, ErrTenantMismatch)
	require.NotNil(t, ErrRLSViolation)
	require.NotNil(t, ErrReplayDetected)
	require.NotNil(t, ErrExpired)
	require.NotNil(t, ErrInvalidToken)

	// Ensure they have meaningful messages
	assert.Equal(t, "resource not found", ErrNotFound.Error())
	assert.Equal(t, "resource conflict", ErrConflict.Error())
	assert.Equal(t, "unauthorized", ErrUnauthorized.Error())
	assert.Equal(t, "forbidden", ErrForbidden.Error())
	assert.Equal(t, "validation failed", ErrValidation.Error())
	assert.Equal(t, "rate limited", ErrRateLimited.Error())
	assert.Equal(t, "guardrail violation", ErrGuardrailViolation.Error())
	assert.Equal(t, "tenant mismatch", ErrTenantMismatch.Error())
	assert.Equal(t, "RLS violation", ErrRLSViolation.Error())
	assert.Equal(t, "replay detected", ErrReplayDetected.Error())
	assert.Equal(t, "expired", ErrExpired.Error())
	assert.Equal(t, "invalid token", ErrInvalidToken.Error())
}