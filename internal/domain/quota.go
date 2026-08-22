package domain

import (
	"time"
)

// QuotaScope represents the scope of a quota
type QuotaScope string

const (
	QuotaScopeTenant QuotaScope = "tenant"
	QuotaScopeUser   QuotaScope = "user"
	QuotaScopeRole   QuotaScope = "role"
)

// Quota represents rate limits for a scope
type Quota struct {
	ID             UUID        `json:"id"`
	TenantID       UUID        `json:"tenant_id"`
	Scope          QuotaScope  `json:"scope"`
	ScopeID        UUID        `json:"scope_id"` // user_id or role_id when scope is user/role
	RequestsPerMin int         `json:"requests_per_min"`
	TokensPerMin   int         `json:"tokens_per_min"`
	ToolExecsPerMin int        `json:"tool_execs_per_min"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// DefaultQuotas returns the default quota configuration
func DefaultQuotas() Quota {
	return Quota{
		RequestsPerMin:  60,
		TokensPerMin:    10000,
		ToolExecsPerMin: 30,
	}
}