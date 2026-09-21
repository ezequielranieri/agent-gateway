package wazero

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

	// ErrPoolExhausted is returned when the runtime pool is exhausted
	ErrPoolExhausted = errors.New("runtime pool exhausted")

	// ErrInvalidLimits is returned when resource limits are not configured
	ErrInvalidLimits = errors.New("resource limits not configured")
)

// Memory bucket configuration
const (
	// DefaultTimeoutMs is the default execution timeout in milliseconds
	DefaultTimeoutMs = 30000 // 30 seconds
	// MinTimeoutMs is the minimum allowed timeout
	MinTimeoutMs = 1
	// MaxTimeoutMs is the maximum allowed timeout (5 minutes)
	MaxTimeoutMs = 300000
	// MaxRuntimesPerBucket is the maximum number of runtimes per memory bucket
	MaxRuntimesPerBucket = 2 // 4 buckets * 2 = 8 max runtimes
)

// MemoryBuckets defines the fixed memory page buckets
var MemoryBuckets = []uint32{256, 512, 1024, 2048}

// CompileCacheKey represents a cache key for compiled modules
// Includes RuntimeID because CompiledModule is tied to its creating runtime
type CompileCacheKey struct {
	Bucket     uint32
	ToolName   string
	RuntimeID  string
}

// WasmExecutor implements tool.ToolExecutor using wazero WebAssembly runtime
// with runtime pooling by memory buckets and lazy compilation.
type WasmExecutor struct {
	config       tool.ToolConfig
	logger       zerolog.Logger
	compileCache map[CompileCacheKey]*compiledModule // Shared CompiledModule cache
	compileMu    sync.RWMutex

	// Runtime pools by memory bucket
	runtimePools map[uint32]*runtimePool
	poolMu       sync.RWMutex

	// Tool to bucket mapping
	toolBuckets map[string]uint32
}

// runtimePool manages a pool of runtimes for a specific memory bucket
type runtimePool struct {
	bucket      uint32
	runtimes    []*pooledRuntime
	available   chan *pooledRuntime
	mu          sync.Mutex
	closed      bool
}

// pooledRuntime wraps a wazero runtime with metadata
type pooledRuntime struct {
	runtime  wazero.Runtime
	mu       sync.Mutex
	inUse    bool
	lastUsed time.Time
}

// compiledModule holds a compiled WASM module and its config
type compiledModule struct {
	config      tool.ToolModuleConfig
	compiledMod wazero.CompiledModule
}

// NewWasmExecutor creates a new WebAssembly-based tool executor with runtime pooling.
func NewWasmExecutor(cfg tool.ToolConfig, logger zerolog.Logger) (*WasmExecutor, error) {
	ctx := context.Background()

	executor := &WasmExecutor{
		config:       cfg,
		logger:       logger.With().Str("component", "wasm_executor").Logger(),
		compileCache: make(map[CompileCacheKey]*compiledModule),
		runtimePools: make(map[uint32]*runtimePool),
		toolBuckets:  make(map[string]uint32),
	}

	// Initialize runtime pools for each memory bucket
	for _, bucket := range MemoryBuckets {
		pool := &runtimePool{
			bucket:    bucket,
			runtimes:  make([]*pooledRuntime, 0, MaxRuntimesPerBucket),
			available: make(chan *pooledRuntime, MaxRuntimesPerBucket),
		}
		executor.runtimePools[bucket] = pool

		// Pre-create runtimes for this bucket
		for i := 0; i < MaxRuntimesPerBucket; i++ {
			runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
				WithCloseOnContextDone(true).
				WithMemoryLimitPages(bucket))
			
			// Instantiate WASI preview1
			wasi_snapshot_preview1.MustInstantiate(ctx, runtime)

			pooled := &pooledRuntime{
				runtime:  runtime,
				inUse:    false,
				lastUsed: time.Now(),
			}
			pool.runtimes = append(pool.runtimes, pooled)
			pool.available <- pooled
		}
		executor.logger.Debug().Uint32("bucket", bucket).Int("runtimes", MaxRuntimesPerBucket).Msg("Runtime pool initialized")
	}

	// Load and compile all configured modules (lazy - compile on first use per bucket)
	for _, tc := range cfg.Tools {
		wasmBytes, err := os.ReadFile(tc.ModulePath)
		if err != nil {
			return nil, fmt.Errorf("read wasm module %s: %w", tc.ModulePath, err)
		}

		// Determine memory bucket for this tool
		bucket := executor.getMemoryBucket(tc)
		executor.toolBuckets[tc.Name] = bucket

		executor.logger.Info().
			Str("tool", tc.Name).
			Str("module", tc.ModulePath).
			Uint32("memory_bucket", bucket).
			Int("size", len(wasmBytes)).
			Msg("WASM module registered for lazy compilation")
	}

	return executor, nil
}

