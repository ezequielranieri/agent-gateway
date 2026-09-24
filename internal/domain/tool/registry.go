package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/gowebpki/jcs"
)

// ToolDefinition represents a persisted tool definition with hash-based integrity.
type ToolDefinition struct {
	ID                int64           // BIGSERIAL from database
	TenantID          domain.UUID
	Name              string
	Description       string
	InputSchema       json.RawMessage // JSON Schema object
	Grants            json.RawMessage // Array of grant strings
	ExecutionTimeoutMs uint64         // Execution timeout in milliseconds (replaces fuel_limit)
	MemoryPages       uint32          // WASM memory pages (64KB each)
	Hash              string          // SHA-256 hex (64 chars)
	IsActive          bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ToolRepository defines the interface for tool definition persistence.
// Implementations must emit audit events on all write operations.
// All methods require a tenant_id for RLS enforcement (super-admin can override via context).
type ToolRepository interface {
	// GetByName returns tool definition by name for a tenant (only active tools).
	GetByName(ctx context.Context, tenantID domain.UUID, name string) (*ToolDefinition, error)

	// CreateToolDefinition inserts a new tool definition for a tenant.
	CreateToolDefinition(ctx context.Context, tenantID domain.UUID, def *ToolDefinition) error

	// UpdateToolDefinition updates grants, limits, or definition fields for a tenant.
	// Returns true if hash was recomputed (definition fields changed).
	UpdateToolDefinition(ctx context.Context, tenantID domain.UUID, def *ToolDefinition) (hashChanged bool, err error)

	// DeactivateToolDefinition soft-deletes a tool for a tenant (is_active = false).
	DeactivateToolDefinition(ctx context.Context, tenantID domain.UUID, name string) error

	// UpsertToolDefinition inserts or updates a tool for a tenant (used by boot seed).
	UpsertToolDefinition(ctx context.Context, tenantID domain.UUID, def *ToolDefinition) error

	// InitFromConfig boot-seeds from ToolConfig.Tools for a specific tenant.
	InitFromConfig(ctx context.Context, tenantID domain.UUID, cfg *ToolConfig) error
}

// hashInput represents the canonical input for hash computation.
type hashInput struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ComputeHash computes SHA-256 over canonical JSON of name, description, and parameters.
// Uses RFC 8785 (JCS) canonicalization via gowebpki/jcs for deterministic serialization.
func ComputeHash(name, description string, parameters json.RawMessage) string {
	// Treat nil parameters as empty object
	if parameters == nil {
		parameters = json.RawMessage(`{}`)
	}

	input := hashInput{
		Name:        name,
		Description: description,
		Parameters:  parameters,
	}

	// Marshal to canonical JSON per RFC 8785 (sorted keys, no whitespace, deterministic)
	canonical, err := json.Marshal(input)
	if err != nil {
		// This should never happen with valid input, but fallback to standard JSON
		canonical, _ = json.Marshal(input)
	} else {
		canonical, err = jcs.Transform(canonical)
		if err != nil {
			// Fallback if JCS fails (shouldn't happen with valid JSON)
			canonical, _ = json.Marshal(input)
		}
	}

	// Compute SHA-256
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:])
}

// Sentinel errors for tool registry operations are defined in errors.go