-- heavy_floatloop:浮点平面累积核 + 嵌套 FORLOOP。
-- 形态:单函数 kernel(x0, y0),内层固定 256 迭代纯算术,所有像素都跑满 — 让
-- P3 relooper(承 "improper scope overlap" 限制)能升起内层函数。
--
-- P3/P4 当前形态子集下「单函数大热点 + 长 BB + 纯浮点」最直接的发挥场景:
--   - kernel(x0, y0):非 vararg,内层 while + 算术,容易升;
--   - 主循环 10000 次调 kernel,每次内层 256 迭代 → 256 万次纯浮点工作。
-- 没有 if/and/or/break/table/string/CALL(数学库都不用)。
--
-- 形态:线性递推 + 累积 sin/cos 近似(纯算术,无溢出)。
local function kernel(x0, y0)
  local x = x0
  local y = y0
  local s = 0.0
  local i = 0
  while i < 256 do
    -- 旋转近似:无溢出的线性核
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
