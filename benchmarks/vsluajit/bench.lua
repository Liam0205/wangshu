-- bench.lua — engine-neutral self-timing benchmark suite for the
-- wangshu-vs-LuaJIT comparison (scripts/bench-vs-luajit.sh).
--
-- Pure Lua 5.1, no engine-specific APIs: the same file runs under
-- wangshu (P1 / P4-auto / P4-force via benchmarks/cmd/benchlua) and
-- the luajit binary. Output: one "name<TAB>iters<TAB>us_per_iter"
-- line per kernel on stdout.
--
-- Methodology:
--   - every kernel is a non-vararg function (a vararg top-level chunk
--     never promotes on wangshu tiered builds);
--   - WARMUP calls run untimed first — that is where wangshu's
--     natural-heat promotion (entry threshold 200) and LuaJIT's trace
--     recording both happen, so the timed section measures steady
--     state on every engine;
--   - fixed iteration counts (not calibrated) so all engines measure
--     the same workload; per-iteration cost is what the table reports;
--   - os.clock deltas: CPU time on LuaJIT, wall time on wangshu —
--     equivalent for these single-threaded busy loops;
--   - each kernel's result accumulates into a global checksum printed
--     at the end, so an engine can't dead-code-eliminate the work and
--     cross-engine result divergence is visible in the logs.

local checksum = 0
local WARMUP = 300

local function bench(name, iters, fn)
  for _ = 1, WARMUP do
    checksum = checksum + fn() * 1e-9
  end
  local t0 = os.clock()
  local acc = 0
  for _ = 1, iters do
    acc = acc + fn()
  end
  local dt = os.clock() - t0
  checksum = checksum + acc * 1e-9
  print(string.format("%s\t%d\t%.3f", name, iters, dt * 1e6 / iters))
end

-- simple: MOVE/compare/jump (baseline tier-1 shape).
local function simple_kernel()
  local a, b = 1, 2
  local r = 0
  if a < b then r = a else r = b end
  return r
end
bench("simple", 2000000, simple_kernel)

-- arith: Horner 5th-degree polynomial (the roadmap §1 calibration
-- kernel shape).
local function arith_kernel()
  local x = 1.5
  return ((((x + 2) * x + 3) * x + 4) * x + 5) * x + 6
end
bench("arith", 2000000, arith_kernel)

-- loop: summation column-kernel shape (baseline tier-3).
local function loop_kernel()
  local s = 0
  for i = 1, 1000 do s = s + i * i end
  return s
end
bench("loop", 20000, loop_kernel)

-- heavy_arith: long hot FORLOOP + pure float arithmetic
-- (benchmarks/heavy/testdata/heavy_arith.lua body).
local function heavy_arith_kernel()
  local s = 0.0
  for i = 1, 100000 do
    local x = i * 1.5
    s = s + x * x - x / 2.0 + 0.25
  end
  return s
end
bench("heavy_arith", 300, heavy_arith_kernel)

-- heavy_floatloop: rotation-approximation accumulation core
-- (benchmarks/heavy/testdata/heavy_floatloop.lua inner kernel).
local function floatloop_kernel(x0, y0)
  local x = x0
  local y = y0
  local s = 0.0
  local i = 0
  while i < 256 do
    local xt = x - 0.001953125 * y
    local yt = y + 0.001953125 * x
    x = xt
    y = yt
    s = s + x * y0 + y * x0
    i = i + 1
  end
  return s
end
local function heavy_floatloop_once()
  local total = 0.0
  for p = 0, 99 do
    total = total + floatloop_kernel(p / 50.0 - 1.5, p / 50.0 - 1.0)
  end
  return total
end
bench("heavy_floatloop", 3000, heavy_floatloop_once)

-- heavy_recursion: collatz tail recursion
-- (benchmarks/heavy/testdata/heavy_recursion.lua).
local function collatz(n, steps)
  if n == 1 then return steps end
  if n % 2 == 0 then return collatz(n / 2, steps + 1) end
  return collatz(3 * n + 1, steps + 1)
end
local function heavy_recursion_once()
  local total = 0
  for i = 1, 1000 do
    total = total + collatz(i, 0)
  end
  return total
end
bench("heavy_recursion", 300, heavy_recursion_once)

-- fib: classic recursive fibonacci (call-overhead-dense,
-- benchmarks/realworld/testdata/fib.lua).
local function fib(n)
  if n < 2 then return n end
  return fib(n - 1) + fib(n - 2)
end
local function fib_once()
  return fib(24)
end
bench("fib", 30, fib_once)

print(string.format("checksum\t%.6f", checksum))
