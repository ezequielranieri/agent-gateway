# WASM Test Fixtures

This directory contains WebAssembly test modules in WAT (WebAssembly Text) format
with their compiled WASM binaries. These fixtures are used by the executor tests
to verify resource limit enforcement, timeout handling, and instance isolation.

## Fixtures

| Fixture | Purpose | Expected Behavior |
|---------|---------|-------------------|
| `memory_grow.wat` | Tests memory limit enforcement | `memory.grow` fails with -1 → `unreachable` → trap |
| `infinite_loop.wat` | Tests execution timeout | Loop runs until timeout kills it |
| `min_memory_exceed.wat` | Tests min memory > bucket limit | Instantiation fails at module load |
| `state_persist.wat` | Tests instance isolation | Each call returns 1 (fresh instance) |

## Compilation

These fixtures are written in WAT (WebAssembly Text Format). To compile to WASM binary:

### Prerequisites

Install WABT (WebAssembly Binary Toolkit):

```bash
# macOS
brew install wabt

# Ubuntu/Debian
apt-get install wabt

# Or from source: https://github.com/WebAssembly/wabt
```

### Compile all fixtures

```bash
cd test/wasm
wat2wasm memory_grow.wat -o memory_grow.wasm
wat2wasm infinite_loop.wat -o infinite_loop.wasm
wat2wasm min_memory_exceed.wat -o min_memory_exceed.wasm
wat2wasm state_persist.wat -o state_persist.wasm
```

Or compile all at once:
```bash
for f in *.wat; do wat2wasm "$f" -o "${f%.wat}.wasm"; done
```

### Verify compilation

```bash
# Check that .wasm files were created
ls -la *.wasm

# Inspect a module
wasm2wat memory_grow.wasm | head -50
```

## Test Expectations

### memory_grow.wasm
- **Bucket limit**: 2 pages (configured in test)
- **Module declares**: min 1, max 2 pages
- **Action**: Tries to grow by 2 pages (total 3)
- **Expected**: `memory.grow(2)` returns -1 → `unreachable` → trap
- **Executor result**: `ErrToolResourceExhausted` (memory limit exceeded)

### infinite_loop.wasm
- **Timeout**: 100ms (configured in test)
- **Action**: Infinite `br` loop
- **Expected**: `WithCloseOnContextDone(true)` kills module at deadline
- **Executor result**: Timeout error, duration ~100ms (not hanging)

### min_memory_exceed.wasm
- **Module declares**: min 10 pages
- **Executor bucket**: 4 pages (configured in test)
- **Expected**: Instantiation fails (module min > bucket max)
- **Executor result**: `ErrToolResourceExhausted` (fail-closed)

### state_persist.wasm
- **Global counter**: Increments on each `execute()` call
- **Expected**: Each call returns 1 (new instance per call)
- **Failure mode**: If returns 2, 3, 4... → state leaking between calls (BUG)

## Test Configuration

The tests in `executor_limits_test.go` configure:
- Memory buckets: {256, 512, 1024, 2048} pages (production)
- Test buckets: smaller values for fast testing
- Timeouts: 100-500ms for fast test execution
- Memory limits: Set below module requirements to trigger limits

## Adding New Fixtures

1. Create `.wat` file in this directory
2. Add compilation command to this README
3. Add test case in `executor_limits_test.go`
4. Document expected behavior in test comments
5. Commit both `.wat` and `.wasm` files

## Compilation in CI

The CI pipeline should:
1. Install wabt
2. Compile all `.wat` → `.wasm`
3. Verify `.wasm` files exist
4. Run integration tests with `-tags integration`

```yaml
- name: Install WABT
  run: |
    # Install wabt (adjust for your CI environment)
    git clone --depth 1 https://github.com/WebAssembly/wabt.git
    cd wabt && mkdir build && cd build && cmake .. && make -j$(nproc)
    sudo cp build/wat2wasm /usr/local/bin/

- name: Compile WASM fixtures
  run: |
    cd test/wasm
    for f in *.wat; do wat2wasm "$f" -o "${f%.wat}.wasm"; done

- name: Verify WASM files
  run: ls -la test/wasm/*.wasm
```