;; echo.wat
;; Simple echo tool that returns the input arguments.
;; Used as a baseline tool that executes successfully with default limits.
;;
;; Compile with: wat2wasm echo.wat -o echo.wasm

(module
  (memory (export "memory") 1)
  (func (export "execute") (result i32)
    ;; Simple successful execution
    (return (i32.const 42))
  )
)

;; Test expectations:
;; - With any reasonable limits, should execute successfully
;; - Returns 42 as success indicator