// getMemoryBucket returns the appropriate memory bucket for a tool config
func (w *WasmExecutor) getMemoryBucket(tc tool.ToolModuleConfig) uint32 {
	// Use tool's memory_pages limit if set, else config default, else 512
	memPages := tc.Limits.MemoryPages
	if memPages == 0 {
		memPages = w.config.DefaultMemoryPages
	}
	if memPages == 0 {
		memPages = 512
	}

	// Round up to next bucket
	for _, bucket := range MemoryBuckets {
		if memPages <= bucket {
			return bucket
		}
	}
	// If exceeds max bucket, use max bucket (will fail at instantiation if min_memory > bucket)
	return MemoryBuckets[len(MemoryBuckets)-1]
}

// getExecutionTimeout returns the execution timeout for a tool in milliseconds
// Returns 0 if no limits are explicitly configured in the registry (fail-closed)
func (w *WasmExecutor) getExecutionTimeout(validatedTool *ValidatedTool) int64 {
	// Registry limits are mandatory - fail-closed if not set
	if validatedTool == nil {
		return 0 // Not in context = fail-closed
	}
	if validatedTool.ExecutionTimeoutMs == 0 {
		return 0 // Registry didn't set limit = fail-closed
	}
	
	// Config default is a CEILING (max allowed), not a fallback
	timeoutMs := int64(validatedTool.ExecutionTimeoutMs)
	if w.config.DefaultTimeoutMs > 0 && timeoutMs > w.config.DefaultTimeoutMs {
		timeoutMs = w.config.DefaultTimeoutMs
	}
	
	// Clamp to valid range
	if timeoutMs < MinTimeoutMs {
		timeoutMs = MinTimeoutMs
	}
	if timeoutMs > MaxTimeoutMs {
		timeoutMs = MaxTimeoutMs
	}
	
	return timeoutMs
}

// getMemoryBucketFromValidated returns the appropriate memory bucket from registry
// Returns 0 if no limits in registry (fail-closed)
func (w *WasmExecutor) getMemoryBucketFromValidated(validatedTool *ValidatedTool) uint32 {
	// Registry limits are mandatory - fail-closed if not set
	if validatedTool == nil {
		return 0
	}
	if validatedTool.MemoryPages == 0 {
		return 0 // Registry didn't set limit = fail-closed
	}
	
	memPages := validatedTool.MemoryPages
	
	// Config default is a CEILING (max allowed), not a fallback
	if w.config.DefaultMemoryPages > 0 && memPages > w.config.DefaultMemoryPages {
		memPages = w.config.DefaultMemoryPages
	}
	
	// Round up to next bucket
	for _, bucket := range MemoryBuckets {
		if memPages <= bucket {
			return bucket
		}
	}
	// If exceeds max bucket, use max bucket (will fail at instantiation if min_memory > bucket)
	return MemoryBuckets[len(MemoryBuckets)-1]
}

// Name returns the executor identifier.
func (w *WasmExecutor) Name() string {
	return "wazero"
}

// SupportsTool returns true if this executor has the tool registered.
func (w *WasmExecutor) SupportsTool(name string) bool {
	_, ok := w.toolBuckets[name]
	return ok
}

