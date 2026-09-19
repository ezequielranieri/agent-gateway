;; memory_grow.wat
;; WASM module that attempts to grow memory, checks for failure (-1),
;; and executes unreachable if growth fails.
;; This tests memory limit enforcement in the executor.
;;
;; Compile with: wat2wasm memory_grow.wat -o memory_grow.wasm

(module
  ;; Memory with initial 1 page (64KB), max 512 pages (32MB)
  ;; The executor will set the actual limit via WithMemoryLimitPages
  (memory (export "memory") 1 512)

  ;; Function to test memory growth beyond bucket limit
  (func (export "execute") (result i32)
    ;; Try to grow memory by 300 pages (total would be 301 pages = ~19MB)
    ;; This should fail if limit is 256 pages (16MB)
    (local $result i32)
    (local.set $result (memory.grow (i32.const 300)))
    
    ;; Check if growth failed (returns -1)
    (if (result i32)
      (i32.eq (local.get $result) (i32.const -1))
      (then
        ;; Growth failed - execute unreachable to trap
        unreachable
      )
      (else
        ;; Growth succeeded - return 0 (should not happen with 256-page limit)
        (return (i32.const 0))
      )
    )
    
    ;; Should never reach here
    (return (i32.const 0))
  )

  ;; Control function: same module with higher bucket limit should succeed
  (func (export "grow_within_limit") (result i32)
    ;; Try to grow by 100 pages (total 101 pages = ~6.4MB)
    ;; This should succeed with 512-page limit
    (local $result i32)
    (local.set $result (memory.grow (i32.const 100)))
    
    ;; Check if growth succeeded (returns old page count, not -1)
    (if (result i32)
      (i32.eq (local.get $result) (i32.const -1))
      (then
        ;; Growth failed unexpectedly
        unreachable
      )
      (else
        ;; Growth succeeded - return 1
        (return (i32.const 1))
      )
    )
    
    ;; Should never reach here
    (return (i32.const 0))
  )

  ;; Function to get current memory pages (for debugging)
  (func (export "memory_pages") (result i32)
    (memory.size)
  )
)

;; Test expectations:
;; - In 256-page bucket (16MB limit):
;;   execute(): memory.grow(300) should return -1 -> unreachable -> trap
;;   Executor should catch trap and return ErrToolResourceExhausted
;; - In 512-page bucket (32MB limit):
;;   grow_within_limit(): memory.grow(100) should succeed (return 1)
;;   Function returns 0
;;
;; Usage in tests:
;; 1. Register memory_grow.wasm with MemoryPages: 256 -> bucket 256
;;    Call execute() -> expect ErrToolResourceExhausted
;; 2. Register memory_grow.wasm with MemoryPages: 512 -> bucket 512
;;    Call grow_within_limit() -> expect success (return 0)