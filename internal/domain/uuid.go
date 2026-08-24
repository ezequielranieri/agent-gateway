package domain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// UUID is a 128-bit identifier compatible with RFC 4122 version 4
type UUID [16]byte

// NewUUID generates a new random UUID (version 4)
func NewUUID() UUID {
	var u UUID
	_, _ = rand.Read(u[:])
	// Set version (4) and variant bits
	u[6] = (u[6] & 0x0f) | 0x40 // version 4
	u[8] = (u[8] & 0x3f) | 0x80 // variant 10
	return u
}

// ParseUUID parses a UUID from its canonical string representation (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx)
func ParseUUID(s string) (UUID, error) {
	var u UUID
	// Remove hyphens for hex decoding
	s = strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(s)
	if err != nil {
		return u, err
	}
	if len(b) != 16 {
		return u, fmt.Errorf("invalid UUID length: %d", len(b))
	}
	copy(u[:], b)
	return u, nil
}

// MustParseUUID parses a UUID or panics
func MustParseUUID(s string) UUID {
	u, err := ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

// String returns the canonical string representation
func (u UUID) String() string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// MarshalJSON implements json.Marshaler interface
func (u UUID) MarshalJSON() ([]byte, error) {
	return json.Marshal(u.String())
}

// UnmarshalJSON implements json.Unmarshaler interface
func (u *UUID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := ParseUUID(s)
	if err != nil {
		return err
	}
	*u = parsed
	return nil
}

// Bytes returns the raw 16 bytes
func (u UUID) Bytes() []byte {
	return u[:]
}

// Equal checks equality
func (u UUID) Equal(other UUID) bool {
	return u == other
}

// IsZero checks if UUID is all zeros
func (u UUID) IsZero() bool {
	return u == UUID{}
}