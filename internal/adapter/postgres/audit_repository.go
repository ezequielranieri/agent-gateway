package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/pgtype"

	postgressqlc "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres/sqlc"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// AuditFilter represents filter options for querying audit events
type AuditFilter struct {
	TenantID   domain.UUID
	ActorID    *domain.UUID
	Action     string
	EntityType string
	EntityID   *domain.UUID
	Severity   string
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// VerifyResult represents the result of chain verification
type VerifyResult struct {
	Valid      bool
	BrokenSeq  int64
	TotalSeen  int64
	Error      error
}

// AuditRepository implements the audit repository using SQLC
type AuditRepository struct {
	queries *postgressqlc.Queries
	pool    *pgxpool.Pool
}

// NewAuditRepository creates a new audit repository
func NewAuditRepository(pool *pgxpool.Pool) *AuditRepository {
	return &AuditRepository{
		queries: postgressqlc.New(pool),
		pool:    pool,
	}
}

// canonicalizePayload ensures JSON is canonicalized (sorted keys, no whitespace)
func canonicalizePayload(payload json.RawMessage) (json.RawMessage, error) {
	if len(payload) == 0 {
		return json.RawMessage("{}"), nil
	}
	var v interface{}
	if err := json.Unmarshal(payload, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// computeChainInput builds the chain input string per spec
func computeChainInput(prevHash string, seq int64, tenantID domain.UUID, actorID *domain.UUID, action, entityType string, entityID *domain.UUID, payload json.RawMessage, createdAt time.Time) string {
	actor := ""
	if actorID != nil {
		actor = actorID.String()
	}
	entity := ""
	if entityID != nil {
		entity = entityID.String()
	}
	// Truncate to microsecond precision
	created := createdAt.Truncate(time.Microsecond).Format(time.RFC3339Nano)
	return prevHash + "|" +
		strconv.FormatInt(seq, 10) + "|" +
		tenantID.String() + "|" +
		actor + "|" +
		action + "|" +
		entityType + "|" +
		entity + "|" +
		string(payload) + "|" +
		created
}

// Append adds an audit event with hash chaining
// Runs inside WithTenant (tenant-bound transaction)
// Retries on UNIQUE(tenant_id, seq) conflict with bounded backoff
func (r *AuditRepository) Append(ctx context.Context, event *domain.AuditEvent) error {
	return WithTenant(ctx, r.pool, event.TenantID, func(ctx context.Context) error {
		return r.appendWithRetry(ctx, event, 3)
	})
}

// appendWithRetry attempts to append with retries on unique constraint violation
func (r *AuditRepository) appendWithRetry(ctx context.Context, event *domain.AuditEvent, maxRetries int) error {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		err := r.doAppend(ctx, event)
		if err == nil {
			return nil
		}

		// Check if it's a unique constraint violation on (tenant_id, seq)
		if isUniqueConstraintViolation(err) && attempt < maxRetries {
			// Exponential backoff: 10ms, 20ms, 40ms...
			time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
			continue
		}

		return err
	}
	return fmt.Errorf("max retries exceeded for audit append")
}

// isUniqueConstraintViolation checks if the error is a unique constraint violation on (tenant_id, seq)
func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return contains(errStr, "uq_audit_events_tenant_seq") ||
		contains(errStr, "duplicate key value violates unique constraint") ||
		contains(errStr, "UNIQUE constraint failed")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// doAppend performs the actual append operation
func (r *AuditRepository) doAppend(ctx context.Context, event *domain.AuditEvent) error {
	// Get the last event for this tenant to compute prev_hash and seq
	lastEvent, err := r.GetLastEvent(ctx, event.TenantID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to get last event: %w", err)
	}

	var prevHash string
	var seq int64 = 1
	genesisHash := "0000000000000000000000000000000000000000000000000000000000000000"

	if lastEvent != nil {
		prevHash = lastEvent.ChainHash
		seq = lastEvent.Seq + 1
	} else {
		prevHash = genesisHash
	}

	// Canonicalize payload
	canonicalPayload, err := canonicalizePayload(event.Payload)
	if err != nil {
		return fmt.Errorf("failed to canonicalize payload: %w", err)
	}

	// Compute chain input and hash
	chainInput := computeChainInput(prevHash, seq, event.TenantID, event.ActorUserID, event.Action, event.EntityType, event.EntityID, canonicalPayload, event.CreatedAt)
	hashBytes := sha256.Sum256([]byte(chainInput))

	// Prepare actor ID
	var actorID pgtype.UUID
	if event.ActorUserID != nil {
		actorID = pgtype.UUID{Bytes: uuid.UUID(*event.ActorUserID), Valid: true}
	}

	// Prepare entity ID
	var entityID pgtype.UUID
	if event.EntityID != nil {
		entityID = pgtype.UUID{Bytes: uuid.UUID(*event.EntityID), Valid: true}
	}

	// Prepare entity type
	var entityType pgtype.Text
	if event.EntityType != "" {
		entityType = pgtype.Text{String: event.EntityType, Valid: true}
	}

	// Insert the audit event
	createParams := postgressqlc.CreateAuditEventParams{
		TenantID:   uuid.UUID(event.TenantID),
		ActorType:  "user", // Default to user, could be enhanced
		ActorID:    actorID,
		Action:     event.Action,
		EntityType: entityType,
		EntityID:   entityID,
		Payload:    canonicalPayload,
		Severity:   string(event.Severity),
		Hash:       hashBytes[:],
	}

	created, err := r.queries.CreateAuditEvent(ctx, createParams)
	if err != nil {
		return fmt.Errorf("failed to insert audit event: %w", err)
	}

	// Update the event with generated values
	event.ID = domain.UUID(created.ID)
	event.Seq = created.Seq
	event.PrevHash = string(created.PrevHash)
	event.ChainHash = string(created.Hash)
	event.Payload = canonicalPayload
	event.CreatedAt = created.CreatedAt

	return nil
}

// GetLastEvent retrieves the last audit event for a tenant
func (r *AuditRepository) GetLastEvent(ctx context.Context, tenantID domain.UUID) (*domain.AuditEvent, error) {
	var event *domain.AuditEvent
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		// Get the event with the highest seq for this tenant
		rows, err := r.pool.Query(ctx, `
			SELECT id, tenant_id, seq, actor_type, actor_id, action, entity_type, entity_id, payload, severity, prev_hash, hash, created_at
			FROM public.audit_events
			WHERE tenant_id = $1
			ORDER BY seq DESC
			LIMIT 1
		`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()

		if !rows.Next() {
			return sql.ErrNoRows
		}

		var i postgressqlc.AuditEvent
		if err := rows.Scan(
			&i.ID, &i.TenantID, &i.Seq, &i.ActorType, &i.ActorID,
			&i.Action, &i.EntityType, &i.EntityID, &i.Payload,
			&i.Severity, &i.PrevHash, &i.Hash, &i.CreatedAt,
		); err != nil {
			return err
		}

		event = convertSQLCAuditEvent(i)
		return nil
	})
	return event, err
}

// Query retrieves audit events with filters
func (r *AuditRepository) Query(ctx context.Context, filter AuditFilter) ([]*domain.AuditEvent, error) {
	var events []*domain.AuditEvent
	err := WithTenant(ctx, r.pool, filter.TenantID, func(ctx context.Context) error {
		// Build query dynamically based on filters
		query := `
			SELECT id, tenant_id, seq, actor_type, actor_id, action, entity_type, entity_id, payload, severity, prev_hash, hash, created_at
			FROM public.audit_events
			WHERE tenant_id = $1
		`
		args := []interface{}{uuid.UUID(filter.TenantID)}
		argIdx := 2

		if filter.From != nil {
			query += fmt.Sprintf(" AND created_at >= $%d", argIdx)
			args = append(args, *filter.From)
			argIdx++
		}
		if filter.To != nil {
			query += fmt.Sprintf(" AND created_at <= $%d", argIdx)
			args = append(args, *filter.To)
			argIdx++
		}
		if filter.Action != "" {
			query += fmt.Sprintf(" AND action = $%d", argIdx)
			args = append(args, filter.Action)
			argIdx++
		}
		if filter.EntityType != "" {
			query += fmt.Sprintf(" AND entity_type = $%d", argIdx)
			args = append(args, filter.EntityType)
			argIdx++
		}
		if filter.ActorID != nil {
			query += fmt.Sprintf(" AND actor_id = $%d", argIdx)
			args = append(args, uuid.UUID(*filter.ActorID))
			argIdx++
		}
		if filter.EntityID != nil {
			query += fmt.Sprintf(" AND entity_id = $%d", argIdx)
			args = append(args, uuid.UUID(*filter.EntityID))
			argIdx++
		}
		if filter.Severity != "" {
			query += fmt.Sprintf(" AND severity = $%d", argIdx)
			args = append(args, filter.Severity)
			argIdx++
		}

		query += " ORDER BY created_at DESC"

		if filter.Limit > 0 {
			query += fmt.Sprintf(" LIMIT $%d", argIdx)
			args = append(args, filter.Limit)
			argIdx++
		}
		if filter.Offset > 0 {
			query += fmt.Sprintf(" OFFSET $%d", argIdx)
			args = append(args, filter.Offset)
		}

		rows, err := r.pool.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		events = make([]*domain.AuditEvent, 0)
		for rows.Next() {
			var i postgressqlc.AuditEvent
			if err := rows.Scan(
				&i.ID, &i.TenantID, &i.Seq, &i.ActorType, &i.ActorID,
				&i.Action, &i.EntityType, &i.EntityID, &i.Payload,
				&i.Severity, &i.PrevHash, &i.Hash, &i.CreatedAt,
			); err != nil {
				return err
			}
			events = append(events, convertSQLCAuditEvent(i))
		}
		return rows.Err()
	})
	return events, err
}

// VerifyChain verifies the hash chain for a tenant from fromSeq to toSeq
func (r *AuditRepository) VerifyChain(ctx context.Context, tenantID domain.UUID, fromSeq, toSeq int64) (*VerifyResult, error) {
	var result VerifyResult
	err := WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			SELECT id, tenant_id, seq, actor_type, actor_id, action, entity_type, entity_id, payload, severity, prev_hash, hash, created_at
			FROM public.audit_events
			WHERE tenant_id = $1 AND seq >= $2 AND seq <= $3
			ORDER BY seq ASC
		`
		rows, err := tx.Query(ctx, query, uuid.UUID(tenantID), fromSeq, toSeq)
		if err != nil {
			return err
		}
		defer rows.Close()

		var prevHash string
		first := true

		for rows.Next() {
			var i postgressqlc.AuditEvent
			if err := rows.Scan(
				&i.ID, &i.TenantID, &i.Seq, &i.ActorType, &i.ActorID,
				&i.Action, &i.EntityType, &i.EntityID, &i.Payload,
				&i.Severity, &i.PrevHash, &i.Hash, &i.CreatedAt,
			); err != nil {
				return err
			}

			result.TotalSeen++

			event := convertSQLCAuditEvent(i)

			if first {
				// Genesis event should have prev_hash = 64 zeros
				expectedPrev := "0000000000000000000000000000000000000000000000000000000000000000"
				if string(i.PrevHash) != expectedPrev {
					result.Valid = false
					result.BrokenSeq = event.Seq
					result.Error = fmt.Errorf("genesis event has invalid prev_hash: got %s", string(i.PrevHash))
					return nil
				}
				first = false
			} else {
				// Verify prev_hash matches previous event's chain_hash
				if string(i.PrevHash) != prevHash {
					result.Valid = false
					result.BrokenSeq = event.Seq
					result.Error = fmt.Errorf("broken chain at seq %d: prev_hash mismatch", event.Seq)
					return nil
				}
			}

			// Verify chain_hash
			if !event.VerifyChainInput() {
				result.Valid = false
				result.BrokenSeq = event.Seq
				result.Error = fmt.Errorf("broken chain at seq %d: chain_hash mismatch", event.Seq)
				return nil
			}

			prevHash = event.ChainHash
		}

		if err := rows.Err(); err != nil {
			return err
		}

		result.Valid = true
		return nil
	})
	return &result, err
}

// convertSQLCAuditEvent converts a SQLC AuditEvent to domain AuditEvent
func convertSQLCAuditEvent(e postgressqlc.AuditEvent) *domain.AuditEvent {
	var actorUserID *domain.UUID
	if e.ActorID.Valid {
		id := domain.UUID(e.ActorID.Bytes)
		actorUserID = &id
	}

	var entityID *domain.UUID
	if e.EntityID.Valid {
		id := domain.UUID(e.EntityID.Bytes)
		entityID = &id
	}

	var entityType string
	if e.EntityType.Valid {
		entityType = e.EntityType.String
	}

	return &domain.AuditEvent{
		ID:          domain.UUID(e.ID),
		TenantID:    domain.UUID(e.TenantID),
		Seq:         e.Seq,
		PrevHash:    string(e.PrevHash),
		ChainHash:   string(e.Hash),
		ActorUserID: actorUserID,
		Action:      e.Action,
		EntityType:  entityType,
		EntityID:    entityID,
		Payload:     e.Payload,
		Severity:    domain.AuditSeverity(e.Severity),
		CreatedAt:   e.CreatedAt,
	}
}