package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	postgressqlc "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres/sqlc"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
)

// cacheKey is a struct key for the LRU cache to ensure proper tenant isolation
// Using struct instead of string concatenation prevents key collisions
type cacheKey struct {
	TenantID domain.UUID
	Name     string
}

// ToolRepository implements the tool.ToolRepository interface with LRU caching and audit emission
type ToolRepository struct {
	queries   *postgressqlc.Queries
	pool      *pgxpool.Pool
	auditRepo *AuditRepository
	logger    zerolog.Logger
	cache     *expirable.LRU[cacheKey, *tool.ToolDefinition]
	mu        sync.RWMutex
}

// ToolRepositoryConfig holds configuration for the tool repository
type ToolRepositoryConfig struct {
	CacheTTL       time.Duration
	CacheMaxEntries int
}

// NewToolRepository creates a new tool repository with LRU cache and audit emission
func NewToolRepository(pool *pgxpool.Pool, auditRepo *AuditRepository, logger zerolog.Logger, cacheTTL time.Duration, cacheMaxEntries int) *ToolRepository {
	if cacheTTL <= 0 {
		cacheTTL = 5 * time.Minute
	}
	if cacheMaxEntries <= 0 {
		cacheMaxEntries = 1000
	}

	return &ToolRepository{
		queries:   postgressqlc.New(pool),
		pool:      pool,
		auditRepo: auditRepo,
		logger:    logger.With().Str("component", "tool_repository").Logger(),
		cache:     expirable.NewLRU[cacheKey, *tool.ToolDefinition](cacheMaxEntries, nil, cacheTTL),
	}
}

// GetByName returns tool definition by name for a tenant (only active tools).
// Uses LRU cache with TTL for performance.
func (r *ToolRepository) GetByName(ctx context.Context, tenantID domain.UUID, name string) (*tool.ToolDefinition, error) {
	key := cacheKey{TenantID: tenantID, Name: name}

	// Check cache first
	r.mu.RLock()
	cached, ok := r.cache.Get(key)
	r.mu.RUnlock()

	if ok {
		r.logger.Debug().Str("tool", name).Str("tenant", tenantID.String()).Msg("Cache hit")
		return cached, nil
	}

	r.logger.Debug().Str("tool", name).Str("tenant", tenantID.String()).Msg("Cache miss - querying database")

	// Cache miss - query database
	var def *tool.ToolDefinition
	err := WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// Use transaction-bound queries so RLS GUC is active on this connection
		q := r.queries.WithTx(tx)

		result, err := q.GetToolDefinitionByName(ctx, postgressqlc.GetToolDefinitionByNameParams{
			TenantID: uuid.UUID(tenantID),
			Name:     name,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return tool.ErrToolNotFound
			}
			return err
		}
		def = r.convertSQLCToolDefinition(result)
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Store in cache
	r.mu.Lock()
	r.cache.Add(key, def)
	r.mu.Unlock()

	return def, nil
}

// GetByNameTx returns tool definition by name for a tenant using an existing transaction.
// The transaction must already have the tenant GUC set.
// Does NOT use cache (caller manages caching if needed).
func (r *ToolRepository) GetByNameTx(ctx context.Context, tx pgx.Tx, tenantID domain.UUID, name string) (*tool.ToolDefinition, error) {
	// Use transaction-bound queries so RLS GUC is active on this connection
	q := r.queries.WithTx(tx)

	result, err := q.GetToolDefinitionByName(ctx, postgressqlc.GetToolDefinitionByNameParams{
		TenantID: uuid.UUID(tenantID),
		Name:     name,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, tool.ErrToolNotFound
		}
		return nil, err
	}
	return r.convertSQLCToolDefinition(result), nil
}

