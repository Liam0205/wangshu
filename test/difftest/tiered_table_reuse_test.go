//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package difftest

import (
	"fmt"
	"strings"
	"testing"
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
// neighbouring table allocated right after it. Whether the second table got the address of the
// first goes into the global REUSED362 and not into the results, since lua5.1 has its own
// allocator.
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
REUSED362 = reused
return first, tostring(r), tostring(rawget(B, 8)), #B, table.concat(N, ",")`,
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
REUSED362 = reused
return first, tostring(r), table.concat(keys, ",")`, n, d, op)
}

var tableReuseNodeOps = []struct{ name, warm, op string }{
	{"read", tableReuseWarmNode, `getk_362(B)`},
	{"store", tableReuseWarmNode, `setk_362(B, 777)`},
	{"method", tableReuseWarmMethod, `getk_362(setmetatable(B, MT362))`},
}

// TestTiered_TableAtAReusedAddress runs each case the four ways TestTiered_ClearWhileTraversing
// does and requires them to agree with each other and with lua5.1. It also requires that in the
// run that warms up first and then forces promotion the second table really landed at the
// address of the first, and that the access to it was the run's one entry into compiled code:
// without the first the run never hands compiled code a different table at a known address,
// without the second it may not hand it to compiled code at all.
//
// What the test cannot see is whether a case really is one the inline checks decide. For the
// array cases it always is: a second table at the first one's address with fewer than eight
// elements is shorter than the frozen index. For the node cases the second table must also end
// up with the generation of the first and with a live field in the node the frozen index names,
// and that depends on the hash function and on how nodes are handed out. With the translation
// from before the fix, 7 of the 90 node cases of each access fail (keys_6..8_removed_3 and
// keys_9..12_removed_2 for the read and the store, keys_6..8_removed_2 and keys_9..12_removed_1
// for the method call); if a change to the table moves those away, the node cases pass without
// testing anything. internal/gibbous/wasm's TestPW5_InlineSlotGuards covers the node checks on
// memory laid out by hand and does not depend on any of this.
func TestTiered_TableAtAReusedAddress(t *testing.T) {
	oracle := findOracle()
	check := func(t *testing.T, warmSrc, runSrc string) {
		tiered := runWarmThenForced(t, oracle, warmSrc, runSrc)
		if got := tiered.GetGlobal("REUSED362").Display(); got != "true" {
			t.Errorf("warm, then force-all: the second table is not at the address of the first (REUSED362 = %s)", got)
		}
		// The run chunk itself is vararg and is never compiled; every accessor is compiled on its
		// first call there, which the interpreter still executes. The access under test is the only
		// second call of an accessor, so it is the one entry into compiled code.
		if n := tiered.GibbousEntries(); n != 1 {
			t.Errorf("warm, then force-all entered compiled code %d times, want 1: the access under test and nothing else", n)
		}
	}
	for _, o := range tableReuseArrayOps {
		for n := 0; n <= 8; n++ {
			t.Run(fmt.Sprintf("array_%s/length_%d", o.name, n), func(t *testing.T) {
				check(t, tableReuseWarmArray, tableReuseRunArray(n, o.op))
			})
		}
	}
	for _, o := range tableReuseNodeOps {
		for n := 1; n <= 12; n++ {
			for d := 0; d <= n; d += 1 {
				t.Run(fmt.Sprintf("node_%s/keys_%d_removed_%d", o.name, n, d), func(t *testing.T) {
					check(t, o.warm, tableReuseRunNode(n, d, o.op))
				})
			}
		}
	}
}
