;; min_memory_exceed.wat
;; WASM module that declares a minimum memory requirement exceeding
;; the executor's bucket limit. Should fail at instantiation.
;;
;; Compile with: wat2wasm min_memory_exceed.wat -o min_memory_exceed.wasm

(module
  ;; Declare memory with minimum 300 pages (~19MB), maximum 400 pages (~25MB)
  ;; The executor's bucket for this tool will be 256 pages max (16MB)
  ;; but we'll test with a tool configured for a smaller bucket
  (memory (export "memory") 300 400)

  ;; Simple execute function
  (func (export "execute") (result i32)
    (return (i32.const 42))
  )
)

;; Test expectations:
;; - When executor bucket is 256 pages (16MB) but module declares min 300 pages:
;;   Instantiation should fail with "memory limit exceeded" or similar
;;   Executor should catch this and return ErrToolResourceExhausted (fail-closed)
;;
;; - If executor bucket is >= 300 pages:
;;   Instantiation succeeds
;;   execute() returns 42