// getOrCompileModule returns a compiled module from cache or compiles it within the given runtime.
// The compiled module is cached per-runtime since CompiledModule is tied to its creating runtime.
func (w *WasmExecutor) getOrCompileModule(ctx context.Context, runtime wazero.Runtime, toolName string, bucket uint32) (*compiledModule, error) {
	// Use runtime pointer as part of cache key since CompiledModule is tied to its runtime
	runtimePtr := fmt.Sprintf("%p", runtime)
	key := CompileCacheKey{Bucket: bucket, ToolName: toolName, RuntimeID: runtimePtr}

	// Check cache first
	w.compileMu.RLock()
	cached, ok := w.compileCache[key]
	w.compileMu.RUnlock()

	if ok {
		return cached, nil
	}

	// Cache miss - compile module within this runtime
	w.compileMu.Lock()
	defer w.compileMu.Unlock()

	// Double-check after acquiring lock
	if cached, ok := w.compileCache[key]; ok {
		return cached, nil
	}

	// Find tool config
	var tc *tool.ToolModuleConfig
	for i := range w.config.Tools {
		if w.config.Tools[i].Name == toolName {
			tc = &w.config.Tools[i]
			break
		}
	}
	if tc == nil {
		return nil, ErrModuleNotFound
	}

	// Read WASM bytes
	wasmBytes, err := os.ReadFile(tc.ModulePath)
	if err != nil {
		return nil, fmt.Errorf("read wasm module %s: %w", tc.ModulePath, err)
	}

	// Compile module within this runtime
	compiled, err := runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("compile wasm module %s: %w", tc.ModulePath, err)
	}

	compiledMod := &compiledModule{
		config:      *tc,
		compiledMod: compiled,
	}

	// Store in cache (per-runtime)
	w.compileCache[key] = compiledMod
	w.logger.Info().
		Str("tool", toolName).
		Uint32("bucket", bucket).
		Msg("WASM module compiled and cached")

	return compiledMod, nil
}

// acquireRuntime acquires a runtime from the pool for the given bucket
func (w *WasmExecutor) acquireRuntime(ctx context.Context, bucket uint32) (wazero.Runtime, error) {
	w.poolMu.RLock()
	pool, ok := w.runtimePools[bucket]
	w.poolMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no runtime pool for bucket %d", bucket)
	}

	select {
	case runtime := <-pool.available:
		runtime.mu.Lock()
		runtime.inUse = true
		runtime.lastUsed = time.Now()
		runtime.mu.Unlock()
		return runtime.runtime, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, ErrPoolExhausted
	}
}

// releaseRuntime returns a runtime to the pool
func (w *WasmExecutor) releaseRuntime(bucket uint32, runtime wazero.Runtime) {
	w.poolMu.RLock()
	pool, ok := w.runtimePools[bucket]
	w.poolMu.RUnlock()

	if !ok {
		return
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	for _, pr := range pool.runtimes {
		if pr.runtime == runtime {
			pr.inUse = false
			pr.lastUsed = time.Now()
			select {
			case pool.available <- pr:
			default:
				// Should not happen if pool size is correct
			}
			break
		}
	}
}

// Execute runs a tool call using WebAssembly with resource limits enforcement.
func (w *WasmExecutor) Execute(ctx context.Context, call tool.ToolCall) (tool.ToolResult, error) {
	startTime := time.Now()

	// 1. Lookup tool config
	bucket, ok := w.toolBuckets[call.Name]
	if !ok {
		w.logger.Warn().Str("tool", call.Name).Msg("Tool not found in registry")
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    tool.ErrToolNotFound.Error(),
			Duration: time.Since(startTime),
		}, tool.ErrToolNotFound
	}

	// Find tool config for grants
	var tc *tool.ToolModuleConfig
	for i := range w.config.Tools {
		if w.config.Tools[i].Name == call.Name {
			tc = &w.config.Tools[i]
			break
		}
	}
	if tc == nil {
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    tool.ErrToolNotFound.Error(),
			Duration: time.Since(startTime),
		}, tool.ErrToolNotFound
	}

