-- binary-trees: benchmark game 经典(分配/GC 密集)
local function bottomup(depth)
  if depth > 0 then
    depth = depth - 1
    local left, right = bottomup(depth), bottomup(depth)
    return { left, right }
  end
  return { false, false }
end

local function check(tree)
  if tree[1] then
    return 1 + check(tree[1]) + check(tree[2])
  end
  return 1
end

local mindepth = 4
local maxdepth = 10

local stretch = bottomup(maxdepth + 1)
local stretchChecksum = check(stretch)

local longlived = bottomup(maxdepth)

local total = 0
for depth = mindepth, maxdepth, 2 do
  local iterations = 2 ^ (maxdepth - depth + mindepth)
  local sum = 0
  for i = 1, iterations do
    sum = sum + check(bottomup(depth))
  end
  total = total + sum
end

return stretchChecksum, check(longlived), total
