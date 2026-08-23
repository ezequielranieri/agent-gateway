package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	postgressqlc "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres/sqlc"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// RefreshTokenRepository implements the refresh token repository using SQLC
type RefreshTokenRepository struct {
	queries *postgressqlc.Queries
	pool    *pgxpool.Pool
}

// NewRefreshTokenRepository creates a new refresh token repository
func NewRefreshTokenRepository(pool *pgxpool.Pool) *RefreshTokenRepository {
	return &RefreshTokenRepository{
		queries: postgressqlc.New(pool),
		pool:    pool,
	}
}

// HashToken returns the SHA-256 hash of a refresh token
func HashToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

// Store stores a new refresh token
func (r *RefreshTokenRepository) Store(ctx context.Context, token *domain.RefreshToken) error {
	// Parse IP address
	var ip *netip.Addr
	if token.IP != "" {
		parsed, err := netip.ParseAddr(token.IP)
		if err == nil {
			ip = &parsed
		}
	}

	params := postgressqlc.CreateRefreshTokenParams{
		UserID:    uuid.UUID(token.UserID),
		TenantID:  uuid.UUID(token.TenantID),
		TokenHash: token.TokenHash,
		FamilyID:  uuid.UUID(token.FamilyID),
		ExpiresAt: token.ExpiresAt,
		UserAgent: pgtype.Text{String: token.UserAgent, Valid: token.UserAgent != ""},
		Ip:        ip,
	}
	result, err := r.queries.CreateRefreshToken(ctx, params)
	if err != nil {
		return err
	}
	token.ID = domain.UUID(result.ID)
	token.CreatedAt = result.CreatedAt
	return nil
}

// FindByTokenHash finds a refresh token by its hash
func (r *RefreshTokenRepository) FindByTokenHash(ctx context.Context, tokenHash []byte) (*domain.RefreshToken, error) {
	result, err := r.queries.GetRefreshTokenByHash(ctx, tokenHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	ip := ""
	if result.Ip != nil {
		ip = result.Ip.String()
	}
	return &domain.RefreshToken{
		ID:           domain.UUID(result.ID),
		UserID:       domain.UUID(result.UserID),
		TenantID:     domain.UUID(result.TenantID),
		TokenHash:    result.TokenHash,
		FamilyID:     domain.UUID(result.FamilyID),
		Revoked:      result.Revoked,
		ExpiresAt:    result.ExpiresAt,
		UserAgent:    result.UserAgent.String,
		IP:           ip,
		CreatedAt:    result.CreatedAt,
		LastUsedAt:   sql.NullTime{Time: result.LastUsedAt.Time, Valid: result.LastUsedAt.Valid},
		RotatedFrom:  result.RotatedFrom,
	}, nil
}

// Rotate rotates a refresh token: persists new token, revokes old, issues access token
func (r *RefreshTokenRepository) Rotate(ctx context.Context, oldTokenID domain.UUID, newToken *domain.RefreshToken) error {
	// Parse IP address
	var ip *netip.Addr
	if newToken.IP != "" {
		parsed, err := netip.ParseAddr(newToken.IP)
		if err == nil {
			ip = &parsed
		}
	}

	params := postgressqlc.RotateRefreshTokenParams{
		UserID:      uuid.UUID(newToken.UserID),
		TenantID:    uuid.UUID(newToken.TenantID),
		TokenHash:   newToken.TokenHash,
		FamilyID:    uuid.UUID(newToken.FamilyID),
		ExpiresAt:   newToken.ExpiresAt,
		UserAgent:   pgtype.Text{String: newToken.UserAgent, Valid: newToken.UserAgent != ""},
		Ip:          ip,
		RotatedFrom: pgtype.UUID{Bytes: oldTokenID, Valid: true},
	}
	result, err := r.queries.RotateRefreshToken(ctx, params)
	if err != nil {
		return err
	}
	newToken.ID = domain.UUID(result.ID)
	newToken.CreatedAt = result.CreatedAt
	return nil
}

// Revoke revokes a specific refresh token
func (r *RefreshTokenRepository) Revoke(ctx context.Context, tokenID domain.UUID) error {
	return r.queries.RevokeRefreshToken(ctx, uuid.UUID(tokenID))
}

// RevokeFamily revokes all tokens in a family (reuse detection)
func (r *RefreshTokenRepository) RevokeFamily(ctx context.Context, familyID domain.UUID) error {
	return r.queries.RevokeRefreshTokenFamily(ctx, uuid.UUID(familyID))
}

// ListActiveTokens lists active refresh tokens for a user
func (r *RefreshTokenRepository) ListActiveTokens(ctx context.Context, tenantID domain.UUID, userID domain.UUID) ([]domain.RefreshToken, error) {
	params := postgressqlc.ListActiveRefreshTokensParams{
		UserID:   uuid.UUID(userID),
		TenantID: uuid.UUID(tenantID),
	}
	results, err := r.queries.ListActiveRefreshTokens(ctx, params)
	if err != nil {
		return nil, err
	}
	tokens := make([]domain.RefreshToken, len(results))
	for i, t := range results {
		ip := ""
		if t.Ip != nil {
			ip = t.Ip.String()
		}
		tokens[i] = domain.RefreshToken{
			ID:           domain.UUID(t.ID),
			UserID:       domain.UUID(t.UserID),
			TenantID:     domain.UUID(t.TenantID),
			TokenHash:    t.TokenHash,
			FamilyID:     domain.UUID(t.FamilyID),
			Revoked:      t.Revoked,
			ExpiresAt:    t.ExpiresAt,
			UserAgent:    t.UserAgent.String,
			IP:           ip,
			CreatedAt:    t.CreatedAt,
			LastUsedAt:   sql.NullTime{Time: t.LastUsedAt.Time, Valid: t.LastUsedAt.Valid},
			RotatedFrom:  t.RotatedFrom,
		}
	}
	return tokens, nil
}

// UpdateLastUsed updates the last_used_at timestamp
func (r *RefreshTokenRepository) UpdateLastUsed(ctx context.Context, tokenID domain.UUID) error {
	return r.queries.UpdateRefreshTokenLastUsed(ctx, uuid.UUID(tokenID))
}