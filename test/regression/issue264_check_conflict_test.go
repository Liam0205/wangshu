package regression

import (
	"testing"

	"github.com/Liam0205/wangshu/test/testutil"
)

// TestMultiAssignLocalConflictUsesSafeCopy covers #264: in a multi-target assignment PUC's check_conflict
// (lparser.c) protects an indexed target whose table or key register is a local that a LATER target in the
// same statement overwrites. Stores run right to left, so without the copy `a.x, a = 1, 2` assigns a = 2
// first and then indexes the number. PUC copies the local into a fresh register (one MOVE) and points the
// indexed target at the copy, so t.x is 1 and a is 2; every case here is the lua5.1 answer.
func TestMultiAssignLocalConflictUsesSafeCopy(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"table register conflicts with later local target",
			"local t = {} local a = t a.x, a = 1, 2 return tostring(t.x) .. ',' .. tostring(a)", "1,2"},
		{"key register conflicts with later local target",
			"local t = {} local a, b = t, 'k' a[b], b = 1, 'z' return tostring(t.k) .. ',' .. tostring(b)", "1,z"},
		{"table register conflicts, bracket key",
			"local b = {} b[1], b = 1, 2 return tostring(b) .. ',' .. type(b)", "2,number"},
		{"same local used as table and key",
			"local a = {} local keep = a a[a], a = 1, 2 return tostring(keep[keep]) .. ',' .. tostring(a)", "1,2"},
		{"two indexed targets share the conflicting local",
			"local t = {} local a = t a.x, a.y, a = 1, 2, 3 return t.x .. ',' .. t.y .. ',' .. tostring(a)", "1,2,3"},
		{"conflict with a local target that is not the last",
			"local t = {} local a = t a.x, a, y = 1, 2, 3 return tostring(t.x) .. ',' .. tostring(a) .. ',' .. tostring(y)", "1,2,3"},
		{"no conflict: local target before the indexed one",
			"local t = {} local a = t a, t.x = 1, 2 return tostring(a) .. ',' .. tostring(t.x)", "1,2"},
		{"no conflict: different locals",
			"local t = {} local a, b = t, 0 a.x, b = 1, 2 return tostring(t.x) .. ',' .. tostring(b)", "1,2"},
		{"conflict with call RHS",
			"local t = {} local a = t local function f() return 1, 2 end a.x, a = f() return tostring(t.x) .. ',' .. tostring(a)", "1,2"},
		{"conflict inside a closure body (upvalue table is not a local)",
			"local t = {} local function g() local a = t a.x, a = 1, 2 return tostring(t.x) .. ',' .. tostring(a) end return g()", "1,2"},
	} {
		if got := testutil.RunOne(t, tc.src).Str(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
