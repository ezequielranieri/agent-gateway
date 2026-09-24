package tool

import (
	"encoding/json"
	"testing"
)

// TestComputeHash_Canonicalization tests that ComputeHash produces deterministic hashes
// for canonical JSON (sorted keys, no whitespace).
func TestComputeHash_Canonicalization(t *testing.T) {
	tests := []struct {
		name           string
		nameField      string
		description    string
		parameters     json.RawMessage
		expectedHash   string // Empty means we only test consistency, not a specific value
		shouldMatch    []string // Other test case names that should produce the same hash
		shouldNotMatch []string // Other test case names that should produce different hashes
	}{
		{
			name:        "basic_tool",
			nameField:   "read_file",
			description: "Read a file from the filesystem",
			parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		},
		{
			name:        "same_definition_different_order",
			nameField:   "read_file",
			description: "Read a file from the filesystem",
			parameters:  json.RawMessage(`{"required":["path"],"type":"object","properties":{"path":{"type":"string"}}}`),
			shouldMatch: []string{"basic_tool"},
		},
		{
			name:        "different_name",
			nameField:   "write_file",
			description: "Read a file from the filesystem",
			parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			shouldNotMatch: []string{"basic_tool"},
		},
		{
			name:        "different_description",
			nameField:   "read_file",
			description: "Write a file to the filesystem",
			parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			shouldNotMatch: []string{"basic_tool"},
		},
		{
			name:        "different_parameters",
			nameField:   "read_file",
			description: "Read a file from the filesystem",
			parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"encoding":{"type":"string"}},"required":["path"]}`),
			shouldNotMatch: []string{"basic_tool"},
		},
		{
			name:        "empty_description",
			nameField:   "simple_echo",
			description: "",
			parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			name:        "empty_parameters",
			nameField:   "simple_echo",
			description: "Echo input",
			parameters:  json.RawMessage(`{}`),
		},
		{
			name:        "nested_parameters_reordered",
			nameField:   "complex_tool",
			description: "Complex tool with nested params",
			parameters:  json.RawMessage(`{"type":"object","properties":{"a":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"number"}}},"b":{"type":"string"}},"required":["a"]}`),
			shouldMatch: []string{"nested_parameters_original"},
		},
		{
			name:        "nested_parameters_original",
			nameField:   "complex_tool",
			description: "Complex tool with nested params",
			parameters:  json.RawMessage(`{"type":"object","properties":{"b":{"type":"string"},"a":{"type":"object","properties":{"y":{"type":"number"},"x":{"type":"string"}}}},"required":["a"]}`),
			shouldMatch: []string{"nested_parameters_reordered"},
		},
		{
			name:        "unicode_in_description",
			nameField:   "unicode_tool",
			description: "Herramienta con descripción en español 🚀",
			parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			name:        "special_chars_in_name",
			nameField:   "tool-with-dashes",
			description: "Tool with dashes in name",
			parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}

	// Compute hashes for all test cases
	hashes := make(map[string]string)
	for _, tc := range tests {
		hash := ComputeHash(tc.nameField, tc.description, tc.parameters)
		hashes[tc.name] = hash

		// Verify hash format: 64 lowercase hex characters
		if len(hash) != 64 {
			t.Errorf("%s: hash length = %d, want 64", tc.name, len(hash))
		}
		for _, c := range hash {
			if c < '0' || (c > '9' && c < 'a') || c > 'f' {
				t.Errorf("%s: hash contains non-lowercase-hex char: %c", tc.name, c)
			}
		}
	}

	// Verify matching expectations
	for _, tc := range tests {
		hash := hashes[tc.name]

		// Check shouldMatch
		for _, matchName := range tc.shouldMatch {
			matchHash := hashes[matchName]
			if hash != matchHash {
				t.Errorf("%s: expected hash to match %s, got %s vs %s", tc.name, matchName, hash, matchHash)
			}
		}

		// Check shouldNotMatch
		for _, notMatchName := range tc.shouldNotMatch {
			notMatchHash := hashes[notMatchName]
			if hash == notMatchHash {
				t.Errorf("%s: expected hash to NOT match %s, but both are %s", tc.name, notMatchName, hash)
			}
		}
	}

	// Verify deterministic: same input always produces same hash
	for _, tc := range tests {
		hash1 := ComputeHash(tc.nameField, tc.description, tc.parameters)
		hash2 := ComputeHash(tc.nameField, tc.description, tc.parameters)
		if hash1 != hash2 {
			t.Errorf("%s: non-deterministic hash: %s != %s", tc.name, hash1, hash2)
		}
	}
}

// TestComputeHash_EmptyInputs tests edge cases with empty/nil inputs
func TestComputeHash_EmptyInputs(t *testing.T) {
	// Empty description and empty parameters object
	hash1 := ComputeHash("test", "", json.RawMessage(`{}`))
	hash2 := ComputeHash("test", "", json.RawMessage(`{}`))
	if hash1 != hash2 {
		t.Errorf("empty inputs not deterministic: %s != %s", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("empty inputs hash length: %d", len(hash1))
	}

	// Nil parameters should be treated as empty object
	hash3 := ComputeHash("test", "desc", nil)
	hash4 := ComputeHash("test", "desc", json.RawMessage(`{}`))
	if hash3 != hash4 {
		t.Errorf("nil vs empty object differ: %s != %s", hash3, hash4)
	}
}

// TestComputeHash_WhitespaceInsensitive tests that whitespace in JSON doesn't affect hash
func TestComputeHash_WhitespaceInsensitive(t *testing.T) {
	compact := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	pretty := json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string"
    }
  }
}`)

	hash1 := ComputeHash("test", "desc", compact)
	hash2 := ComputeHash("test", "desc", pretty)
	if hash1 != hash2 {
		t.Errorf("whitespace affects hash: %s != %s", hash1, hash2)
	}
}

