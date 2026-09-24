package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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
	Valid     bool
	BrokenSeq int64
	TotalSeen int64
	Error     error
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

// Append adds an audit event with hash chaining.
// Runs inside WithTenantTx (tenant-bound transaction) and delegates to AppendWithTx.
// The row-level lock per tenant is acquired inside AppendWithTx.
func (r *AuditRepository) Append(ctx context.Context, event *domain.AuditEvent) error {
	return WithTenantTx(ctx, r.pool, event.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return r.AppendWithTx(ctx, tx, event)
	})
}

// contains is a simple substring check used for error classification
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

// GetLastEvent retrieves the last audit event for a tenant
func (r *AuditRepository) GetLastEvent(ctx context.Context, tenantID domain.UUID) (*domain.AuditEvent, error) {
	var event *domain.AuditEvent
	err := WithTenantTx(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// Get the event with the highest seq for this tenant
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, seq, actor_type, actor_id, action, entity_type, entity_id, payload, severity, prev_hash, hash, created_at
			FROM public.audit_events
			WHERE tenant_id = $1
			ORDER BY seq DESC
			LIMIT 1
		`, uuid.UUID(tenantID))
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
	err := WithTenantTx(ctx, r.pool, filter.TenantID, func(ctx context.Context, tx pgx.Tx) error {
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
			argIdx++
		}

		rows, err := tx.Query(ctx, query, args...)
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

// AppendWithTx adds an audit event with hash chaining within an existing transaction.
// This is used for atomic audit emission alongside the primary operation (e.g., tool definition write).
// The transaction must already have the tenant GUC set via WithTenantTx.
// Acquires row-level lock per tenant via SELECT ... FOR UPDATE on audit_chain_locks.
func (r *AuditRepository) AppendWithTx(ctx context.Context, tx pgx.Tx, event *domain.AuditEvent) error {
	// Insert lock row if not exists (to ensure row for SELECT FOR UPDATE)
	// Acquire row-level lock for this tenant (held until transaction end)
	_, err := tx.Exec(ctx, `
			INSERT INTO public.audit_chain_locks (tenant_id)
			VALUES ($1)
			ON CONFLICT (tenant_id) DO NOTHING
			`, event.TenantID)
	if err != nil {
		return fmt.Errorf("failed to ensure lock row: %w", err)
	}

	// Acquire tenant lock to serialize chain appends for this tenant.
	// SELECT ... FOR UPDATE acquires a row-level lock that is held until transaction end.
	var dummy int64
	err = tx.QueryRow(ctx, `SELECT 1 FROM public.audit_chain_locks WHERE tenant_id = $1 FOR UPDATE`, event.TenantID).Scan(&dummy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lock row missing for tenant %s: %w", event.TenantID, err)
		}
		return fmt.Errorf("failed to acquire tenant lock: %w", err)
	}

	// Set CreatedAt to current UTC time truncated to microsecond for hash input
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	event.CreatedAt = createdAt
	// Get the last event for this tenant to compute prev_hash and seq
	lastEvent, err := r.getLastEventTx(ctx, tx, event.TenantID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
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
	chainInput := computeChainInput(prevHash, seq, event.TenantID, event.ActorUserID, event.Action, event.EntityType, event.EntityID, canonicalPayload, createdAt)
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

	// Insert the audit event using the transaction
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
		CreatedAt:  createdAt,
	}

	// Use transaction-bound queries so RLS GUC is active on this connection
	q := r.queries.WithTx(tx)
	created, err := q.CreateAuditEvent(ctx, createParams)
	if err != nil {
		return fmt.Errorf("failed to insert audit event: %w", err)
	}

	// Validate that returned values match what we computed
	if created.Seq != seq {
		return fmt.Errorf("sequence mismatch: expected %d, got %d", seq, created.Seq)
	}
	if hex.EncodeToString(created.PrevHash) != prevHash {
		return fmt.Errorf("prev_hash mismatch: expected %s, got %s", prevHash, hex.EncodeToString(created.PrevHash))
	}
	createdAtTrunc := created.CreatedAt.UTC().Truncate(time.Microsecond)
	if !createdAtTrunc.Equal(createdAt) {
		return fmt.Errorf("created_at mismatch: expected %v, got %v", createdAt, createdAtTrunc)
	}

	// Update the event with generated values
	event.ID = domain.UUID(created.ID)
	event.Seq = created.Seq
	event.PrevHash = hex.EncodeToString(created.PrevHash)
	event.ChainHash = hex.EncodeToString(created.Hash)
	event.Payload = canonicalPayload
	event.CreatedAt = created.CreatedAt

	return nil
}

// getLastEventTx retrieves the last audit event for a tenant using an existing transaction
func (r *AuditRepository) getLastEventTx(ctx context.Context, tx pgx.Tx, tenantID domain.UUID) (*domain.AuditEvent, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, seq, actor_type, actor_id, action, entity_type, entity_id, payload, severity, prev_hash, hash, created_at
		FROM public.audit_events
		WHERE tenant_id = $1
		ORDER BY seq DESC
		LIMIT 1
	`, uuid.UUID(tenantID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, pgx.ErrNoRows
	}

	var i postgressqlc.AuditEvent
	if err := rows.Scan(
		&i.ID, &i.TenantID, &i.Seq, &i.ActorType, &i.ActorID,
		&i.Action, &i.EntityType, &i.EntityID, &i.Payload,
		&i.Severity, &i.PrevHash, &i.Hash, &i.CreatedAt,
	); err != nil {
		return nil, err
	}

	return convertSQLCAuditEvent(i), nil
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

		var prevHash []byte
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
				// Genesis event should have prev_hash = 32 zero bytes (SHA-256 size)
				expectedPrev := make([]byte, 32)
				if !bytes.Equal(i.PrevHash, expectedPrev) {
					result.Valid = false
					result.BrokenSeq = event.Seq
					result.Error = fmt.Errorf("genesis event has invalid prev_hash: got %x", i.PrevHash)
					return nil
				}
				first = false
			} else {
				// Verify prev_hash matches previous event's chain_hash
				if !bytes.Equal(i.PrevHash, prevHash) {
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

			prevHash = i.Hash
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
		PrevHash:    hex.EncodeToString(e.PrevHash),
		ChainHash:   hex.EncodeToString(e.Hash),
		ActorUserID: actorUserID,
		Action:      e.Action,
		EntityType:  entityType,
		EntityID:    entityID,
		Payload:     e.Payload,
		Severity:    domain.AuditSeverity(e.Severity),
		CreatedAt:   e.CreatedAt,
	}
}
