-- heavy_floatloop: floating-point plane accumulation kernel + nested FORLOOP.
-- Shape: a single kernel(x0, y0) function, inner loop fixed at 256 pure-
-- arithmetic iterations, every pixel runs the full count -- so the P3
-- relooper (under the "improper scope overlap" limitation) can promote the
-- inner function.
--
-- The most direct showcase of "single-function hotspot + long BB + pure
-- float" under the current P3/P4 shape subset:
--   - kernel(x0, y0): non-vararg, inner while + arithmetic, easy to promote;
--   - the main loop calls kernel 10000 times, 256 inner iterations each
--     -> 2.56M pure floating-point operations.
-- No if/and/or/break/table/string/CALL (not even the math library).
--
-- Shape: linear recurrence + accumulated sin/cos approximation (pure
-- arithmetic, no overflow).
local function kernel(x0, y0)
  local x = x0
  local y = y0
  local s = 0.0
  local i = 0
  while i < 256 do
    -- rotation approximation: an overflow-free linear kernel
    local xt = x - 0.001953125 * y
    local yt = y + 0.001953125 * x
    x = xt
    y = yt
    s = s + x * y0 + y * x0
    i = i + 1
  end
  return s
end
local total = 0.0
local W = 100
local H = 100
for py = 0, H - 1 do
  for px = 0, W - 1 do
    local x0 = px / (W * 0.5) - 1.5
    local y0 = py / (H * 0.5) - 1.0
    total = total + kernel(x0, y0)
  end
end
return total