// 2. Resolve execution limits from registry (fail-closed if not set)
	validatedTool := getValidatedToolFromContext(ctx, call.Name)
	timeoutMs := w.getExecutionTimeout(validatedTool)

	// Resolve memory bucket from registry limits
	bucket = w.getMemoryBucketFromValidated(validatedTool)

	// FAIL-CLOSED: If no limits from registry, reject immediately
	if timeoutMs <= 0 || bucket == 0 {
		w.logger.Error().Str("tool", call.Name).Msg("Execution rejected: no resource limits from registry (fail-closed)")
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    ErrInvalidLimits.Error(),
			Duration: time.Since(startTime),
		}, tool.ErrToolResourceExhausted
	}

	// 3. Acquire runtime from pool (compile + instantiate in same runtime)
	runtime, err := w.acquireRuntime(ctx, bucket)
	if err != nil {
		w.logger.Error().Err(err).Str("tool", call.Name).Uint32("bucket", bucket).Msg("Failed to acquire runtime from pool")
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    fmt.Sprintf("runtime pool exhausted: %v", err),
			Duration: time.Since(startTime),
		}, tool.ErrToolResourceExhausted
	}
	defer w.releaseRuntime(bucket, runtime)

	// 4. Get or compile module within this runtime
	compiledMod, err := w.getOrCompileModule(ctx, runtime, call.Name, bucket)
	if err != nil {
		w.logger.Error().Err(err).Str("tool", call.Name).Msg("Failed to get compiled module")
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    fmt.Sprintf("module compilation failed: %v", err),
			Duration: time.Since(startTime),
		}, ErrModuleInstantiationFailed
	}

	// 5. Create execution context with timeout
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	// 6. Configure module
	moduleConfig := wazero.NewModuleConfig().
		WithName(tc.Name).
		WithSysNanotime().
		WithSysNanosleep()

	// Configure FS mounts (read-only)
	for _, mount := range tc.Grants.FSReadOnlyMounts {
		hostPath := mount.HostPath
		if !filepath.IsAbs(hostPath) {
			hostPath = filepath.Join(".", hostPath)
		}
		moduleConfig = moduleConfig.WithFSConfig(
			wazero.NewFSConfig().WithDirMount(hostPath, mount.GuestPath),
		)
	}

	// Network access (opt-in)
	if tc.Grants.AllowNetwork {
		moduleConfig = moduleConfig.
			WithSysNanosleep().
			WithSysWalltime()
	}

	// 5. Instantiate module per-execution (new instance per call)
	mod, err := runtime.InstantiateModule(execCtx, compiledMod.compiledMod, moduleConfig)
	if err != nil {
		w.logger.Error().
			Err(err).
			Str("tool", tc.Name).
			Uint32("bucket", bucket).
			Msg("Module instantiation failed")
		return tool.ToolResult{
			CallID:   call.ID,
			Error:    fmt.Sprintf("module instantiation failed: %v", err),
			Duration: time.Since(startTime),
		}, ErrModuleInstantiationFailed
	}
	defer mod.Close(execCtx)

	// 6. Call the tool's execute function
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
			Str("tool", tc.Name).
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
	w.poolMu.Lock()
	defer w.poolMu.Unlock()

	for bucket, pool := range w.runtimePools {
		pool.mu.Lock()
		pool.closed = true
		close(pool.available)
		for _, pr := range pool.runtimes {
			if err := pr.runtime.Close(ctx); err != nil {
				w.logger.Error().Err(err).Uint32("bucket", bucket).Msg("Failed to close runtime")
			}
		}
		pool.mu.Unlock()
	}

	return nil
}

// validatedToolContextKey is the context key for validated tool
type validatedToolContextKey struct{}

// WithValidatedTool adds a validated tool to the context
func WithValidatedTool(ctx context.Context, vt *ValidatedTool) context.Context {
	return context.WithValue(ctx, validatedToolContextKey{}, vt)
}

// getValidatedToolFromContext retrieves a validated tool from context
func getValidatedToolFromContext(ctx context.Context, toolName string) *ValidatedTool {
	if vt, ok := ctx.Value(validatedToolContextKey{}).(*ValidatedTool); ok && vt.Name == toolName {
		return vt
	}
	return nil
}

// ValidatedTool represents a tool definition validated against the registry
// with registry grants, limits, and hash for execution.
// Note: This mirrors the ValidatedTool in the chat package but is defined here
// to avoid import cycles. In practice, this should be in a shared package.
type ValidatedTool struct {
	Name                string
	Description         string
	InputSchema         json.RawMessage
	Grants              json.RawMessage
	ExecutionTimeoutMs  uint64
	MemoryPages         uint32
	Hash                string
}