// TestComputeHash_Golden tests RFC 8785 canonicalization against hardcoded expected hashes.
// These are GOLDEN VALUES — if they change, the canonicalization behavior has changed,
// which would invalidate all existing tool definition hashes in the registry.
// Update these ONLY if deliberately changing the canonicalization algorithm.
func TestComputeHash_Golden(t *testing.T) {
	golden := []struct {
		name     string
		nameF    string
		desc     string
		params   json.RawMessage
		expected string
	}{
		{
			name:     "float_minimum",
			nameF:    "validate_age",
			desc:     "Validate age",
			params:   json.RawMessage(`{"type":"object","properties":{"age":{"type":"integer","minimum":18.0}}}`),
			expected: "d088f688232b86deed0378e2cd627c332247f4aa8ef33a98d01840f815fa82cb",
		},
		{
			name:     "scientific_notation",
			nameF:    "calc_rate",
			desc:     "Calc rate",
			params:   json.RawMessage(`{"type":"object","properties":{"rate":{"type":"number","maximum":1e3}}}`),
			expected: "3cdc703a6f0768d6f06c7241ed6b23e393f8ea4172e81557d4d707a3d81b6bee",
		},
		{
			name:     "unicode_emoji",
			nameF:    "greet",
			desc:     "Greet user 👋🌍",
			params:   json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
			expected: "f369b3c1ffd8e9ddfd12616895d94d9e0503acb6c097c2fa73e08e53dbaf7f7c",
		},
		{
			name:     "unicode_arabic",
			nameF:    "مرحبا",
			desc:     "أداة باللغة العربية",
			params:   json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
			expected: "eb57dfb63d1d7f3a53c541538265a20ff822fb4420cf9c78671497f93d3bca71",
		},
		{
			name:     "trailing_zeros",
			nameF:    "precision_tool",
			desc:     "Precision tool",
			params:   json.RawMessage(`{"type":"object","properties":{"value":{"type":"number","multipleOf":0.01}}}`),
			expected: "7fc7b237019abe7c5711353731ea853961b1187ec945a9530f08bf8e515f624c",
		},
	}

	for _, tc := range golden {
		hash := ComputeHash(tc.nameF, tc.desc, tc.params)
		if hash != tc.expected {
			t.Errorf("%s: golden hash mismatch\ngot:      %s\nexpected: %s", tc.name, hash, tc.expected)
		}
	}
}

// TestComputeHash_RFC8785Reference tests the canonicalization against the official
// RFC 8785 (JCS) reference examples. This verifies the library implements the
// standard correctly, independent of our golden hashes.
//
// RFC 8785 Section 3.2.2 (Number serialization) specifies:
// Input:  [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001]
// Output: [333333333.3333333,1e+30,4.5,0.002,1e-27]
//
// We test this by embedding the numbers in a tool definition's parameters
// and verifying the hash is deterministic and matches the canonical form.
func TestComputeHash_RFC8785Reference(t *testing.T) {
	// RFC 8785 number serialization test case
	// The canonical JSON for these numbers should be:
	// [333333333.3333333,1e+30,4.5,0.002,1e-27]
	rfcParams := json.RawMessage(`{"type":"array","items":{"type":"number"},"enum":[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001]}`)

	// Compute hash twice - must be deterministic
	hash1 := ComputeHash("rfc8785_numbers", "RFC 8785 number serialization test", rfcParams)
	hash2 := ComputeHash("rfc8785_numbers", "RFC 8785 number serialization test", rfcParams)

	if hash1 != hash2 {
		t.Errorf("RFC 8785 reference: non-deterministic hash: %s != %s", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("RFC 8785 reference: hash length = %d, want 64", len(hash1))
	}

	// Verify the canonical form by checking that equivalent number representations
	// produce the SAME hash (this is the core RFC 8785 requirement)
	equivParams := json.RawMessage(`{"type":"array","items":{"type":"number"},"enum":[333333333.3333333,1e+30,4.5,0.002,1e-27]}`)
	hash3 := ComputeHash("rfc8785_numbers", "RFC 8785 number serialization test", equivParams)

	if hash1 != hash3 {
		t.Errorf("RFC 8785 reference: canonical numbers produce different hash\ngot:      %s\nexpected: %s", hash3, hash1)
	}

	// Verify non-equivalent numbers produce different hash
	diffParams := json.RawMessage(`{"type":"array","items":{"type":"number"},"enum":[333333333.3333333,1e+30,4.5,0.002,1e-26]}`)
	hash4 := ComputeHash("rfc8785_numbers", "RFC 8785 number serialization test", diffParams)

	if hash1 == hash4 {
		t.Errorf("RFC 8785 reference: different last number should produce different hash")
	}

	// Test negative zero handling (RFC 8785: -0.0 serializes as 0)
	negZeroParams := json.RawMessage(`{"type":"object","properties":{"value":{"type":"number","const":-0.0}}}`)
	posZeroParams := json.RawMessage(`{"type":"object","properties":{"value":{"type":"number","const":0.0}}}`)
	hashNeg := ComputeHash("zero_test", "Negative zero test", negZeroParams)
	hashPos := ComputeHash("zero_test", "Negative zero test", posZeroParams)
	if hashNeg != hashPos {
		t.Errorf("RFC 8785 reference: -0.0 and 0.0 should canonicalize to same value")
	}
}