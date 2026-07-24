//go:build wangshu_p4

package jit

import (
	"errors"
	"math"
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// disposable wraps a cast: the bridge.GibbousCode interface has no Dispose, but
// p4Code implements it because holding an mmap segment requires release. Tests
// obtain the package-internal method via a type assertion.
type disposable interface {
	Dispose() error
}

func tryDispose(t *testing.T, gc bridge.GibbousCode) {
	t.Helper()
	if d, ok := gc.(disposable); ok {
		if err := d.Dispose(); err != nil {
			t.Errorf("Dispose failed: %v", err)
		}
	}
}

// mockP4Host is a test double for P4HostState: it records SetReg / DoReturn
// calls so unit tests can assert that the Run path writes values correctly.
//
// The PJ7 real path (p4Code.Run) writes R(retA) via host.SetReg and pops the
// frame via host.DoReturn. Unit tests use this mock to verify "mmap segment
// executes + value fetched + SetReg path reached + value correct".
type mockP4Host struct {
	regs          map[int32]uint64 // written R(idx) → val
	doReturnCalls int
	lastReturnPC  int32
	lastReturnA   int32
	lastReturnB   int32
	upvals        map[int32]uint64 // simulated upvalue table (used by GetUpval)
	// Arith call records:
	arithCalls   int
	lastArithOp  int32
	lastArithB   int32
	lastArithC   int32
	lastArithA   int32
	arithResult  uint64 // simulated "Arith writes back R(A)" value
	arithRetCode int32  // simulated Arith return (0=OK / 1=ERR); preset by unit test
	// Unm/Len call records:
	unaryCalls   int   // shared count for Unm and Len
	lastUnaryOp  int32 // 0=not called / 1=Unm / 2=Len (mock-private tag, not bytecode)
	lastUnaryB   int32
	lastUnaryA   int32
	unaryResult  uint64 // simulated value written back to R(A) by Unm/Len
	unaryRetCode int32
	// NewTable/GetTable call records:
	tableCalls   int   // shared count for NewTable and GetTable
	lastTableOp  int32 // 0=not called / 1=NewTable / 2=GetTable (mock-private tag)
	lastTableA   int32
	lastTableB   int32
	lastTableC   int32
	tableResult  uint64 // simulated value written back to R(A)
	tableRetCode int32  // simulated GetTable return (NewTable is always 0)
	// SetUpvalFromReg call records:
	setUpvalCalls int
	lastSetUpvalA int32
	lastSetUpvalB int32
	// Compare call records:
	cmpCalls  int
	lastCmpOp int32
	lastCmpB  int32
	lastCmpC  int32
	cmpResult bool
	cmpErr    bool
	// CallBaseline call records (PJ5 CALL void form):
	callCalls   int
	lastCallA   int32
	lastCallB   int32
	lastCallC   int32
	lastCallPC  int32
	callRetCode int32 // 0=OK / 1=ERR (preset by unit test)
	// TailCall call records (PJ5 TAILCALL form):
	tailCallCalls   int
	lastTailCallA   int32
	lastTailCallB   int32
	lastTailCallC   int32
	lastTailCallPC  int32
	tailCallRetCode int32 // 0=Lua tail complete / 1=ERR / 2=host falls through to trailing RETURN (preset by unit test)
	// Self call records (PJ5 SELF form):
	selfCalls   int
	lastSelfA   int32
	lastSelfB   int32
	lastSelfC   int32
	lastSelfPC  int32
	selfRetCode int32 // 0=OK / 1=ERR (preset by unit test)
	// PJ2 full-integration groundwork: simulated arena base value
	arenaBase uintptr
}

func newMockP4Host() *mockP4Host {
	return &mockP4Host{
		regs:   make(map[int32]uint64),
		upvals: make(map[int32]uint64),
	}
}

func (m *mockP4Host) DoReturn(base int32, pc int32, a int32, b int32) int32 {
	m.doReturnCalls++
	m.lastReturnPC = pc
	m.lastReturnA = a
	m.lastReturnB = b
	return 0
}

func (m *mockP4Host) SetReg(idx int32, val uint64) {
	m.regs[idx] = val
}

func (m *mockP4Host) GetUpval(base int32, b int32) uint64 {
	_ = base
	return m.upvals[b]
}

// GetReg simulates host.GetReg: reads the mock's regs table (0 if unset).
func (m *mockP4Host) GetReg(idx int32) uint64 {
	return m.regs[idx]
}

// SetUpvalFromReg simulates host.SetUpvalFromReg: a setter that records the
// call and writes upvals[b] = the mock's regs[a].
func (m *mockP4Host) SetUpvalFromReg(base, a, b int32) {
	_ = base
	m.setUpvalCalls++
	m.lastSetUpvalA = a
	m.lastSetUpvalB = b
	m.upvals[b] = m.regs[a]
}

// Arith simulates host.Arith: records the arguments, writes R(a) = arithResult
// via SetReg, and returns arithRetCode (0=OK default / 1=ERR preset by unit
// test). The real host calls doArith; the mock uses preset values to skip the
// arithmetic semantics and only verifies the mechanics of "prelude path wired
// up + error propagation".
func (m *mockP4Host) Arith(base, pc, op, b, c, a int32) int32 {
	_ = base
	_ = pc
	m.arithCalls++
	m.lastArithOp = op
	m.lastArithB = b
	m.lastArithC = c
	m.lastArithA = a
	if m.arithRetCode == 0 {
		m.regs[a] = m.arithResult
	}
	return m.arithRetCode
}

// Unm simulates host.Unm: same mock form as Arith, sharing the three fields
// unaryCalls/unaryResult/unaryRetCode (UNM/LEN are unit-tested in separate
// files, so no distinction is needed).
func (m *mockP4Host) Unm(base, pc, b, a int32) int32 {
	_ = base
	_ = pc
	m.unaryCalls++
	m.lastUnaryOp = 1 // Unm tag
	m.lastUnaryB = b
	m.lastUnaryA = a
	if m.unaryRetCode == 0 {
		m.regs[a] = m.unaryResult
	}
	return m.unaryRetCode
}

// Len simulates host.Len.
func (m *mockP4Host) Len(base, pc, b, a int32) int32 {
	_ = base
	_ = pc
	m.unaryCalls++
	m.lastUnaryOp = 2 // Len tag
	m.lastUnaryB = b
	m.lastUnaryA = a
	if m.unaryRetCode == 0 {
		m.regs[a] = m.unaryResult
	}
	return m.unaryRetCode
}

// Concat simulates host.Concat: reuses the unary* counters (when unit tests
// have no CONCAT-specific assertion).
func (m *mockP4Host) Concat(base, pc, a, b, c int32) int32 {
	_ = base
	_ = pc
	_ = b
	_ = c
	m.unaryCalls++
	m.lastUnaryOp = 3 // Concat tag
	m.lastUnaryA = a
	if m.unaryRetCode == 0 {
		m.regs[a] = m.unaryResult
	}
	return m.unaryRetCode
}

// Eq simulates host.Eq: returns packed bit0=result, bit1=error.
func (m *mockP4Host) Eq(base, pc, b, c int32) int32 {
	_ = base
	_ = pc
	_ = b
	_ = c
	return int32(m.unaryRetCode)
}

// SetList simulates host.SetList: reuses the unary* counters.
func (m *mockP4Host) SetList(base, pc, a, b, c int32) int32 {
	_ = base
	_ = pc
	_ = a
	_ = b
	_ = c
	return int32(m.unaryRetCode)
}

// Closure simulates host.Closure: simplified to always OK.
func (m *mockP4Host) Closure(base, pc, a, bx int32) int32 {
	_ = base
	_ = pc
	_ = a
	_ = bx
	return 0
}

// Close simulates host.Close: always OK.
func (m *mockP4Host) Close(base, pc, a int32) int32 {
	_ = base
	_ = pc
	_ = a
	return 0
}

// TForLoop simulates host.TForLoop: returns -2 (exit) by default.
func (m *mockP4Host) TForLoop(base, pc, a, c int32) int64 {
	_ = base
	_ = pc
	_ = a
	_ = c
	return -2
}

// (mockP4Host.Compare mock is defined further down in this file)

// NewTable simulates host.NewTable: records the call and writes R(A) =
// tableResult. Never raises.
func (m *mockP4Host) NewTable(base, pc, a, b, c int32) int32 {
	_ = base
	_ = pc
	_ = b
	_ = c
	m.tableCalls++
	m.lastTableOp = 1 // NewTable tag
	m.lastTableA = a
	m.regs[a] = m.tableResult
	return 0
}

// GetTable simulates host.GetTable: records the call, writes R(A) =
// tableResult via SetReg, and returns tableRetCode.
func (m *mockP4Host) GetTable(base, pc, a, b, c int32) int32 {
	_ = base
	_ = pc
	m.tableCalls++
	m.lastTableOp = 2 // GetTable tag
	m.lastTableA = a
	m.lastTableB = b
	m.lastTableC = c
	if m.tableRetCode == 0 {
		m.regs[a] = m.tableResult
	}
	return m.tableRetCode
}

// SetTable simulates host.SetTable: as a setter it does not write R(A), only
// records the call and returns tableRetCode.
func (m *mockP4Host) SetTable(base, pc, a, b, c int32) int32 {
	_ = base
	_ = pc
	m.tableCalls++
	m.lastTableOp = 3 // SetTable tag
	m.lastTableA = a
	m.lastTableB = b
	m.lastTableC = c
	return m.tableRetCode
}

// DoGetGlobal simulates host.DoGetGlobal: GETGLOBAL shares the count and
// result with tableCalls/tableResult, but is distinguished by its op tag
// (4=DoGetGlobal).
func (m *mockP4Host) DoGetGlobal(base, pc, a, bx int32) int32 {
	_ = base
	_ = pc
	m.tableCalls++
	m.lastTableOp = 4 // DoGetGlobal tag
	m.lastTableA = a
	m.lastTableB = bx // reuse the B field to record Bx
	if m.tableRetCode == 0 {
		m.regs[a] = m.tableResult
	}
	return m.tableRetCode
}

// DoSetGlobal simulates host.DoSetGlobal: a setter, does not write R(A).
func (m *mockP4Host) DoSetGlobal(base, pc, a, bx int32) int32 {
	_ = base
	_ = pc
	m.tableCalls++
	m.lastTableOp = 5 // DoSetGlobal tag
	m.lastTableA = a
	m.lastTableB = bx
	return m.tableRetCode
}

// Compare simulates host.Compare: returns cmpResult|cmpErr (packed bit0=result
// / bit1=err); the mock controls each separately via the cmpResult/cmpErr
// fields.
func (m *mockP4Host) Compare(base, pc, op, b, c int32) int32 {
	_ = base
	_ = pc
	m.cmpCalls++
	m.lastCmpOp = op
	m.lastCmpB = b
	m.lastCmpC = c
	if m.cmpErr {
		return 2
	}
	if m.cmpResult {
		return 1
	}
	return 0
}

// ArenaBaseAddr simulates host.ArenaBaseAddr: returns the mock's arenaBase
// field (the PJ7 simplified form does not actually use it; this stub merely
// completes the interface).
func (m *mockP4Host) ArenaBaseAddr() uintptr { return m.arenaBase }

// ValueStackBaseAddr simulates host.ValueStackBaseAddr: returns arenaBase + base.
func (m *mockP4Host) ValueStackBaseAddr(base int32) uintptr {
	return m.arenaBase + uintptr(base)
}

// RefreshJitCtxAddrs mirrors the batched setter: mock fills arenaBase +
// derived valueStackBase (rest 0, matching the individual mock getters).
func (m *mockP4Host) RefreshJitCtxAddrs(ctx *JITContext, base int32) {
	ctx.SetAllAddrs(m.arenaBase, m.arenaBase+uintptr(base), 0, 0, 0)
}

// CIDepthHostAddr simulates host.CIDepthHostAddr (per §9.20 Option B Spike 1):
// returns a fixed placeholder address in the mock; unit-test paths never
// actually reach the byte-level inc/dec.
func (m *mockP4Host) CIDepthHostAddr() uintptr { return 0 }

// CISegBaseHostAddr simulates host.CISegBaseHostAddr (per §9.20).
func (m *mockP4Host) CISegBaseHostAddr() uintptr { return 0 }

// TopHostAddr simulates host.TopHostAddr (per §9.20).
func (m *mockP4Host) TopHostAddr() uintptr { return 0 }

// ExecuteCalleeFromInlineFrame mock stub (per §9.20.9 commit-2 + commit-5l/5p/5q signature fix).
// Unit-test paths never reach it (archSupportsFrameInline=false blocks the real
// call), so it returns 0=OK as a fallback.
func (m *mockP4Host) ExecuteCalleeFromInlineFrame(base, callA, callArgCount, nresults int32) int32 {
	_ = base
	_ = callA
	_ = callArgCount
	_ = nresults
	return 0
}

// ForPrep mock stub (for the PJ3 reg-limit deopt path; unit-test paths never reach it).
func (m *mockP4Host) ForPrep(base, pc, a int32) int32 { _ = base; _ = pc; _ = a; return 0 }

// ForLoop mock stub (issue #177 deopt path completes the empty-body loop;
// unit-test paths never reach it, but the interface must be satisfied).
func (m *mockP4Host) ForLoop(base, pc, a int32) int32 { _ = base; _ = pc; _ = a; return 0 }

// LoopPreempt mock stub (issue #102 loop back-edge fuel): unit tests
// never arm a budget, so refill unlimited and report OK.
func (m *mockP4Host) LoopPreempt(ctx *JITContext, base, pc int32) int32 {
	_ = base
	_ = pc
	if ctx != nil {
		ctx.SetLoopFuel(SegCallFuelUnlimited)
	}
	return 0
}

// ObserveCallCallee mock stub (issue #50 Spike 1): returns zero so the
// per-CALL-site IC populate is a no-op in unit tests. Real observation
// is exercised via the end-to-end crescent tests.
func (m *mockP4Host) ObserveCallCallee(base, a int32) uint64 { _ = base; _ = a; return 0 }

// NativeCalleeSegAddr mock stub (issue #50 Spike 5): returns 0 so no
// segment-to-segment dispatch is attempted in unit tests.
func (m *mockP4Host) NativeCalleeSegAddr(protoID uint32) uint64 { _ = protoID; return 0 }

// CalleeNeverExitsSegment mock stub (issue #50 Spike 5): returns false.
func (m *mockP4Host) CalleeNeverExitsSegment(protoID uint32) bool { _ = protoID; return false }
func (m *mockP4Host) CalleeSeg2SegRetCount(protoID uint32) int32  { _ = protoID; return -1 }

// ExecutePlainCallInlineFrame mock stub (issue #50 Spike 2): unit
// tests don't emit the CALL EmitCallInline path (the segment guard is
// gated on IC + arch flags), so this stub returns 0=OK as a safety net.
func (m *mockP4Host) ExecutePlainCallInlineFrame(base, callA, nargs, nresults int32) int32 {
	_ = base
	_ = callA
	_ = nargs
	_ = nresults
	return 0
}

// CallBaseline simulates host.CallBaseline: records the arguments and returns the preset callRetCode.
func (m *mockP4Host) CallBaseline(base, pc, a, b, c int32) int32 {
	_ = base
	m.callCalls++
	m.lastCallPC = pc
	m.lastCallA = a
	m.lastCallB = b
	m.lastCallC = c
	return m.callRetCode
}

// TailCall simulates host.TailCall: records the arguments and returns the preset tailCallRetCode (three-valued).
func (m *mockP4Host) TailCall(base, pc, a, b, c int32) int32 {
	_ = base
	m.tailCallCalls++
	m.lastTailCallPC = pc
	m.lastTailCallA = a
	m.lastTailCallB = b
	m.lastTailCallC = c
	return m.tailCallRetCode
}

// GlobalsRaw mocks host.GlobalsRaw. Zero disables the GETGLOBAL /
// SETGLOBAL NodeHit inline fast path (emits fall back to exit-reason).
func (m *mockP4Host) GlobalsRaw() uint64 { return 0 }

// Self simulates host.Self: records the arguments and returns the preset selfRetCode.
// It is the mock double for the byte-equal interpreter's SELF segment
// (R(A+1)=R(B) self + R(A)=R(B)[RK(C)] method); unit tests do not actually run
// the table IC + __index metamethod chain.
func (m *mockP4Host) Self(base, pc, a, b, c int32) int32 {
	_ = base
	m.selfCalls++
	m.lastSelfPC = pc
	m.lastSelfA = a
	m.lastSelfB = b
	m.lastSelfC = c
	return m.selfRetCode
}

// compileWithHost constructs a *Compiler, injects the mock host, and then calls Compile.
func compileWithHost(t *testing.T, p *bytecode.Proto) (bridge.GibbousCode, *mockP4Host) {
	t.Helper()
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	gc, err := c.Compile(p, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	return gc, host
}

// `docs/design/p4-method-jit/00-overview.md` §4 + `06-backends.md` §6.1):
//
//   - SupportsAllOpcodes is all false (the supported table starts empty, per the
//     06 §3.8 progressive-whitelist discipline);
//   - Compile returns a real GibbousCode for the single LOADK+RETURN shape (PJ2
//     real path); other shapes return ErrCompileUnsupportedShape;
//   - it implements the bridge.P3Compiler interface (the compile-time assertion
//     is already in code.go; this test verifies it once more at runtime, as
//     prove-the-path evidence).

// TestPJ0_NewReturnsCompiler constructs a non-nil Compiler.
func TestPJ0_NewReturnsCompiler(t *testing.T) {
	c := New()
	if c == nil {
		t.Fatal("PJ0: New() should return non-nil Compiler")
	}
}

// TestPJ0_ImplementsP3Compiler implements the bridge.P3Compiler interface.
func TestPJ0_ImplementsP3Compiler(t *testing.T) {
	c := New()
	var iface bridge.P3Compiler = c
	if iface == nil {
		t.Fatal("PJ0: Compiler should satisfy bridge.P3Compiler")
	}
}

// TestPJ7_SupportsAllOpcodesGate PJ7 real path: the LOADK+RETURN single-BB shape
// returns true, others return false.
func TestPJ7_SupportsAllOpcodesGate(t *testing.T) {
	c := New()
	rejectCases := []struct {
		name string
		p    *bytecode.Proto
	}{
		{
			name: "nil",
			p:    nil,
		},
		{
			name: "empty",
			p:    &bytecode.Proto{Code: []bytecode.Instruction{}},
		},
		{
			name: "MOVE+RETURN A 不一致 + B=1(MOVE 形态需 A 一致 + B=2)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
				},
			},
		},
		{
			name: "LOADK+RETURN B!=2",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // B=1 returns no value
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
		{
			name: "LOADK+RETURN A 不一致",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
	}
	for _, tc := range rejectCases {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			if c.SupportsAllOpcodes(tc.p) {
				t.Errorf("PJ7: %q should NOT be supported", tc.name)
			}
		})
	}

	acceptCase := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts:       []value.Value{value.NumberValue(42)},
		StringLitIdx: []int32{-1},
	}
	if !c.SupportsAllOpcodes(acceptCase) {
		t.Error("PJ7: LOADK+RETURN single-BB shape should be supported")
	}
}

