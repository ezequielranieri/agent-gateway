package postgres

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	postgressqlc "github.com/ezequielranieri/agent-gateway/internal/adapter/postgres/sqlc"
	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// UserRepository implements the user repository using SQLC
type UserRepository struct {
	queries *postgressqlc.Queries
	pool    *pgxpool.Pool
}

// NewUserRepository creates a new user repository
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{
		queries: postgressqlc.New(pool),
		pool:    pool,
	}
}

// Create creates a new user
func (r *UserRepository) Create(ctx context.Context, tenantID domain.UUID, user *domain.User) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.CreateUserParams{
			TenantID:      uuid.UUID(tenantID),
			Email:         user.Email,
			PasswordHash:  user.PasswordHash,
		}
		result, err := r.queries.CreateUser(ctx, params)
		if err != nil {
			return err
		}
		user.ID = domain.UUID(result.ID)
		user.TenantID = domain.UUID(result.TenantID)
		user.CreatedAt = result.CreatedAt
		user.UpdatedAt = result.UpdatedAt
		return nil
	})
}

// GetByEmail retrieves a user by email within a tenant
func (r *UserRepository) GetByEmail(ctx context.Context, tenantID domain.UUID, email string) (*domain.User, error) {
	var user *domain.User
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.GetUserByEmailParams{
			TenantID: uuid.UUID(tenantID),
			Email:    email,
		}
		result, err := r.queries.GetUserByEmail(ctx, params)
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.ErrNotFound
			}
			return err
		}
		user = &domain.User{
			ID:           domain.UUID(result.ID),
			TenantID:     domain.UUID(result.TenantID),
			Email:        result.Email,
			PasswordHash: result.PasswordHash,
			Status:       domain.UserStatus(result.Status),
			CreatedAt:    result.CreatedAt,
			UpdatedAt:    result.UpdatedAt,
		}
		return nil
	})
	return user, err
}

// GetByID retrieves a user by ID within a tenant
func (r *UserRepository) GetByID(ctx context.Context, tenantID domain.UUID, id domain.UUID) (*domain.User, error) {
	var user *domain.User
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.GetUserByIDParams{
			TenantID: uuid.UUID(tenantID),
			ID:       uuid.UUID(id),
		}
		result, err := r.queries.GetUserByID(ctx, params)
		if err != nil {
			if err == sql.ErrNoRows {
				return domain.ErrNotFound
			}
			return err
		}
		user = &domain.User{
			ID:           domain.UUID(result.ID),
			TenantID:     domain.UUID(result.TenantID),
			Email:        result.Email,
			PasswordHash: result.PasswordHash,
			Status:       domain.UserStatus(result.Status),
			CreatedAt:    result.CreatedAt,
			UpdatedAt:    result.UpdatedAt,
		}
		return nil
	})
	return user, err
}

// Update updates a user
func (r *UserRepository) Update(ctx context.Context, tenantID domain.UUID, user *domain.User) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.UpdateUserParams{
			TenantID:      uuid.UUID(tenantID),
			ID:            uuid.UUID(user.ID),
			Email:         user.Email,
			PasswordHash:  user.PasswordHash,
			Status:        string(user.Status),
		}
		result, err := r.queries.UpdateUser(ctx, params)
		if err != nil {
			return err
		}
		user.UpdatedAt = result.UpdatedAt
		return nil
	})
}

// ListSessions lists active sessions (refresh tokens) for a user
func (r *UserRepository) ListSessions(ctx context.Context, tenantID domain.UUID, userID domain.UUID) ([]domain.Session, error) {
	var sessions []domain.Session
	err := WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.ListUserSessionsParams{
			UserID:   uuid.UUID(userID),
			TenantID: uuid.UUID(tenantID),
		}
		results, err := r.queries.ListUserSessions(ctx, params)
		if err != nil {
			return err
		}
		sessions = make([]domain.Session, len(results))
		for i, r := range results {
			sessions[i] = domain.Session{
				ID:        domain.UUID(r.ID),
				UserID:    domain.UUID(r.UserID),
				CreatedAt: r.CreatedAt,
			}
			if r.LastUsedAt.Valid {
				sessions[i].LastUsedAt = sql.NullTime{Time: r.LastUsedAt.Time, Valid: true}
			}
			if r.UserAgent.Valid {
				sessions[i].UserAgent = sql.NullString{String: r.UserAgent.String, Valid: true}
			}
			if r.Ip != nil {
				sessions[i].IP = r.Ip.String()
			}
		}
		return nil
	})
	return sessions, err
}

// RevokeAllSessions revokes all sessions for a user
func (r *UserRepository) RevokeAllSessions(ctx context.Context, tenantID domain.UUID, userID domain.UUID) error {
	return WithTenant(ctx, r.pool, tenantID, func(ctx context.Context) error {
		params := postgressqlc.RevokeAllUserSessionsParams{
			UserID:   uuid.UUID(userID),
			TenantID: uuid.UUID(tenantID),
		}
		return r.queries.RevokeAllUserSessions(ctx, params)
	})
}

// ConvertSQLCUser converts a SQLC user to domain user
func ConvertSQLCUser(u postgressqlc.User) *domain.User {
	return &domain.User{
		ID:           domain.UUID(u.ID),
		TenantID:     domain.UUID(u.TenantID),
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Status:       domain.UserStatus(u.Status),
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}
}