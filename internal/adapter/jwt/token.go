package jwt

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// ErrInvalidAlgorithm is returned when a non-HS256 algorithm is used
var ErrInvalidAlgorithm = errors.New("invalid algorithm: only HS256 is allowed")

// RefreshClaims represents the claims stored in a refresh token
type RefreshClaims struct {
	UserID    domain.UUID `json:"sub"`
	TenantID  domain.UUID `json:"tenant_id"`
	FamilyID  domain.UUID `json:"family_id"`
	TokenHash string      `json:"token_hash"`
	Revoked   bool        `json:"revoked"`
	ExpiresAt time.Time   `json:"exp"`
	CreatedAt time.Time   `json:"iat"`
}

// TokenService defines the interface for JWT token operations
type TokenService interface {
	IssueAccessToken(claims Claims) (string, error)
	IssueSuperAdminToken(superAdminID domain.UUID) (string, error)
	IssueRefreshToken(userID domain.UUID) (token, tokenHash string, familyID domain.UUID, expiresAt time.Time, err error)
	ParseAccessToken(tokenString string) (*Claims, error)
	VerifyRefreshToken(tokenHash string) (*RefreshClaims, error)
	RotateRefreshToken(oldTokenHash string) (newToken, newTokenHash string, familyID domain.UUID, expiresAt time.Time, err error)
	RevokeRefreshToken(tokenHash string) error
	RevokeFamily(familyID domain.UUID) error
}

// KeyStore holds the current signing key and verification keys for rotation
type KeyStore struct {
	CurrentKID string
	SigningKey []byte
	Keys       map[string][]byte // kid -> key
}

// NewKeyStore creates a new key store with the current key
func NewKeyStore(currentKID string, signingKey []byte, keys map[string][]byte) *KeyStore {
	if keys == nil {
		keys = make(map[string][]byte)
	}
	keys[currentKID] = signingKey
	return &KeyStore{
		CurrentKID: currentKID,
		SigningKey: signingKey,
		Keys:       keys,
	}
}

// RotateKey adds a new key and makes it the current signing key
func (ks *KeyStore) RotateKey(newKID string, newKey []byte) {
	ks.Keys[newKID] = newKey
	ks.CurrentKID = newKID
	ks.SigningKey = newKey
}

// GetSigningKey returns the current signing key and its KID
func (ks *KeyStore) GetSigningKey() (string, []byte) {
	return ks.CurrentKID, ks.SigningKey
}

// GetVerificationKey returns the verification key for a given KID
func (ks *KeyStore) GetVerificationKey(kid string) ([]byte, bool) {
	key, ok := ks.Keys[kid]
	return key, ok
}

// Claims represents the JWT claims for the agent-gateway
type Claims struct {
	UserID   string   `json:"sub"`
	TenantID string   `json:"tenant_id,omitempty"` // Empty for SuperAdmin
	Role     string   `json:"role"`
	Scopes   []string `json:"scopes"`
	jwt.RegisteredClaims
}

// AuthService provides JWT signing and verification
type AuthService struct {
	keystore *KeyStore
	issuer   string
	audience string
}

// NewAuthService creates a new auth service
func NewAuthService(keystore *KeyStore, issuer, audience string) *AuthService {
	return &AuthService{
		keystore: keystore,
		issuer:   issuer,
		audience: audience,
	}
}

// IssueAccessToken issues a new access token (15 min TTL, HS256, kid header)
func (s *AuthService) IssueAccessToken(claims Claims) (string, error) {
	now := time.Now()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    s.issuer,
		Audience:  jwt.ClaimStrings{s.audience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
		NotBefore: jwt.NewNumericDate(now),
	}

	kid, key := s.keystore.GetSigningKey()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = kid

	return token.SignedString(key)
}

// IssueSuperAdminToken issues a new SuperAdmin access token (15 min TTL, no tenant_id)
func (s *AuthService) IssueSuperAdminToken(superAdminID domain.UUID) (string, error) {
	claims := Claims{
		UserID:   superAdminID.String(),
		TenantID: "", // Empty for SuperAdmin
		Role:     "super_admin",
		Scopes:   []string{"*"},
	}
	return s.IssueAccessToken(claims)
}

// ParseAccessToken parses and validates an access token
func (s *AuthService) ParseAccessToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Algorithm pinning: reject non-HS256
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidAlgorithm
		}

		// Get KID from header
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, errors.New("missing kid header")
		}

		// Get verification key
		key, ok := s.keystore.GetVerificationKey(kid)
		if !ok {
			return nil, fmt.Errorf("unknown kid: %s", kid)
		}

		return key, nil
	}, jwt.WithIssuer(s.issuer), jwt.WithAudience(s.audience))

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}

// VerifyHMAC verifies an HMAC signature (for internal use)
func VerifyHMAC(key, data []byte, sig string) bool {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// IssueRefreshToken generates a new opaque refresh token and stores it
func (s *AuthService) IssueRefreshToken(userID domain.UUID) (token, tokenHash string, familyID domain.UUID, expiresAt time.Time, err error) {
	// Generate opaque token (32 random bytes)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", domain.UUID{}, time.Time{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(tokenBytes)

	// Hash token for storage
	hash := sha256.Sum256([]byte(token))
	tokenHash = base64.RawURLEncoding.EncodeToString(hash[:])

	// Generate family ID
	familyID = domain.NewUUID()

	// Expires in 7 days (configurable)
	expiresAt = time.Now().Add(7 * 24 * time.Hour)

	// Note: Actual persistence should be done via RefreshTokenRepository
	// This method only generates the token and hash

	return token, tokenHash, familyID, expiresAt, nil
}

// VerifyRefreshToken parses and validates a refresh token by its hash
func (s *AuthService) VerifyRefreshToken(tokenHash string) (*RefreshClaims, error) {
	// This requires a repository lookup - not implemented in AuthService directly
	return nil, errors.New("VerifyRefreshToken requires repository")
}

// RotateRefreshToken rotates a refresh token (persist new, revoke old)
func (s *AuthService) RotateRefreshToken(oldTokenHash string) (newToken, newTokenHash string, familyID domain.UUID, expiresAt time.Time, err error) {
	// This requires a repository - not implemented in AuthService directly
	return "", "", domain.UUID{}, time.Time{}, errors.New("RotateRefreshToken requires repository")
}

// RevokeRefreshToken revokes a single refresh token
func (s *AuthService) RevokeRefreshToken(tokenHash string) error {
	// This requires a repository - not implemented in AuthService directly
	return errors.New("RevokeRefreshToken requires repository")
}

// RevokeFamily revokes all refresh tokens in a family
func (s *AuthService) RevokeFamily(familyID domain.UUID) error {
	// This requires a repository - not implemented in AuthService directly
	return errors.New("RevokeFamily requires repository")
}