// CreateToolDefinition inserts a new tool definition for a tenant.
// Invalidates cache and emits audit event on success.
func (r *ToolRepository) CreateToolDefinition(ctx context.Context, tenantID domain.UUID, def *tool.ToolDefinition) error {
	// Ensure hash is computed
	if def.Hash == "" {
		def.Hash = tool.ComputeHash(def.Name, def.Description, def.InputSchema)
	}

	err := WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// Use transaction-bound queries so RLS GUC is active on this connection
		q := r.queries.WithTx(tx)

		// Insert tool definition
		params := postgressqlc.CreateToolDefinitionParams{
			TenantID:           uuid.UUID(tenantID),
			Name:               def.Name,
			Description:        def.Description,
			InputSchema:        def.InputSchema,
			Grants:             def.Grants,
			ExecutionTimeoutMs: int64(def.ExecutionTimeoutMs),
			MemoryPages:        int32(def.MemoryPages),
			Hash:               def.Hash,
			IsActive:           def.IsActive,
		}

		created, err := q.CreateToolDefinition(ctx, params)
		if err != nil {
			return err
		}

		// Update def with generated values
		def.ID = created.ID
		def.TenantID = domain.UUID(created.TenantID)
		def.CreatedAt = created.CreatedAt
		def.UpdatedAt = created.UpdatedAt

		// Emit audit event within the same transaction
		if r.auditRepo != nil {
			auditEvent := r.buildAuditEvent(tenantID, def, "CREATE", nil, &def.Hash, nil, domain.AuditSeverityInfo)
			if err := r.auditRepo.AppendWithTx(ctx, tx, auditEvent); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	// Invalidate cache AFTER successful commit
	r.invalidateCache(tenantID, def.Name)

	return nil
}

// UpdateToolDefinition updates grants, limits, or definition fields for a tenant.
// Returns true if hash was recomputed (definition fields changed).
// Invalidates cache and emits audit event on success.
func (r *ToolRepository) UpdateToolDefinition(ctx context.Context, tenantID domain.UUID, def *tool.ToolDefinition) (hashChanged bool, err error) {
	// Get existing tool to compare
	existing, err := r.GetByName(ctx, tenantID, def.Name)
	if err != nil {
		return false, err
	}

	// Determine what changed
	oldHash := existing.Hash
	newHash := def.Hash
	if newHash == "" {
		newHash = tool.ComputeHash(def.Name, def.Description, def.InputSchema)
	}
	hashChanged = oldHash != newHash

	// Build changed fields list
	var changedFields []string
	if existing.Description != def.Description {
		changedFields = append(changedFields, "description")
	}
	if !jsonEqual(existing.InputSchema, def.InputSchema) {
		changedFields = append(changedFields, "input_schema")
	}
	if !jsonEqual(existing.Grants, def.Grants) {
		changedFields = append(changedFields, "grants")
	}
	if existing.ExecutionTimeoutMs != def.ExecutionTimeoutMs {
		changedFields = append(changedFields, "execution_timeout_ms")
	}
	if existing.MemoryPages != def.MemoryPages {
		changedFields = append(changedFields, "memory_pages")
	}
	if existing.IsActive != def.IsActive {
		changedFields = append(changedFields, "is_active")
	}

	// Determine severity based on what changed
	severity := domain.AuditSeverityWarn
	if hashChanged {
		severity = domain.AuditSeverityCritical
	}

	err = WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// Use transaction-bound queries so RLS GUC is active on this connection
		q := r.queries.WithTx(tx)

		// Update tool definition
		params := postgressqlc.UpdateToolDefinitionParams{
			TenantID:           uuid.UUID(tenantID),
			Name:               def.Name,
			Description:        def.Description,
			InputSchema:        def.InputSchema,
			Grants:             def.Grants,
			ExecutionTimeoutMs: int64(def.ExecutionTimeoutMs),
			MemoryPages:        int32(def.MemoryPages),
			Hash:               newHash,
			IsActive:           def.IsActive,
		}

		updated, err := q.UpdateToolDefinition(ctx, params)
		if err != nil {
			return err
		}

		// Update def with new values
		def.ID = updated.ID
		def.TenantID = domain.UUID(updated.TenantID)
		def.Hash = updated.Hash
		def.UpdatedAt = updated.UpdatedAt

		// Emit audit event within the same transaction
		if r.auditRepo != nil {
			auditEvent := r.buildAuditEvent(tenantID, def, "UPDATE", &oldHash, &newHash, changedFields, severity)
			if err := r.auditRepo.AppendWithTx(ctx, tx, auditEvent); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return false, err
	}

	// Invalidate cache AFTER successful commit
	r.invalidateCache(tenantID, def.Name)

	return hashChanged, nil
}

// UpdateToolDefinitionTx updates a tool definition within an existing transaction.
// The caller is responsible for managing the transaction (commit/rollback).
// The transaction must already have the tenant GUC set.
func (r *ToolRepository) UpdateToolDefinitionTx(ctx context.Context, tx pgx.Tx, tenantID domain.UUID, def *tool.ToolDefinition) (hashChanged bool, err error) {
	// Get existing tool to compare (uses same transaction for RLS)
	existing, err := r.GetByNameTx(ctx, tx, tenantID, def.Name)
	if err != nil {
		return false, err
	}

	// Determine what changed
	oldHash := existing.Hash
	newHash := def.Hash
	if newHash == "" {
		newHash = tool.ComputeHash(def.Name, def.Description, def.InputSchema)
	}
	hashChanged = oldHash != newHash

	// Build changed fields list
	var changedFields []string
	if existing.Description != def.Description {
		changedFields = append(changedFields, "description")
	}
	if !jsonEqual(existing.InputSchema, def.InputSchema) {
		changedFields = append(changedFields, "input_schema")
	}
	if !jsonEqual(existing.Grants, def.Grants) {
		changedFields = append(changedFields, "grants")
	}
	if existing.ExecutionTimeoutMs != def.ExecutionTimeoutMs {
		changedFields = append(changedFields, "execution_timeout_ms")
	}
	if existing.MemoryPages != def.MemoryPages {
		changedFields = append(changedFields, "memory_pages")
	}
	if existing.IsActive != def.IsActive {
		changedFields = append(changedFields, "is_active")
	}

	// Determine severity based on what changed
	severity := domain.AuditSeverityWarn
	if hashChanged {
		severity = domain.AuditSeverityCritical
	}

	// Use transaction-bound queries so RLS GUC is active on this connection
	q := r.queries.WithTx(tx)

	// Update tool definition
	params := postgressqlc.UpdateToolDefinitionParams{
		TenantID:           uuid.UUID(tenantID),
		Name:               def.Name,
		Description:        def.Description,
		InputSchema:        def.InputSchema,
		Grants:             def.Grants,
		ExecutionTimeoutMs: int64(def.ExecutionTimeoutMs),
		MemoryPages:        int32(def.MemoryPages),
		Hash:               newHash,
		IsActive:           def.IsActive,
	}

	updated, err := q.UpdateToolDefinition(ctx, params)
	if err != nil {
		return false, err
	}

	// Update def with new values
	def.ID = updated.ID
	def.TenantID = domain.UUID(updated.TenantID)
	def.Hash = updated.Hash
	def.UpdatedAt = updated.UpdatedAt

	// Emit audit event within the same transaction
	if r.auditRepo != nil {
		auditEvent := r.buildAuditEvent(tenantID, def, "UPDATE", &oldHash, &newHash, changedFields, severity)
		if err := r.auditRepo.AppendWithTx(ctx, tx, auditEvent); err != nil {
			return false, err
		}
	}

	return hashChanged, nil
}

// DeactivateToolDefinition soft-deletes a tool for a tenant (is_active = false).
// Invalidates cache and emits audit event on success.
func (r *ToolRepository) DeactivateToolDefinition(ctx context.Context, tenantID domain.UUID, name string) error {
	// Get existing tool for audit
	existing, err := r.GetByName(ctx, tenantID, name)
	if err != nil {
		return err
	}

	oldHash := existing.Hash

	err = WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// Use transaction-bound queries so RLS GUC is active on this connection
		q := r.queries.WithTx(tx)

		// Soft delete
		params := postgressqlc.DeactivateToolDefinitionParams{
			TenantID: uuid.UUID(tenantID),
			Name:     name,
		}

		if err := q.DeactivateToolDefinition(ctx, params); err != nil {
			return err
		}

		// Emit audit event within the same transaction
		if r.auditRepo != nil {
			auditEvent := r.buildAuditEvent(tenantID, existing, "DELETE", &oldHash, nil, nil, domain.AuditSeverityWarn)
			if err := r.auditRepo.AppendWithTx(ctx, tx, auditEvent); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	// Invalidate cache AFTER successful commit
	r.invalidateCache(tenantID, name)

	return nil
}

// UpsertToolDefinition inserts or updates a tool for a tenant (used by boot seed).
// Emits audit event with info severity.
// Does NOT invalidate cache (boot seed handles bulk cache clear).
func (r *ToolRepository) UpsertToolDefinition(ctx context.Context, tenantID domain.UUID, def *tool.ToolDefinition) error {
	// Ensure hash is computed
	if def.Hash == "" {
		def.Hash = tool.ComputeHash(def.Name, def.Description, def.InputSchema)
	}

	err := WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// Use transaction-bound queries so RLS GUC is active on this connection
		q := r.queries.WithTx(tx)

		params := postgressqlc.UpsertToolDefinitionParams{
			TenantID:           uuid.UUID(tenantID),
			Name:               def.Name,
			Description:        def.Description,
			InputSchema:        def.InputSchema,
			Grants:             def.Grants,
			ExecutionTimeoutMs: int64(def.ExecutionTimeoutMs),
			MemoryPages:        int32(def.MemoryPages),
			Hash:               def.Hash,
			IsActive:           def.IsActive,
		}

		created, err := q.UpsertToolDefinition(ctx, params)
		if err != nil {
			return err
		}

		def.ID = created.ID
		def.TenantID = domain.UUID(created.TenantID)
		def.CreatedAt = created.CreatedAt
		def.UpdatedAt = created.UpdatedAt

		// Emit audit event within the same transaction (info severity for boot seed)
		if r.auditRepo != nil {
			auditEvent := r.buildAuditEvent(tenantID, def, "CREATE", nil, &def.Hash, nil, domain.AuditSeverityInfo)
			if err := r.auditRepo.AppendWithTx(ctx, tx, auditEvent); err != nil {
				return err
			}
		}

		return nil
	})

	return err
}

