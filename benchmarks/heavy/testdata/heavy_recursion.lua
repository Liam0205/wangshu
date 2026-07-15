-- heavy_recursion: collatz sequence, deep tail recursion + integer arithmetic.
-- Shape: 1 non-vararg self-recursive kernel(n, steps) -> after promotion,
-- gibbous->gibbous goes direct via call_indirect, bypassing the h_call
-- helper. Per-frame body: compare + arithmetic + RETURN, a reasonable ratio
-- of frame-body work to frame setup/teardown. The outer main loop drives
-- 1000 collatz calls.
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