// TestPJ2_CompileLoadKReturnSucceeds PJ2 real-path evidence: for the "LOADK A
// K(0); RETURN A 1" shape, Compile emits a real mmap segment + wraps a *p4Code,
// and Run fetches the value via callJITFull and writes it back to the R(A) slot
// on the stack.
//
// **prove-the-path evidence** (per
// `llmdoc/guides/prove-the-path-under-test.md`): this test goes through the real
// Compile path → real mmap segment → callJITFull → stack write-back, a white-box
// proof that:
//  1. the emitter path is reached (Compile calls EmitMovRaxImm64 + EmitRet);
//  2. mmap+W^X flipping works (MmapCode returns a *CodePage);
//  3. callJITFull jumps into the segment + the in-segment mov+ret works (RAX = NaN-box const);
//  4. p4Code.Run writes the correct NaN-box value back to the stack.
//
// Note: **SupportsAllOpcodes is still all false** ⇒ this path is not taken by the
// bridge main path; this test is PJ2-internal prove-the-path, verifying the mmap
// segment is really reached + the value is correct.
func TestPJ2_CompileLoadKReturnSucceeds(t *testing.T) {
	cases := []struct {
		name  string
		konst value.Value
	}{
		{"number 0", value.NumberValue(0)},
		{"number 1", value.NumberValue(1)},
		{"number 3.14", value.NumberValue(3.14)},
		{"number -1", value.NumberValue(-1)},
		{"number Inf", value.NumberValue(math.Inf(1))},
		{"nil", value.Nil},
		{"bool true", value.BoolValue(true)},
		{"bool false", value.BoolValue(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Proto:LOADK 0 K(0); RETURN 0 2(R(0) = K(0); return R(0)).
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
				},
				Consts:       []value.Value{tc.konst},
				StringLitIdx: []int32{-1}, // non-string placeholder
			}
			gc, host := compileWithHost(t, proto)
			defer tryDispose(t, gc)

			// Execute via the real Run path (host.SetReg receives the R(retA) value).
			stack := make([]uint64, 4)
			status := gc.Run(stack, 0)
			if status != 0 {
				t.Errorf("Run status = %d, want 0(OK)", status)
			}
			// host.SetReg(0, val) should have been called, with val == tc.konst
			got, ok := host.regs[0]
			if !ok {
				t.Fatal("SetReg(0, ...) not called")
			}
			if value.Value(got) != tc.konst {
				t.Errorf("SetReg(0, 0x%016x), want 0x%016x (%v)", got, uint64(tc.konst), tc.konst)
			}
			if host.doReturnCalls != 1 {
				t.Errorf("DoReturn called %d times, want 1", host.doReturnCalls)
			}
		})
	}
}

