-- fib: 经典递归 Fibonacci(benchmark game 形态;调用开销密集)
local function fib(n)
  if n < 2 then return n end
  return fib(n - 1) + fib(n - 2)
end
return fib(24)
