(module
  (memory 1)
  (export "memory" (memory 0))
  (func (export "execute") (result i32)
    i32.const 42
    return)
)