// TestPJ7_CompileLoadKStringConst PJ7 extension: the LOADK string-constant shape
// (the path where IsStringConst actually returns true).
//
// **Background**: previously the PJ7 shape's analyzeShape hard-rejected when
// IsStringConst=true—conservatively, out of concern that a string ref would not
// be stable within the jit package's unit-test domain. But under the real
// LoadProgram path `proto.Consts[bx]` is already a NaN-box
// `MakeGC(TagString, intern_ref)` (written via `gc.Intern` in the
// `state.go::LoadProgram` §private Consts segment), sharing the same source as
// number/nil/bool—the string ref is kept alive by `State.strRefs` (an R6 root)
// registered via `LoadProgram` and scanned into the collector via
// `visitProgramStringRefs`; **not** via `proto.Consts` itself. p4Code holding
// proto only keeps proto's lifetime; it is decoupled from the string-ref
// keep-alive mechanism but shares the same lifetime. The mmap segment can just
// emit `mov rax, u64; ret`.
//
// This test asserts: Compile accepts a Proto with IsStringConst=true, and Run
// writes back R(0) = the fake string NaN-box (the payload is not dereferenced
// within the jit package; only value passthrough correctness is verified).
//
// **prove-the-path evidence**: same mmap segment path as number/nil/bool, but
// taking the IsStringConst=true branch—if analyzeShape ever reverts to the
// "IsStringConst hard-reject", this test catches it immediately.
func TestPJ7_CompileLoadKStringConst(t *testing.T) {
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	// IsStringConst returning true requires StringLitIdx[0] >= 0 (per the
	// bytecode.Proto IsStringConst implementation)
	fakeStrRef := value.MakeGC(value.TagString, 0x789abc)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts:       []value.Value{fakeStrRef},
		StringLitIdx: []int32{0}, // IsStringConst(0) = true
	}
	if !proto.IsStringConst(0) {
		t.Fatal("test setup: IsStringConst(0) should be true(StringLitIdx[0]=0)")
	}
	if !c.SupportsAllOpcodes(proto) {
		t.Fatal("PJ7: LOADK string const + RETURN should be supported (IsStringConst 硬拒已撤回)")
	}
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0(OK)", status)
	}
	got, ok := host.regs[0]
	if !ok {
		t.Fatal("SetReg(0, ...) not called")
	}
	if got != uint64(fakeStrRef) {
		t.Errorf("SetReg(0, 0x%x), want 0x%x(string NaN-box passthrough)", got, uint64(fakeStrRef))
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn called %d times, want 1", host.doReturnCalls)
	}
}

