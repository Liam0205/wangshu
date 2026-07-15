// GC transparency fuzz (12 §5): the same script must produce byte-equal output under
// two modes — "normal GC" and "high-pressure stress GC (force Collect at every safepoint)".
//
// A difference = GC missing a root / early reclaim / relocation corrupting a live object —
// this is the primary line of defense for GC correctness.
package difftest

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// runWithStress runs the script; stress controls the GC pressure mode. Returns (output, runError).
func runWithStress(t *testing.T, src string, stress bool) (string, error) {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "gcstress")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetGCStressMode(stress)
	results, err := prog.Run(st)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Display()
	}
	return strings.Join(parts, "\t"), nil
}

// TestGCStress_SeedCorpus runs the dual-mode comparison over the entire seed corpus.
func TestGCStress_SeedCorpus(t *testing.T) {
	for _, c := range seedCorpus {
		t.Run(c.name, func(t *testing.T) {
			normal, err1 := runWithStress(t, c.src, false)
			stressed, err2 := runWithStress(t, c.src, true)
			if (err1 == nil) != (err2 == nil) {
				t.Fatalf("error divergence: normal=%v stressed=%v", err1, err2)
			}
			if err1 != nil {
				if err1.Error() != err2.Error() {
					t.Errorf("error text diff:\n  normal:  %v\n  stressed: %v", err1, err2)
				}
				return
			}
			if normal != stressed {
				t.Errorf("GC transparency violated:\n  normal:   %q\n  stressed: %q", normal, stressed)
			}
		})
	}
}

// TestGCStress_RandomScripts runs the dual-mode comparison over randomly generated scripts
// (200 seeds by default; scaled up in nightly via WANGSHU_GCSTRESS_N; it shares the generator
// with the seeds used for the oracle differential test, but here it only compares its own two modes).
func TestGCStress_RandomScripts(t *testing.T) {
	nScripts := int64(200)
	if v := os.Getenv("WANGSHU_GCSTRESS_N"); v != "" {
		if p, err := strconv.ParseInt(v, 10, 64); err == nil {
			nScripts = p
		}
	}
	for seed := int64(0); seed < nScripts; seed++ {
		src := generateScript(seed)
		normal, err1 := runWithStress(t, src, false)
		stressed, err2 := runWithStress(t, src, true)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("DIVERGENCE seed=%d kind=gcstress\nerror divergence: normal=%v stressed=%v\n--- script ---\n%s",
				seed, err1, err2, src)
		}
		if err1 != nil {
			continue
		}
		if normal != stressed {
			t.Errorf("DIVERGENCE seed=%d kind=gcstress\nGC transparency violated:\n  normal:   %q\n  stressed: %q\n--- script ---\n%s",
				seed, normal, stressed, src)
		}
	}
}

// TestGCStress_AllocHeavy runs allocation-dense targeted scripts (frequent construction of tables/closures/strings).
func TestGCStress_AllocHeavy(t *testing.T) {
	cases := []diffCase{
		{"table_churn", `
local keep = {}
for i = 1, 100 do
  local t = { i, i * 2, tostring(i) }
  if i % 10 == 0 then keep[#keep + 1] = t end
end
local sum = 0
for _, t in ipairs(keep) do sum = sum + t[1] + t[2] end
return sum`},
		{"closure_churn", `
local fns = {}
for i = 1, 50 do
  fns[i] = function() return i * i end
end
local total = 0
for i = 1, 50 do total = total + fns[i]() end
return total`},
		{"string_churn", `
local s = ""
for i = 1, 60 do s = s .. tostring(i % 10) end
return #s, s:sub(1, 5)`},
		{"weak_under_stress", `
local strong = {}
local weak = setmetatable({}, { __mode = "v" })
for i = 1, 30 do
  local t = { v = i }
  weak[i] = t
  if i % 3 == 0 then strong[#strong + 1] = t end
end
local kept = 0
for i = 1, 30 do if weak[i] ~= nil then kept = kept + 1 end end
-- 强引用的 10 个必然存活;弱引用的存活数依 GC 时机而定,只断言下界
return kept >= 10`},
		{"coroutine_churn", `
local out = 0
for n = 1, 10 do
  local co = coroutine.create(function(x)
    local acc = x
    for i = 1, 5 do acc = acc + coroutine.yield(acc) end
    return acc
  end)
  local _, v = coroutine.resume(co, n)
  for i = 1, 5 do _, v = coroutine.resume(co, 1) end
  out = out + v
end
return out`},
		// Two freelist UAF regression cases (fixed after random fuzz hit them):
		// (1) setmetatable after coroutine churn — residual values above top resurrect + the SETLIST multi-value window
		//     failing to restore top once caused the __index table to be wrongly reclaimed (nil under stress mode);
		{"uaf_coroutine_then_meta", `
local cov = coroutine.create(function(z)
  for i = 1, 4 do z = coroutine.yield(z + i) end
  return z
end)
local v0 = ""
for i = 1, 5 do
  local ok, v = coroutine.resume(cov, 9)
  v0 = v0 .. tostring(v) .. ";"
end
local function mk()
  local n = 4
  return function() n = n + 1; return n end
end
local c = mk()
local base = { v = 807 }
local probe1 = base.v
local tv = setmetatable({}, { __index = base })
return v0, tostring(probe1), tostring(tv.v)`},
		// (2) setmetatable after a table constructor containing a host call (SETLIST B=0 consuming the multi-value window).
		{"uaf_setlist_then_meta", `
local v1 = { 650, math.floor(math.max(#"abc", #"42")) }
v1[#v1 + 1] = 34.6561
local base = { v = 672 }
local tv = setmetatable({}, { __index = base })
return tostring(tv.v)`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			normal, err1 := runWithStress(t, c.src, false)
			stressed, err2 := runWithStress(t, c.src, true)
			if (err1 == nil) != (err2 == nil) {
				t.Fatalf("error divergence: normal=%v stressed=%v", err1, err2)
			}
			if err1 == nil && normal != stressed {
				t.Errorf("GC transparency violated:\n  normal:   %q\n  stressed: %q", normal, stressed)
			}
		})
	}
}