// InitFromConfig boot-seeds from ToolConfig.Tools for a specific tenant.
// Runs all upserts in a single transaction (fail-closed).
// Clears cache after bulk upsert.
func (r *ToolRepository) InitFromConfig(ctx context.Context, tenantID domain.UUID, cfg *tool.ToolConfig) error {
	r.logger.Info().Str("tenant", tenantID.String()).Int("tools", len(cfg.Tools)).Msg("Starting boot seed")

	err := WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// Use transaction-bound queries so RLS GUC is active on this connection
		q := r.queries.WithTx(tx)

		for _, toolConfig := range cfg.Tools {
			// Compute hash from FunctionDef (name, description, parameters)
			// For boot seed, we need to extract the function definition
			// The actual parameters would come from the WASM module or config
			hash := tool.ComputeHash(toolConfig.Name, "", json.RawMessage(`{}`))

			// Convert grants to JSON
			grantsJSON, _ := json.Marshal(toolConfig.Grants)

			params := postgressqlc.UpsertToolDefinitionParams{
				TenantID:           uuid.UUID(tenantID),
				Name:               toolConfig.Name,
				Description:        "", // Description comes from WASM module metadata
				InputSchema:        json.RawMessage(`{}`), // Parameters from WASM module
				Grants:             grantsJSON,
				ExecutionTimeoutMs: cfg.EffectiveTimeoutMs(&toolConfig), // Use new timeout method
				MemoryPages:        int32(cfg.EffectiveMemoryPages(&toolConfig)),
				Hash:               hash,
				IsActive:           true,
			}

			if _, err := q.UpsertToolDefinition(ctx, params); err != nil {
				return err
			}

			// Emit audit event for each upsert
			if r.auditRepo != nil {
				auditEvent := r.buildAuditEvent(tenantID, &tool.ToolDefinition{
					TenantID: tenantID,
					Name:     toolConfig.Name,
					Hash:     hash,
				}, "CREATE", nil, &hash, nil, domain.AuditSeverityInfo)
				if err := r.auditRepo.AppendWithTx(ctx, tx, auditEvent); err != nil {
					return err
				}
			}
		}
		return nil
	})

	if err != nil {
		return err
	}

	// Clear cache after bulk upsert
	r.clearCache()

	r.logger.Info().Str("tenant", tenantID.String()).Msg("Boot seed completed")
	return nil
}

