-- fib: classic recursive Fibonacci (benchmark game shape; call-overhead heavy)
local function fib(n)
  if n < 2 then return n end
  return fib(n - 1) + fib(n - 2)
end
return fib(24)
