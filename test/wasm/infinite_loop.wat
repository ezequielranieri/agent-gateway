;; infinite_loop.wat
;; WASM module with an infinite loop that should be terminated by
;; the execution timeout + WithCloseOnContextDone(true).
;;
;; Compile with: wat2wasm infinite_loop.wat -o infinite_loop.wasm

(module
  ;; Memory with minimal size
  (memory (export "memory") 1)

  ;; Infinite loop function - should be killed by timeout
  (func (export "execute") (result i32)
    (local $i i32)
    (local.set $i (i32.const 0))
    
    ;; Infinite loop
    loop $infinite
      ;; Increment counter
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      
      ;; No-op to prevent optimization
      (drop (i32.add (local.get $i) (i32.const 0)))
      
      ;; Continue loop
      br $infinite
    end
    
    ;; Never reached
    (return (i32.const 0))
  )

  ;; Function that does some work and returns (for comparison)
  (func (export "finite_work") (result i32)
    (local $i i32)
    (local.set $i (i32.const 0))
    
    loop $finite
      (local.set $i (i32.add (local.get $i) (i32.const 1)))
      (i32.lt_u (local.get $i) (i32.const 1000000))
      br_if $finite
    end
    
    (return (local.get $i))
  )
)

;; Test expectations:
;; - With execution_timeout_ms = 100ms:
;;   execute() should be terminated by timeout after ~100ms
;;   WithCloseOnContextDone(true) should force module closure
;;   Executor should return timeout error (not success)
;;   Duration should be ~100ms (not minutes/hours)
;;
;; - finite_work() with same timeout:
;;   Should complete before timeout if fast enough
;;   Should return 1000000