// TestPJ2_CompileLoadKReturnRetANonZero the retA != 0 shape (R(2) = K(0); return R(2)).
//
// Verifies the retA field is passed correctly + Run writes to the correct slot (via mock host.SetReg).
func TestPJ2_CompileLoadKReturnRetANonZero(t *testing.T) {
	konst := value.NumberValue(42)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 2, 0),
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
		},
		Consts:       []value.Value{konst},
		StringLitIdx: []int32{-1},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	stack := make([]uint64, 8)
	status := gc.Run(stack, 0)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got, ok := host.regs[2]
	if !ok {
		t.Fatal("SetReg(2, ...) not called")
	}
	if value.Value(got) != konst {
		t.Errorf("SetReg(2, 0x%x), want 0x%x", got, uint64(konst))
	}
	if _, ok := host.regs[0]; ok {
		t.Errorf("SetReg(0, ...) should NOT be called for retA=2")
	}
}

// TestPJ2_CompileBaseNonZero the base != 0 shape (simulates a nested call frame).
//
// SetReg takes an idx, and the Run path no longer depends on the base argument
// (the host computes the real location via thread.cur.base). This test verifies
// retA is unchanged when passed to the host via SetReg.
func TestPJ2_CompileBaseNonZero(t *testing.T) {
	konst := value.NumberValue(99)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts:       []value.Value{konst},
		StringLitIdx: []int32{-1},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)

	stack := make([]uint64, 8)
	status := gc.Run(stack, 16) // base = 16 bytes (p4Code.Run no longer reads the base argument; SetReg does not depend on it)
	if status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got, ok := host.regs[0]
	if !ok {
		t.Fatal("SetReg(0, ...) not called")
	}
	if value.Value(got) != konst {
		t.Errorf("SetReg(0, 0x%x), want 0x%x", got, uint64(konst))
	}
}

