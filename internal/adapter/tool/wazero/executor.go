package wazero

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ezequielranieri/agent-gateway/internal/domain/tool"
	"github.com/rs/zerolog"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

var (
	// ErrModuleNotFound is returned when a WASM module file is not found
	ErrModuleNotFound = errors.New("wasm module not found")

	// ErrModuleInstantiationFailed is returned when module instantiation fails
	ErrModuleInstantiationFailed = errors.New("module instantiation failed")

	// ErrExecutionFailed is returned when tool execution fails
	ErrExecutionFailed = errors.New("execution failed")
)

// WasmExecutor implements tool.ToolExecutor using wazero WebAssembly runtime.
type WasmExecutor struct {
	config   tool.ToolConfig
	modules  map[string]*compiledModule
	logger   zerolog.Logger
	runtime  wazero.Runtime
}

type compiledModule struct {
	config      tool.ToolModuleConfig
	compiledMod wazero.CompiledModule
}

// NewWasmExecutor creates a new WebAssembly-based tool executor.
func NewWasmExecutor(cfg tool.ToolConfig, logger zerolog.Logger) (*WasmExecutor, error) {
	// Create wazero runtime
	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)

	// Instantiate WASI preview1
	wasi_snapshot_preview1.MustInstantiate(ctx, runtime)

	executor := &WasmExecutor{
		config:  cfg,
		modules: make(map[string]*compiledModule),
		logger:  logger.With().Str("component", "wasm_executor").Logger(),
		runtime: runtime,
	}

	// Load and compile all configured modules
	for _, tc := range cfg.Tools {
		wasmBytes, err := os.ReadFile(tc.ModulePath)
		if err != nil {
			return nil, fmt.Errorf("read wasm module %s: %w", tc.ModulePath, err)
		}

		compiled, err := runtime.CompileModule(context.Background(), wasmBytes)
		if err != nil {
			return nil, fmt.Errorf("compile wasm module %s: %w", tc.ModulePath, err)
		}

		executor.modules[tc.Name] = &compiledModule{
			config:      tc,
			compiledMod: compiled,
		}
		executor.logger.Info().
			Str("tool", tc.Name).
			Str("module", tc.ModulePath).
			Int("size", len(wasmBytes)).
			Msg("WASM module compiled and loaded")
	}

	return executor, nil
}

// Name returns the executor identifier.
func (w *WasmExecutor) Name() string {
	return "wazero"
}

// SupportsTool returns true if this executor has the tool registered.
func (w *WasmExecutor) SupportsTool(name string) bool {
	_, ok := w.modules[name]
	return ok
}

// Execute runs a tool call using WebAssembly.
func (w *WasmExecutor) Execute(ctx context.Context, call tool.ToolCall) (tool.ToolResult, error) {
	startTime := time.Now()

	// 1. Lookup tool config
	module, ok := w.modules[call.Name]
	if !ok {
		w.logger.Warn().Str("tool", call.Name).Msg("Tool not found in registry")
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    tool.ErrToolNotFound.Error(),
			Duration: time.Since(startTime),
		}, tool.ErrToolNotFound
	}

	// 2. Build module configuration
	timeout := w.config.EffectiveTimeout(&module.config)

	// Create execution context with timeout
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 3. Configure module
	moduleConfig := wazero.NewModuleConfig().
		WithName(module.config.Name).
		WithSysNanotime().
		WithSysNanosleep()

	// Configure FS mounts (read-only)
	for _, mount := range module.config.Grants.FSReadOnlyMounts {
		hostPath := mount.HostPath
		if !filepath.IsAbs(hostPath) {
			hostPath = filepath.Join(".", hostPath)
		}
		moduleConfig = moduleConfig.WithFSConfig(
			wazero.NewFSConfig().WithDirMount(hostPath, mount.GuestPath),
		)
	}

	// Network access (opt-in)
	if module.config.Grants.AllowNetwork {
		moduleConfig = moduleConfig.
			WithSysNanosleep().
			WithSysWalltime()
	}

	// 4. Instantiate module per-execution (no cross-tenant caching)
	mod, err := w.runtime.InstantiateModule(execCtx, module.compiledMod, moduleConfig)
	if err != nil {
		w.logger.Error().
			Err(err).
			Str("tool", module.config.Name).
			Msg("Module instantiation failed")
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    fmt.Sprintf("module instantiation failed: %v", err),
			Duration: time.Since(startTime),
		}, ErrModuleInstantiationFailed
	}
	defer mod.Close(execCtx)

	// 5. Call the tool's execute function
	executeFn := mod.ExportedFunction("execute")
	if executeFn == nil {
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    "execute function not exported",
			Duration: time.Since(startTime),
		}, ErrExecutionFailed
	}

	// Call execute function (no params for now)
	_, callErr := executeFn.Call(execCtx)
	if callErr != nil {
		w.logger.Error().
			Err(callErr).
			Str("tool", module.config.Name).
			Msg("Tool execution failed")
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    fmt.Sprintf("execution failed: %v", callErr),
			Duration: time.Since(startTime),
		}, ErrExecutionFailed
	}

	return tool.ToolResult{
		CallID:   call.ID,
		Output:   map[string]any{"status": "ok"},
		Duration: time.Since(startTime),
	}, nil
}

// Close closes the executor and releases resources
func (w *WasmExecutor) Close(ctx context.Context) error {
	if w.runtime != nil {
		return w.runtime.Close(ctx)
	}
	return nil
}