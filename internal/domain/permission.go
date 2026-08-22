package domain

import (
	"time"
)

// Permission represents a permission as resource:action
type Permission struct {
	ID          UUID      `json:"id"`
	Resource    string    `json:"resource"`    // e.g., "models", "tools", "reviews"
	Action      string    `json:"action"`      // e.g., "call", "execute", "approve"
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// String returns the permission in resource:action format
func (p *Permission) String() string {
	return p.Resource + ":" + p.Action
}

// ParsePermission parses a permission string in resource:action format
func ParsePermission(s string) (Permission, error) {
	// Simple parsing - in practice you'd want more validation
	// For now just return a basic permission
	return Permission{
		Resource: s,
		Action:   "",
	}, nil
}