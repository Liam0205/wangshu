package difftest

import (
	"fmt"
	"strings"
	"testing"
)

// tableReuseInterpScript reads or stores a field through a function whose inline cache was
// filled on a table that has since been collected. A has na fields; B, built after the
// collection, has nb fields and not the one the function names; C has that field and nc more.
// B is allocated where A was and starts from the same generation, and for some sizes C's nodes
// lie right behind B's, where the node index cached for A points. The second result says
// whether B is at the address A had.
func tableReuseInterpScript(na, nb, nc int, store bool) string {
	list := func(prefix string, n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf("%s%d = %d", prefix, i+1, i+1)
		}
		return strings.Join(parts, ", ")
	}
	field := fmt.Sprintf("a%d", na)
	op := `local r = tostring(get(B))`
	if store {
		op = fmt.Sprintf(`set(B, 777) local r = tostring(rawget(B, %q)) .. "/" .. tostring(C.%s)`, field, field)
	}
	return fmt.Sprintf(`
local function get(t) return t.%[1]s end
local function set(t, v) t.%[1]s = v end
local A = {%[2]s}
get(A) set(A, %[5]d)
local addr = tostring(A)
A = nil
collectgarbage() collectgarbage()
local B = {%[3]s}
local C = {%[1]s = "other table", %[4]s}
%[6]s
return r, tostring(B) == addr`, field, list("a", na), list("b", nb), list("c", nc), na, op)
}

// TestDiff_FieldOfATableAtAReusedAddress runs the script for a range of sizes on the default
// State, where the interpreter executes it under every build. B never has the field, so the
// read gives nil and the store lands in B and leaves C alone; nb == 1 with nc == 0 is compared
// with lua5.1 as well. The script reports whether B got A's address, and the test requires it
// for every size. Whether a node of C then sits where the cached index points cannot be told
// from a script: internal/crescent's TestIC_NodeHitOnATableAtAReusedAddress looks at the arena
// and requires that layout for its own script.
func TestDiff_FieldOfATableAtAReusedAddress(t *testing.T) {
	oracle := findOracle()
	for na := 2; na <= 8; na++ {
		for nb := 0; nb <= 2; nb++ {
			for nc := 0; nc <= 7; nc++ {
				for _, store := range []bool{false, true} {
					src := tableReuseInterpScript(na, nb, nc, store)
					want := "nil"
					if store {
						want = "777/other table"
					}
					got, reused, _ := strings.Cut(strings.TrimSuffix(runWangshu(t, src), "\n"), "\t")
					if got != want {
						t.Errorf("A with %d fields, B with %d, C with %d more, store %v: got %q, want %q", na, nb, nc, store, got, want)
					}
					if reused != "true" {
						t.Errorf("A with %d fields, B with %d, C with %d more, store %v: B is not at the address A had", na, nb, nc, store)
					}
					if oracle != "" && nb == 1 && nc == 0 {
						// lua5.1 has its own allocator; only the access is compared.
						got, _, _ := strings.Cut(strings.TrimSuffix(runOracle(t, oracle, wrapForOracle(src)), "\n"), "\t")
						if got != want {
							t.Errorf("lua5.1, A with %d fields, store %v: got %q, want %q", na, store, got, want)
						}
					}
				}
			}
		}
	}
}
