;; state_persist.wat
;; WASM module that modifies global state between calls.
;; Tests that a new instance is created per Execute() call (state isolation).
;;
;; Compile with: wat2wasm state_persist.wat -o state_persist.wasm

(module
  (memory (export "memory") 1)

  ;; Global counter that persists within a module instance
  (global $counter (mut i32) (i32.const 0))

  ;; Execute function that increments and returns counter
  (func (export "execute") (result i32)
    (global.set $counter (i32.add (global.get $counter) (i32.const 1)))
    (return (global.get $counter))
  )

  ;; Function to read counter without modifying
  (func (export "read_counter") (result i32)
    (return (global.get $counter))
  )

  ;; Function to reset counter
  (func (export "reset_counter")
    (global.set $counter (i32.const 0))
  )
)

;; Test expectations:
;; - With instance-per-call (new InstantiateModule per Execute):
;;   Each call to execute() should return 1 (fresh instance, counter starts at 0)
;;   State should NOT persist between calls
;;
;; - If executor reuses module instance:
;;   First call returns 1, second returns 2, third returns 3, etc.
;;   This would be a BUG - state leaking between calls
;;
;; Test sequence:
;; 1. Call execute() -> expect 1
;; 2. Call execute() -> expect 1 (not 2!)
;; 3. Call execute() -> expect 1 (not 3!)
;; 4. Call read_counter() -> expect 1 (within same call's instance)