// buildAuditEvent creates an audit event for tool definition operations
func (r *ToolRepository) buildAuditEvent(
	tenantID domain.UUID,
	def *tool.ToolDefinition,
	operation string,
	oldHash, newHash *string,
	changedFields []string,
	severity domain.AuditSeverity,
) *domain.AuditEvent {
	payload := toolDefinitionAuditPayload{
		Operation:     operation,
		ToolName:      def.Name,
		OldHash:       oldHash,
		NewHash:       newHash,
		ChangedFields: changedFields,
	}

	fullDef, _ := json.Marshal(def)
	payload.FullDefinition = fullDef

	payloadBytes, _ := json.Marshal(payload)

	return &domain.AuditEvent{
		TenantID:   tenantID,
		ActorUserID: nil, // System actor for boot seed; admin API will set this
		Action:     "tool_definition." + toLower(operation),
		EntityType: "tool_definition",
		EntityID:   nil, // ToolDefinition uses int64 ID; included in payload instead
		Payload:    payloadBytes,
		Severity:   severity,
		CreatedAt:  time.Now().Truncate(time.Microsecond),
	}
}

// invalidateCache removes a specific cache entry
func (r *ToolRepository) invalidateCache(tenantID domain.UUID, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := cacheKey{TenantID: tenantID, Name: name}
	r.cache.Remove(key)
	r.logger.Debug().Str("tool", name).Str("tenant", tenantID.String()).Msg("Cache invalidated")
}

