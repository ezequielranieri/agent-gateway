package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrInvalidHash is returned when the hash format is invalid
var ErrInvalidHash = errors.New("invalid hash format")

// ErrInvalidVersion is returned when the argon2 version is not supported
var ErrInvalidVersion = errors.New("invalid argon2 version")

// Argon2idParams holds the Argon2id parameters
type Argon2idParams struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams returns the OWASP-recommended parameters
func DefaultParams() *Argon2idParams {
	return &Argon2idParams{
		Memory:      65536, // 64 MiB
		Iterations:  1,
		Parallelism: 4,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// HashPassword hashes a password using Argon2id and returns a PHC string
func HashPassword(password string, params *Argon2idParams) (string, error) {
	if params == nil {
		params = DefaultParams()
	}

	// Generate random salt
	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	// Hash the password
	hash := argon2.IDKey(
		[]byte(password),
		salt,
		params.Iterations,
		params.Memory,
		params.Parallelism,
		params.KeyLength,
	)

	// Encode in PHC format: $argon2id$v=19$m=65536,t=1,p=4$salt$hash
	phcHash := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		params.Memory,
		params.Iterations,
		params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)

	return phcHash, nil
}

// VerifyPassword verifies a password against a PHC hash
func VerifyPassword(password, phcHash string) error {
	// Parse the PHC string
	params, salt, hash, err := decodePHC(phcHash)
	if err != nil {
		return err
	}

	// Compute hash with the same parameters
	computedHash := argon2.IDKey(
		[]byte(password),
		salt,
		params.Iterations,
		params.Memory,
		params.Parallelism,
		uint32(len(hash)),
	)

	// Compare using constant-time comparison
	if subtle.ConstantTimeCompare(hash, computedHash) == 1 {
		return nil
	}
	return errors.New("invalid password")
}

// decodePHC decodes a PHC string into parameters, salt, and hash
func decodePHC(phc string) (*Argon2idParams, []byte, []byte, error) {
	// Expected format: $argon2id$v=19$m=65536,t=1,p=4$salt$hash
	parts := strings.Split(phc, "$")
	if len(parts) != 6 {
		return nil, nil, nil, ErrInvalidHash
	}

	// parts[0] = "" (empty before first $)
	// parts[1] = "argon2id"
	// parts[2] = "v=19"
	// parts[3] = "m=65536,t=1,p=4"
	// parts[4] = salt (base64)
	// parts[5] = hash (base64)

	if parts[1] != "argon2id" {
		return nil, nil, nil, ErrInvalidHash
	}

	// Check version
	if parts[2] != "v=19" {
		return nil, nil, nil, ErrInvalidVersion
	}

	// Parse parameters
	paramParts := strings.Split(parts[3], ",")
	if len(paramParts) != 3 {
		return nil, nil, nil, ErrInvalidHash
	}

	var memory, iterations uint32
	var parallelism uint8

	for _, p := range paramParts {
		kv := strings.Split(p, "=")
		if len(kv) != 2 {
			return nil, nil, nil, ErrInvalidHash
		}
		switch kv[0] {
		case "m":
			fmt.Sscanf(kv[1], "%d", &memory)
		case "t":
			fmt.Sscanf(kv[1], "%d", &iterations)
		case "p":
			fmt.Sscanf(kv[1], "%d", &parallelism)
		}
	}

	// Decode salt and hash
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	return &Argon2idParams{
		Memory:      memory,
		Iterations:  iterations,
		Parallelism: parallelism,
		SaltLength:  uint32(len(salt)),
		KeyLength:   uint32(len(hash)),
	}, salt, hash, nil
}