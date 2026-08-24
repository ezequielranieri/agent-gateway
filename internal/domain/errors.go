package domain

import "errors"

// Sentinel errors for the domain layer
var (
	// ErrNotFound indicates a resource was not found
	ErrNotFound = errors.New("resource not found")

	// ErrConflict indicates a resource conflict (e.g., duplicate unique field)
	ErrConflict = errors.New("resource conflict")

	// ErrUnauthorized indicates authentication is required or failed
	ErrUnauthorized = errors.New("unauthorized")

	// ErrForbidden indicates the authenticated user lacks permission
	ErrForbidden = errors.New("forbidden")

	// ErrValidation indicates input validation failed
	ErrValidation = errors.New("validation failed")

	// ErrRateLimited indicates the request was rate limited
	ErrRateLimited = errors.New("rate limited")

	// ErrGuardrailViolation indicates a guardrail check failed
	ErrGuardrailViolation = errors.New("guardrail violation")

	// ErrTenantMismatch indicates the token tenant doesn't match the request tenant
	ErrTenantMismatch = errors.New("tenant mismatch")

	// ErrRLSViolation indicates a Row Level Security policy violation
	ErrRLSViolation = errors.New("RLS violation")

	// ErrReplayDetected indicates a replay attack was detected (refresh token reuse)
	ErrReplayDetected = errors.New("replay detected")

	// ErrExpired indicates a token or resource has expired
	ErrExpired = errors.New("expired")

	// ErrInvalidToken indicates a token is malformed or invalid
	ErrInvalidToken = errors.New("invalid token")

	// ErrInvalidCredentials indicates invalid login credentials
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrReviewNotPending indicates the review is not in PENDING state
	ErrReviewNotPending = errors.New("review not pending")

	// ErrNotImplemented indicates a feature is not yet implemented
	ErrNotImplemented = errors.New("not implemented")
)