// clearCache clears the entire cache
func (r *ToolRepository) clearCache() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache.Purge()
	r.logger.Debug().Msg("Cache cleared")
}

// convertSQLCToolDefinition converts SQLC tool definition to domain
// Accepts any SQLC row type (GetToolDefinitionByNameRow, GetToolDefinitionByNameAllRow, etc.)
// All row types have identical fields, so we use a type switch to extract them
func (r *ToolRepository) convertSQLCToolDefinition(t interface{}) *tool.ToolDefinition {
	// Use reflection-like pattern via type assertion to extract common fields
	// All SQLC row types for tool_definitions have the same structure
	v := struct {
		ID                 int64
		TenantID           uuid.UUID
		Name               string
		Description        string
		InputSchema        json.RawMessage
		Grants             json.RawMessage
		ExecutionTimeoutMs int64
		MemoryPages        int32
		Hash               string
		IsActive           bool
		CreatedAt          time.Time
		UpdatedAt          time.Time
	}{}

	// Use type assertion to extract fields from the SQLC row type
	// This works because all row types have the same field names
	rv := reflect.ValueOf(t)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}

	for i := 0; i < rv.NumField(); i++ {
		field := rv.Type().Field(i)
		value := rv.Field(i)
		switch field.Name {
		case "ID":
			v.ID = value.Int()
		case "TenantID":
			v.TenantID = value.Interface().(uuid.UUID)
		case "Name":
			v.Name = value.String()
		case "Description":
			v.Description = value.String()
		case "InputSchema":
			v.InputSchema = value.Interface().(json.RawMessage)
		case "Grants":
			v.Grants = value.Interface().(json.RawMessage)
		case "ExecutionTimeoutMs":
			v.ExecutionTimeoutMs = value.Int()
		case "MemoryPages":
			v.MemoryPages = int32(value.Int())
		case "Hash":
			v.Hash = value.String()
		case "IsActive":
			v.IsActive = value.Bool()
		case "CreatedAt":
			v.CreatedAt = value.Interface().(time.Time)
		case "UpdatedAt":
			v.UpdatedAt = value.Interface().(time.Time)
		}
	}

	return &tool.ToolDefinition{
		ID:                v.ID,
		TenantID:          domain.UUID(v.TenantID),
		Name:              v.Name,
		Description:       v.Description,
		InputSchema:       v.InputSchema,
		Grants:            v.Grants,
		ExecutionTimeoutMs: uint64(v.ExecutionTimeoutMs),
		MemoryPages:       uint32(v.MemoryPages),
		Hash:              v.Hash,
		IsActive:          v.IsActive,
		CreatedAt:         v.CreatedAt,
		UpdatedAt:         v.UpdatedAt,
	}
}

// jsonEqual compares two json.RawMessage for semantic equality
func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	var va, vb interface{}
	if err := json.Unmarshal(a, &va); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return false
	}
	return jsonEqualValue(va, vb)
}

func jsonEqualValue(a, b interface{}) bool {
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !jsonEqualValue(v, bv[k]) {
				return false
			}
		}
		return true
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok || len(av) != len(bv) {
			return false
		}
		for i, v := range av {
			if !jsonEqualValue(v, bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// toLower converts string to lowercase
func toLower(s string) string {
	// Simple lowercase for audit action names
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}

// toolDefinitionAuditPayload represents the audit payload for tool definition operations
type toolDefinitionAuditPayload struct {
	Operation      string          `json:"operation"`
	ToolName       string          `json:"tool_name"`
	OldHash        *string         `json:"old_hash,omitempty"`
	NewHash        *string         `json:"new_hash,omitempty"`
	ChangedFields  []string        `json:"changed_fields,omitempty"`
	FullDefinition json.RawMessage `json:"full_definition,omitempty"`
}