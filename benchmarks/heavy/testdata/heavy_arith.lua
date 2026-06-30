-- heavy_arith: 单函数 + 长热循环 + 纯浮点算术。
-- 形态:1 个非 vararg kernel(n) → 必升 P3/P4;循环体里 ADD/SUB/MUL/DIV/FORLOOP,
-- 没有表访问、没有 CALL、没有 string。是 wasm/JIT 档最有利发挥的场。
local function kernel(n)
  local s = 0.0
  for i = 1, n do
    local x = i * 1.5
    s = s + x * x - x / 2.0 + 0.25
  end
  return s
end
return kernel(2000000)
