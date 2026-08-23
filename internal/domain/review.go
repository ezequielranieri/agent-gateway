package domain

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// ReviewStatus represents the status of a review request
type ReviewStatus string

const (
	ReviewStatusPending   ReviewStatus = "PENDING"
	ReviewStatusApproved  ReviewStatus = "APPROVED"
	ReviewStatusRejected  ReviewStatus = "REJECTED"
	ReviewStatusExpired   ReviewStatus = "EXPIRED"
	ReviewStatusExecuted  ReviewStatus = "EXECUTED"
)

// ReviewRequest represents a human-in-the-loop approval request
type ReviewRequest struct {
	ID            UUID         `json:"id"`
	TenantID      UUID         `json:"tenant_id"`
	RequesterID   UUID         `json:"requester_id"`
	ReviewerID    *UUID        `json:"reviewer_id,omitempty"` // Optional assigned reviewer
	TokenHash     string       `json:"-"`                     // SHA-256 of the opaque token, never returned
	Action        string       `json:"action"`                // e.g., "tool:execute", "model:call"
	Payload       string       `json:"payload"`               // JSON payload of the action to approve
	Status        ReviewStatus `json:"status"`
	ExpiresAt     time.Time    `json:"expires_at"`
	DecidedAt     *time.Time   `json:"decided_at,omitempty"`
	DecidedBy     *UUID        `json:"decided_by,omitempty"`
	DecisionReason string      `json:"decision_reason,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at,omitempty"`
}

// GenerateOpaqueToken generates a cryptographically secure random token (32 bytes = 64 hex chars)
func GenerateOpaqueToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate random token: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// IsPending returns true if the review is pending
func (r *ReviewRequest) IsPending() bool {
	return r.Status == ReviewStatusPending
}

// IsExpired returns true if the review has expired
func (r *ReviewRequest) IsExpired() bool {
	return time.Now().After(r.ExpiresAt)
}

// CanTransitionTo returns true if the review can transition to the given status
func (r *ReviewRequest) CanTransitionTo(status ReviewStatus) bool {
	switch r.Status {
	case ReviewStatusPending:
		return status == ReviewStatusApproved || status == ReviewStatusRejected || status == ReviewStatusExpired
	case ReviewStatusApproved:
		return status == ReviewStatusExecuted
	default:
		return false
	}
}