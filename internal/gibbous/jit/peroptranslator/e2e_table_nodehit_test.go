//go:build wangshu_p4 && wangshu_profile && ((amd64 && linux) || (arm64 && (linux || (darwin && cgo)) && !wangshu_qemu))

// e2e_table_nodehit_test.go — issue #67 (n-body half): GETTABLE/SETTABLE
// NodeHit constant-string-key sites inline in-segment on arm64 (the
// arch where the exit-reason round trip is expensive enough that the
// inline guards pay off; amd64 keeps the exit-reason path — inlining it
// there measured a ~3% table-heavy regression, so it is arm64-only).
//
// These assertions are ARCH-NEUTRAL on purpose: they check the
// correctness contract (a NodeHit string-key access promotes and stays
// byte-equal to the interpreter) which must hold on both arches. On arm64
// the access additionally inlines; that performance effect is measured by
// the benchmark + verified on real arm64 hardware (see the PR), not
// asserted here (a dispatch-count assertion would be arch-specific).

package peroptranslator_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestPJ10_TableNodeHit_Get: a function reading a string-keyed field
// (`t.x`) — GETTABLE with a constant string key, NodeHit IC. Asserts it
// promotes and the result is byte-equal to the interpreter.
func TestPJ10_TableNodeHit_Get(t *testing.T) {
	src := `
local function readx(tbl) return tbl.x + tbl.y + tbl.z end
local p = { x = 3, y = 5, z = 7 }
local acc = 0
for i = 1, 40 do acc = acc + readx(p) end
return acc
`
	prog, err := wangshu.Compile([]byte(src), "pj10getnodehit")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; NodeHit-read function did not promote")
	}
	// (3+5+7) * 40 = 600.
	if len(res) != 1 || res[0].Display() != "600" {
		t.Fatalf("got %v, want [600]", res)
	}
}

// TestPJ10_TableNodeHit_Set: a function writing a string-keyed field
// (`t.total = ...`) — SETTABLE with a constant string key, NodeHit IC,
// overwriting an existing key. Asserts promote + byte-equal.
func TestPJ10_TableNodeHit_Set(t *testing.T) {
	src := `
local function bump(tbl, n)
  for i = 1, n do tbl.total = tbl.total + tbl.step end
end
local p = { total = 0, step = 2 }
for j = 1, 5 do p.total = 0; bump(p, 100) end
return p.total
`
	prog, err := wangshu.Compile([]byte(src), "pj10setnodehit")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() == 0 {
		t.Fatal("PromotionCount = 0; NodeHit-write function did not promote")
	}
	// total = 0 + 2*100 = 200.
	if len(res) != 1 || res[0].Display() != "200" {
		t.Fatalf("got %v, want [200]", res)
	}
}

// TestPJ10_TableNodeHit_ShapeChangeDeopt: correctness under a mid-run
// shape change. A field read is warmed as NodeHit, then the table's shape
// changes (new field added -> gen bump), which must fail the inline
// gen/TableRef guard and route to host.GetTable, staying byte-equal. This
// guards the guard itself (on arm64; on amd64 it always takes the host
// path, still byte-equal).
func TestPJ10_TableNodeHit_ShapeChangeDeopt(t *testing.T) {
	src := `
local function readx(tbl) return tbl.x end
local a = { x = 10 }
local acc = 0
for i = 1, 30 do acc = acc + readx(a) end   -- warm readx as NodeHit
a.y = 99                                      -- shape change: gen bump
for i = 1, 30 do acc = acc + readx(a) end   -- guard must fail -> host path
return acc
`
	prog, err := wangshu.Compile([]byte(src), "pj10nodehitshape")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// acc = 10 * 60 = 600 (x stays 10 across the shape change).
	if len(res) != 1 || res[0].Display() != "600" {
		t.Fatalf("got %v, want [600]", res)
	}
}

// TestPJ10_TableNodeHit_KeyDegradeDeopt: correctness when a warmed
// NodeHit site later sees a DIFFERENT table whose same-slot key differs.
// The NodeKey guard must fail and route to the host path, byte-equal.
func TestPJ10_TableNodeHit_KeyDegradeDeopt(t *testing.T) {
	src := `
local function getname(tbl) return tbl.name end
local a = { name = "alice" }
local b = { other = 1, name = "bob" }
local out = ""
for i = 1, 20 do out = getname(a) end   -- warm NodeHit on a
for i = 1, 20 do out = getname(b) end   -- b: same key, likely different slot/gen
return out
`
	prog, err := wangshu.Compile([]byte(src), "pj10nodehitkey")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetForceAllPromote(true)
	res, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// last call is getname(b) = "bob".
	if len(res) != 1 || res[0].Display() != "bob" {
		t.Fatalf("got %v, want [bob]", res)
	}
}
