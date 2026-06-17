// consts_test.go:embedded 包内被多 build variant 共用的 const + type + helper。
// 无 build tag → p1 / p3 build 都能引用,避免文件级 build tag 在重分桶时丢
// 共享依赖(issue #15 review)。
//
// 各 benchmark 文件的形态:
//   - mini_bench_test.go(!wangshu_p3):`_Wangshu` / `_Gopher` mini benchmark
//   - realworld_embedded_bench_test.go(!wangshu_p3):同形态 realworld bench
//   - embedded_gibbous_test.go(wangshu_p3):`_Gibbous` benchmark,亦读这里的 const
package embedded

// official baseline "simple": data hardcoded as locals, no host data passed in.
const simpleSelfContained = `
local a, b = 1, 2
local r = 0
if a < b then r = a else r = b end
return r
`

// pineapple-shaped "if_" predicate: reads a GLOBAL (set by host per call).
const ifPredicateScript = `
function evaluate()
  if (user_id ~= nil and user_id ~= '' and user_id ~= '0') then
    return false
  else
    return true
  end
end
`

// const predicate: same control flow, reads NO globals (isolates call cost).
const constPredicateScript = `
function evaluate()
  local x = 1
  if x == 1 then return false else return true end
end
`

// predicateScript: an eligibility gate over several item fields — the common
// "short boolean predicate" pineapple form. Reads globals set per item.
const predicateScript = `
function evaluate()
  if user_id == nil or user_id == '' or user_id == '0' then
    return false
  end
  if age < 18 or age > 120 then
    return false
  end
  if not is_active then
    return false
  end
  return score >= 0.5
end
`

// transformScript: numeric feature derivation (weighted score + clamp) — the
// "feature transform" pineapple form. Reads globals, returns a number.
const transformScript = `
function evaluate()
  local s = raw_score * 0.85 + base_bias
  if s < 0.0 then s = 0.0 end
  if s > 100.0 then s = 100.0 end
  return s * recency_factor
end
`

// item is a synthetic input record; nItems sized so the loop stays in-cache.
type item struct {
	userID        string
	age           float64
	isActive      bool
	score         float64
	rawScore      float64
	baseBias      float64
	recencyFactor float64
}

const nItems = 1000

func makeItems() []item {
	items := make([]item, nItems)
	for i := range items {
		items[i] = item{
			userID:        "user-" + string(rune('0'+i%10)),
			age:           18 + float64(i%80),
			isActive:      i%3 != 0,
			score:         float64(i%100) / 100.0,
			rawScore:      float64(i % 100),
			baseBias:      5.0,
			recencyFactor: 0.5 + float64(i%50)/100.0,
		}
	}
	return items
}
