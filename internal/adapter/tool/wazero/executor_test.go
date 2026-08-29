package wazero

import (
	"testing"
)

// TODO: Add proper wasm test fixtures using wat2wasm toolchain
// For now, WasmExecutor tests are skipped since creating valid WASM binaries by hand is error-prone
// Proper wasm test fixtures should be created using wat2wasm toolchain

func TestWasmExecutor_Skip(t *testing.T) {
	t.Skip("Skipping WasmExecutor tests - need proper wasm test fixtures")
}