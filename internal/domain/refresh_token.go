package domain

import (
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// RefreshToken represents a refresh token in the system
type RefreshToken struct {
	ID           UUID       `json:"id"`
	UserID       UUID       `json:"user_id"`
	TenantID     UUID       `json:"tenant_id"`
	TokenHash    []byte     `json:"-"` // SHA-256 hash, never returned
	FamilyID     UUID       `json:"family_id"`
	Revoked      bool       `json:"revoked"`
	ExpiresAt    time.Time  `json:"expires_at"`
	UserAgent    string     `json:"user_agent,omitempty"`
	IP           string     `json:"ip,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   sql.NullTime `json:"last_used_at,omitempty"`
	RotatedFrom  pgtype.UUID `json:"rotated_from,omitempty"`
}

// IsExpired returns true if the refresh token has expired
func (r *RefreshToken) IsExpired() bool {
	return time.Now().After(r.ExpiresAt)
}

// IsActive returns true if the refresh token is active (not revoked, not expired)
func (r *RefreshToken) IsActive() bool {
	return !r.Revoked && !r.IsExpired()
}

// Session represents a user session (for listing active sessions)
type Session struct {
	ID         UUID         `json:"id"`
	UserID     UUID         `json:"user_id"`
	CreatedAt  time.Time    `json:"created_at"`
	LastUsedAt sql.NullTime `json:"last_used_at,omitempty"`
	UserAgent  sql.NullString `json:"user_agent,omitempty"`
	IP         string       `json:"ip,omitempty"`
}