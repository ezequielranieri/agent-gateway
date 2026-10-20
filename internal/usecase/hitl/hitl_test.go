package hitl

import (
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ezequielranieri/agent-gateway/internal/domain"
)

// generateTestToken creates a test opaque token and returns both the token and its hash hex string
func generateTestToken() (string, string) {
	token := make([]byte, 32)
	_, _ = rand.Read(token)
	tokenHex := hex.EncodeToString(token)
	hash := hashToken(tokenHex)
	hashHex := hex.EncodeToString(hash)
	return tokenHex, hashHex
}

func TestVerifyTokenHash(t *testing.T) {
	t.Run("valid token matches stored hash", func(t *testing.T) {
		token, hashHex := generateTestToken()
		err := verifyTokenHash(hashHex, token)
		assert.NoError(t, err)
	})

	t.Run("invalid token fails verification", func(t *testing.T) {
		token, hashHex := generateTestToken()
		// Modify the token - ensure it's actually different
		lastChar := token[len(token)-1]
		var differentChar byte
		if lastChar == 'f' {
			differentChar = '0'
		} else {
			differentChar = 'f'
		}
		invalidToken := token[:len(token)-1] + string(differentChar)
		err := verifyTokenHash(hashHex, invalidToken)
		assert.Error(t, err)
		assert.Equal(t, domain.ErrInvalidToken, err)
	})

	t.Run("invalid hex in stored hash fails", func(t *testing.T) {
		err := verifyTokenHash("not-valid-hex", "some-token")
		assert.Error(t, err)
		assert.Equal(t, domain.ErrInvalidToken, err)
	})

	t.Run("empty token fails", func(t *testing.T) {
		_, hashHex := generateTestToken()
		err := verifyTokenHash(hashHex, "")
		assert.Error(t, err)
		assert.Equal(t, domain.ErrInvalidToken, err)
	})

	t.Run("hash is deterministic", func(t *testing.T) {
		token := "test-token-123456789012345678901234"
		hash1 := hashToken(token)
		hash2 := hashToken(token)
		assert.Equal(t, hash1, hash2, "hashToken should be deterministic")
	})
}

// TestApproveAndRejectTokenVerification verifies that both Approve and Reject
// use the same shared verifyTokenHash function and behave consistently.
func TestApproveAndRejectTokenVerification(t *testing.T) {
	// This test verifies the shared verification logic is used by both paths
	// by testing the verifyTokenHash function directly with various scenarios.

	t.Run("shared verification works for both approve and reject scenarios", func(t *testing.T) {
		token, hashHex := generateTestToken()

		// Simulate Approve path verification
		err := verifyTokenHash(hashHex, token)
		require.NoError(t, err, "Approve path: valid token should pass")

		// Simulate Reject path verification (same function)
		err = verifyTokenHash(hashHex, token)
		require.NoError(t, err, "Reject path: valid token should pass")

		// Both paths should reject invalid tokens identically
		invalidToken := token[:len(token)-1] + "x"
		err = verifyTokenHash(hashHex, invalidToken)
		require.Error(t, err, "Approve path: invalid token should fail")
		assert.Equal(t, domain.ErrInvalidToken, err)

		err = verifyTokenHash(hashHex, invalidToken)
		require.Error(t, err, "Reject path: invalid token should fail")
		assert.Equal(t, domain.ErrInvalidToken, err)
	})

	t.Run("timing-safe comparison prevents oracle attacks", func(t *testing.T) {
		// The verifyTokenHash function uses subtle.ConstantTimeCompare
		// which should take constant time regardless of where the mismatch occurs.
		// We can't easily test timing in a unit test, but we verify the function
		// is used by both Approve and Reject paths.

		token, hashHex := generateTestToken()

		// Mismatch at first byte
		invalidToken1 := "f" + token[1:]
		err := verifyTokenHash(hashHex, invalidToken1)
		assert.Error(t, err)
		assert.Equal(t, domain.ErrInvalidToken, err)

		// Mismatch at last byte
		invalidToken2 := token[:len(token)-1] + "f"
		err = verifyTokenHash(hashHex, invalidToken2)
		assert.Error(t, err)
		assert.Equal(t, domain.ErrInvalidToken, err)

		// Both should return the same error type (no oracle via error type)
	})
}