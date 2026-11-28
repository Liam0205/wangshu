//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package difftest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// The cases here hand compiled code a table that sits where the table it warmed up on used to
// be. The warm chunk fills the inline caches of a few accessors on a global table; the run chunk
// calls them once more (so that they are compiled with those caches), drops the table, collects,
// and builds another table, which the allocator places at the freed address. A new table also
// starts from the same generation, so "a table at this address with this generation" holds for
// it, and it is not the table the caches describe: its array part can be shorter and its nodes
// hold other keys.

const tableReuseWarmArray = `
A362 = {1, 2, 3, 4, 5, 6, 7, 8}
ADDR362 = tostring(A362)
function get8_362(t) return t[8] end
function set8_362(t, v) t[8] = v end
function geti_362(t, i) return t[i] end
function seti_362(t, i, v) t[i] = v end
for i = 1, 40 do
  get8_362(A362) set8_362(A362, 8)
  geti_362(A362, 8) seti_362(A362, 8, 8)
end`

// tableReuseRunArray builds the run chunk for a second table with n array elements. op is the
// access under test; the chunk reports what it returned or stored, the table's length, and the
// neighbouring table allocated right after it.
func tableReuseRunArray(n int, op string) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprint((i + 1) * 11)
	}
	return fmt.Sprintf(`
local first = get8_362(A362) + geti_362(A362, 8)
set8_362(A362, 8) seti_362(A362, 8, 8)
A362 = nil
collectgarbage() collectgarbage()
local B = {%s}
local N = {901, 902, 903, 904, 905, 906, 907, 908}
local reused = tostring(B) == ADDR362
local r = %s
return first, tostring(r), tostring(rawget(B, 8)), #B, table.concat(N, ","), REUSED362 and reused`,
		strings.Join(items, ", "), op)
}

var tableReuseArrayOps = []struct{ name, op string }{
	{"read_constant_key", `get8_362(B)`},
	{"read_register_key", `geti_362(B, 8)`},
	{"store_constant_key", `set8_362(B, 777)`},
	{"store_register_key", `seti_362(B, 8, 777)`},
}

const tableReuseWarmNode = `
function mk362() local t = {} for i = 1, 8 do t["k" .. i] = i end return t end
A362 = mk362()
ADDR362 = tostring(A362)
function getk_362(t) return t.k8 end
function setk_362(t, v) t.k8 = v end
for i = 1, 40 do getk_362(A362) setk_362(A362, 8) end`

// tableReuseWarmMethod is the same for a method call: the table that is dropped later has a
// function under k8, and the second table gets its k8 from a metatable, so that the call
// succeeds without pcall (under pcall the accessor would not run as compiled code).
const tableReuseWarmMethod = `
function mk362() local t = {} for i = 1, 8 do t["k" .. i] = i end return t end
A362 = mk362()
A362.k8 = function() return 8 end
ADDR362 = tostring(A362)
function getk_362(t) return t:k8() end
function setk_362() end
MT362 = {__index = {k8 = function() return "inherited" end}}
for i = 1, 40 do getk_362(A362) end`

// tableReuseRunNode builds the run chunk for a second table that got n string keys and lost the
// first d of them again.
func tableReuseRunNode(n, d int, op string) string {
	return fmt.Sprintf(`
local first = getk_362(A362)
setk_362(A362, 8)
A362 = nil
collectgarbage() collectgarbage()
local B = {}
for i = 1, %d do B["j" .. i] = i end
for i = 1, %d do B["j" .. i] = nil end
local reused = tostring(B) == ADDR362
local r = %s
local keys = {}
for k, v in pairs(B) do keys[#keys + 1] = k .. "=" .. tostring(v) end
table.sort(keys)
return first, tostring(r), table.concat(keys, ","), REUSED362 and reused`, n, d, op)
}

var tableReuseNodeOps = []struct{ name, warm, op string }{
	{"read", tableReuseWarmNode, `getk_362(B)`},
	{"store", tableReuseWarmNode, `setk_362(B, 777)`},
	{"method", tableReuseWarmMethod, `getk_362(setmetatable(B, MT362))`},
}

// TestTiered_TableAtAReusedAddress runs each case the four ways TestTiered_ClearWhileTraversing
// does and requires them to agree with each other and with lua5.1. The chunks never report
// whether the address was reused (REUSED362 is nil), since lua5.1 has its own allocator;
// TestTiered_TableAtAReusedAddress_Reuses checks that separately.
func TestTiered_TableAtAReusedAddress(t *testing.T) {
	oracle := findOracle()
	for _, o := range tableReuseArrayOps {
		for n := 0; n <= 8; n++ {
			t.Run(fmt.Sprintf("array_%s/length_%d", o.name, n), func(t *testing.T) {
				checkWarmThenForced(t, oracle, tableReuseWarmArray, tableReuseRunArray(n, o.op))
			})
		}
	}
	for _, o := range tableReuseNodeOps {
		for n := 1; n <= 12; n++ {
			for d := 0; d <= n; d += 1 {
				t.Run(fmt.Sprintf("node_%s/keys_%d_removed_%d", o.name, n, d), func(t *testing.T) {
					checkWarmThenForced(t, oracle, o.warm, tableReuseRunNode(n, d, o.op))
				})
			}
		}
	}
}

// TestTiered_TableAtAReusedAddress_Reuses requires that the second table really lands at the
// address of the first in wangshu, for every access and every second table of the test above:
// without that it would compare runs that never hand compiled code a different table at a
// known address.
func TestTiered_TableAtAReusedAddress_Reuses(t *testing.T) {
	check := func(name, warmSrc, runSrc string) {
		warm, err := wangshu.Compile([]byte("REUSED362 = true\n"+warmSrc), "reuse-warm")
		if err != nil {
			t.Fatalf("wangshu compile (warm): %v", err)
		}
		run, err := wangshu.Compile([]byte(runSrc), "reuse-run")
		if err != nil {
			t.Fatalf("wangshu compile (run): %v", err)
		}
		got := runTieredClear(t, newInterpBaselineState(), warm, run, true, name)
		if !strings.HasSuffix(got, "\ttrue\n") {
			t.Errorf("%s: the second table is not at the address of the first (result %q)", name, got)
		}
	}
	for _, o := range tableReuseArrayOps {
		for n := 0; n <= 8; n++ {
			check(fmt.Sprintf("array %s, length %d", o.name, n), tableReuseWarmArray, tableReuseRunArray(n, o.op))
		}
	}
	for _, o := range tableReuseNodeOps {
		for n := 1; n <= 12; n++ {
			for d := 0; d <= n; d++ {
				check(fmt.Sprintf("node %s, keys %d removed %d", o.name, n, d), o.warm, tableReuseRunNode(n, d, o.op))
			}
		}
	}
}
