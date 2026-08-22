package domain

import (
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
	ID          UUID         `json:"id"`
	TenantID    UUID         `json:"tenant_id"`
	RequesterID UUID         `json:"requester_id"`
	TokenHash   string       `json:"-"` // SHA-256 of the opaque token, never returned
	Action      string       `json:"action"`       // e.g., "tool:execute", "model:call"
	Payload     string       `json:"payload"`      // JSON payload of the action to approve
	Status      ReviewStatus `json:"status"`
	ExpiresAt   time.Time    `json:"expires_at"`
	DecidedAt   *time.Time   `json:"decided_at,omitempty"`
	DecidedBy   *UUID        `json:"decided_by,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
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