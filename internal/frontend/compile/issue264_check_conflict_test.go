package compile

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
)

// TestMultiAssignConflictCopiesLocal pins the register discipline PUC's check_conflict imposes on a
// multi-target assignment whose later LOCAL target is the table or key register of an earlier INDEXED
// target (#264): exactly one `MOVE copy, local` is emitted when that local target is parsed -- before the
// RHS, on the target's line -- and every affected SETTABLE reads the copy instead of the local, so the
// right-to-left stores can overwrite the local first without breaking the indexed store. Without the copy
// `a.x, a = 1, 2` indexed the number 2. Checked against `luac5.1 -p -l` on the same sources; the trailing
// LOADK/MOVE pair for the last constant is our pre-existing opcode-sequence difference (luac loads it
// straight into the target register) and is not pinned here.
func TestMultiAssignConflictCopiesLocal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		src       string
		local     int      // register of the conflicting local
		copies    int      // MOVEs out of that local before the first LOADK (the safe copies)
		settables [][2]int // for each SETTABLE, in order: (A table reg, B key RK); -1 = don't care
		moveLine  int32    // line the copy carries: the local target's own line
	}{
		{"table register", "local t = {} local a = t\na.x, a = 1, 2", 1, 1, [][2]int{{2, 256}}, 2},
		{"key register", "local t = {} local a, b = t, \"k\"\na[b], b = 1, \"z\"", 2, 1, [][2]int{{1, 3}}, 2},
		// The same local as table AND key: both operands move to the one copy.
		{"table and key", "local a = {}\na[a], a = 1, 2", 0, 1, [][2]int{{1, 1}}, 2},
		// Two earlier indexed targets share the local: still one copy, both SETTABLEs use it.
		{"two indexed targets", "local t = {} local a = t\na.x, a.y, a = 1, 2, 3", 1, 1, [][2]int{{2, 257}, {2, 256}}, 2},
		// The local target comes FIRST: PUC only looks back, so no copy and SETTABLE uses t directly.
		{"local target first, no conflict", "local t = {} local a = t\na, t.x = 1, 2", 1, 0, [][2]int{{0, 256}}, 0},
		// Different locals: no copy.
		{"different locals, no conflict", "local t = {} local a, b = t, 0\na.x, b = 1, 2", 2, 0, [][2]int{{1, 257}}, 0},
	} {
		block, err := parse.Parse(lex.New([]byte(tc.src), "z"), "z")
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		mainID, protos, err := Compile(block, "z")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		p := protos[mainID]
		// Locate the assignment statement: everything from the first instruction on line 2.
		start := 0
		for start < len(p.Code) && p.LineInfo[start] != 2 {
			start++
		}
		copies, seenRHS := 0, false
		var settables [][2]int
		for pc := start; pc < len(p.Code); pc++ {
			ins := p.Code[pc]
			switch bytecode.Op(ins) {
			case bytecode.MOVE:
				if !seenRHS && bytecode.B(ins) == tc.local {
					copies++
					if p.LineInfo[pc] != tc.moveLine {
						t.Errorf("%s: copy MOVE at pc=%d on line %d, want %d", tc.name, pc, p.LineInfo[pc], tc.moveLine)
					}
				}
			case bytecode.LOADK, bytecode.CALL:
				seenRHS = true
			case bytecode.SETTABLE:
				settables = append(settables, [2]int{bytecode.A(ins), bytecode.B(ins)})
			}
		}
		if copies != tc.copies {
			t.Errorf("%s: %d safe copies of R(%d), want %d (code %v)", tc.name, copies, tc.local, tc.copies, p.Code[start:])
		}
		if len(settables) != len(tc.settables) {
			t.Fatalf("%s: %d SETTABLE, want %d", tc.name, len(settables), len(tc.settables))
		}
		for i, want := range tc.settables {
			if settables[i] != want {
				t.Errorf("%s: SETTABLE #%d operands (A,B) = %v, want %v", tc.name, i, settables[i], want)
			}
		}
	}
}
