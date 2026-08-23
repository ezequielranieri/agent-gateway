package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

// AuditSeverity represents the severity of an audit event
type AuditSeverity string

const (
	AuditSeverityInfo     AuditSeverity = "info"
	AuditSeverityWarn     AuditSeverity = "warn"
	AuditSeverityCritical AuditSeverity = "critical"
)

// AuditEvent represents an immutable audit log entry
type AuditEvent struct {
	ID            UUID           `json:"id"`
	TenantID      UUID           `json:"tenant_id"`
	Seq           int64          `json:"seq"`
	PrevHash      string         `json:"prev_hash"`
	ChainHash     string         `json:"chain_hash"`
	ActorUserID   *UUID          `json:"actor_user_id,omitempty"`
	Action        string         `json:"action"`
	EntityType    string         `json:"entity_type"`
	EntityID      *UUID          `json:"entity_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
	Severity      AuditSeverity  `json:"severity"`
	CreatedAt     time.Time      `json:"created_at"`
}

// ChainInput returns the canonical input for hash chaining
func (a *AuditEvent) ChainInput() string {
	actor := ""
	if a.ActorUserID != nil {
		actor = a.ActorUserID.String()
	}
	entity := ""
	if a.EntityID != nil {
		entity = a.EntityID.String()
	}
	payload := "{}"
	if len(a.Payload) > 0 {
		// Canonicalize: decode and re-marshal to ensure consistent ordering
		var v interface{}
		if err := json.Unmarshal(a.Payload, &v); err == nil {
			if b, err := json.Marshal(v); err == nil {
				payload = string(b)
			}
		}
	}
	// prev_hash|seq|tenant_id|actor_user_id|action|entity_type|entity_id|payload|created_at
	// created_at truncated to microsecond precision
	created := a.CreatedAt.Truncate(time.Microsecond).Format(time.RFC3339Nano)
	return a.PrevHash + "|" +
		strconv.FormatInt(a.Seq, 10) + "|" +
		a.TenantID.String() + "|" +
		actor + "|" +
		a.Action + "|" +
		a.EntityType + "|"+
		entity + "|" +
		payload + "|" +
		created
}

// ComputeChainHash computes the SHA256 hash of the chain input
func (a *AuditEvent) ComputeChainHash() string {
	input := a.ChainInput()
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

// VerifyChainInput verifies that the event's chain_hash matches the computed hash
func (a *AuditEvent) VerifyChainInput() bool {
	expected := a.ComputeChainHash()
	return a.ChainHash == expected
}