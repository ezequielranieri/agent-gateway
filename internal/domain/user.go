package domain

import (
	"time"
)

// UserStatus represents the status of a user
type UserStatus string

const (
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended"
	UserStatusDeleted   UserStatus = "deleted"
)

// User represents a user within a tenant
type User struct {
	ID           UUID        `json:"id"`
	TenantID     UUID        `json:"tenant_id"`
	Email        string      `json:"email"`
	PasswordHash string      `json:"-"` // Never serialize
	Status       UserStatus  `json:"status"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// IsActive returns true if the user is active
func (u *User) IsActive() bool {
	return u.Status == UserStatusActive
}