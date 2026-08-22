package domain

import (
	"time"
)

// Role represents a role in the global catalog (no tenant_id)
type Role struct {
	ID          UUID      `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}