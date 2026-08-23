package domain

import (
	"time"
)

// SuperAdmin represents a global platform administrator (no tenant_id)
type SuperAdmin struct {
	ID           UUID     `json:"id"`
	Email        string   `json:"email"`
	PasswordHash string   `json:"-"` // Never serialize
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}