// TestPJ2_CompileRejectsNonShape rejects shapes other than LOADK+RETURN single-BB
// (per Compile's shape check).
func TestPJ2_CompileRejectsNonShape(t *testing.T) {
	c := New()
	cases := []struct {
		name string
		p    *bytecode.Proto
	}{
		{
			name: "nil",
			p:    nil,
		},
		{
			name: "empty code",
			p:    &bytecode.Proto{Code: []bytecode.Instruction{}},
		},
		{
			name: "single RETURN B!=1(2 个返回值,不在 PJ7 形态内)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.RETURN, 0, 3, 0), // B=3 returns 2 values
				},
			},
		},
		{
			name: "MOVE+RETURN A 不一致 + B=1(MOVE 形态需 A 一致 + B=2,本 case 双重不命中)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
				},
			},
		},
		{
			name: "LOADK+JMP(JMP 不在 PJ2 范围)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeAsBx(bytecode.JMP, 0, 0),
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
		{
			name: "LOADK+RETURN(retA != loadA)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0), // retA=1 ≠ loadA=0
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
		{
			name: "LOADK+RETURN(B != 2)",
			p: &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABx(bytecode.LOADK, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // B=1 returns no value
				},
				Consts:       []value.Value{value.NumberValue(0)},
				StringLitIdx: []int32{-1},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gc, err := c.Compile(tc.p, nil)
			if !errors.Is(err, ErrCompileUnsupportedShape) {
				t.Errorf("Compile should return ErrCompileUnsupportedShape, got %v", err)
			}
			if gc != nil {
				t.Errorf("Compile should return nil GibbousCode on unsupported shape, got %v", gc)
			}
		})
	}
}

// TestPJ2_CompileToleratesNilFeedback nil feedback does not panic (per the
// P3Compiler interface contract).
func TestPJ2_CompileToleratesNilFeedback(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Compile must tolerate nil feedback (P3Compiler 接口契约), panicked: %v", r)
		}
	}()
	c := New()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.LOADK, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts:       []value.Value{value.NumberValue(0)},
		StringLitIdx: []int32{-1},
	}
	gc, _ := c.Compile(proto, nil)
	if gc != nil {
		tryDispose(t, gc)
	}
}
