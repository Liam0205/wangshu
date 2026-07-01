-- heavy_recursion: collatz 序列,深尾递归 + 整数算术。
-- 形态:1 个非 vararg kernel(n, steps) 自递归 → 升层后 gibbous→gibbous
-- 走 call_indirect 直达,不经 h_call helper。每帧体:比较 + 算术 + RETURN,
-- 帧体工作量 / 帧建拆比值合理。外层主循环驱动 1000 次 collatz 调用。
local function collatz(n, steps)
  if n == 1 then return steps end
  if n % 2 == 0 then return collatz(n / 2, steps + 1) end
  return collatz(3 * n + 1, steps + 1)
end
local total = 0
for i = 1, 1000 do
  total = total + collatz(i, 0)
end
return total
