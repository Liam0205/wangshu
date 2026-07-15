-- heavy_arith: single function + long hot loop + pure floating-point arithmetic.
-- Shape: 1 non-vararg kernel(n) -> always promotes to P3/P4; the loop body is
-- ADD/SUB/MUL/DIV/FORLOOP with no table access, no CALL, no string. The most
-- favorable ground for the wasm/JIT tiers.
local function kernel(n)
  local s = 0.0
  for i = 1, n do
    local x = i * 1.5
    s = s + x * x - x / 2.0 + 0.25
  end
  return s
end
return kernel(2000000)
