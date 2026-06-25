//go:build wangshu_p4

package jit

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/value"
)

// TestPJ7_IdentityForm_NoOverwrite identity 形态(`function(x) return x end`)
// writeRetA=false 路径专属 prove-the-path 命中证据。
//
// **背景**:writeRetA=false 路径只有 e2e 隐性覆盖,缺 jit 包内显式正向单测;
// 加本测确保 identity 函数路径(R(A) 不被 mmap 段 dummy RAX 覆盖)有显式
// 命中证据。本测断言:
//   - SetReg 未被调用(R(A) 不被 mmap 段覆盖)
//   - DoReturn 调 1 次(弹帧路径走到)
//
// 这是 [[prove-the-path-under-test]] 实例——避免 writeRetA 逻辑误改后
// identity 函数静默错果(返参 → 返 nil)而 LOADK e2e 测试仍过的盲区。
func TestPJ7_IdentityForm_NoOverwrite(t *testing.T) {
	// `function(x) return x end` 编译为 RETURN 0 2(luac 优化形态,长度 1 / 2)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0), // 真执行:return R(0)
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // dead(luac 尾部冗余)
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if len(host.regs) != 0 {
		t.Errorf("identity 形态不应调 SetReg(R(0) 已是参数值,不应被覆盖),got regs=%v", host.regs)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
	// retA 应是 RETURN 的 A=0
	if host.lastReturnA != 0 {
		t.Errorf("DoReturn A = %d, want 0", host.lastReturnA)
	}
	// retB = 2(返 1 个值)
	if host.lastReturnB != 2 {
		t.Errorf("DoReturn B = %d, want 2", host.lastReturnB)
	}
	// retPC = 0(首条 RETURN 是真执行的,长度 2 luac 优化形态)
	if host.lastReturnPC != 0 {
		t.Errorf("DoReturn PC = %d, want 0", host.lastReturnPC)
	}
}

// TestPJ7_EmptyFunctionForm_NoOverwrite `function() end` 单条 RETURN 0 1
// (B=1,0 返回值)形态,writeRetA=false + retB=1 双重不写槽。
func TestPJ7_EmptyFunctionForm_NoOverwrite(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // RETURN 0 1
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if len(host.regs) != 0 {
		t.Errorf("空函数形态不应调 SetReg, got regs=%v", host.regs)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
	if host.lastReturnB != 1 {
		t.Errorf("DoReturn B = %d, want 1(0 返回值)", host.lastReturnB)
	}
}

// TestPJ7_MoveForm_RetargetA MOVE A B + RETURN A 2 形态:retA 应被设为 B
// (跳过 R(A) = R(B) 中转,直接返 R(B))。
func TestPJ7_MoveForm_RetargetA(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.MOVE, 1, 0, 0), // MOVE 1 0(R(1) = R(0))
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if len(host.regs) != 0 {
		t.Errorf("MOVE+RETURN 形态不应调 SetReg(R(B) 已是参数值), got regs=%v", host.regs)
	}
	// DoReturn 的 A 应是 MOVE 的 B(0,跳过中转直接返 R(0))
	if host.lastReturnA != 0 {
		t.Errorf("DoReturn A = %d, want 0(retA 设为 MOVE B 跳过中转)", host.lastReturnA)
	}
}

// TestPJ7_GetUpvalForm_HostRoute GETUPVAL A B + RETURN A 2 形态:Run 调
// host.GetUpval + SetReg(prelude opcode 路径)。
func TestPJ7_GetUpvalForm_HostRoute(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETUPVAL, 0, 0, 0), // GETUPVAL 0 0
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	gc, host := compileWithHost(t, proto)
	defer tryDispose(t, gc)
	// 预设 upval[0] = NaN-box number 99
	expected := uint64(value.NumberValue(99))
	host.upvals[0] = expected
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	got, ok := host.regs[0]
	if !ok {
		t.Fatal("GETUPVAL 形态:Run 应经 host.SetReg(0, upval[0]) 写 R(0)")
	}
	if got != expected {
		t.Errorf("R(0) = 0x%x, want 0x%x(upval[0] 经 GetUpval 路径写入)", got, expected)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
}

// TestPJ7_ArithForm_HostRoute_OK ADD/SUB/MUL/DIV/MOD/POW + RETURN A 2 形态:
// Run 调 host.Arith helper 完成算术,OK 路径写 R(A) 经 SetReg + DoReturn 弹帧。
//
// **背景**:本批扩 PJ7 支持算术族(`function(x, y) return x + y end` /
// `function(x) return x + 1 end` 等),mmap 段保持 dummy(`mov rax, _; ret`),
// 真值在 Go 端 Run prelude 路径调 host.Arith 完成。mock host 跳过算术语义,
// 只验「prelude 路径调通 + 入参传递正确 + 写 R(A) + DoReturn」机械。
//
// **prove-the-path 命中证据**:若 analyzeShape 误回退到拒 ADD/SUB/...
// (preludeOp 未填),本测立即抓出:arithCalls=0 → R(A) 未被写 → 断言失败。
func TestPJ7_ArithForm_HostRoute_OK(t *testing.T) {
	cases := []struct {
		name string
		op   bytecode.OpCode
	}{
		{"ADD", bytecode.ADD},
		{"SUB", bytecode.SUB},
		{"MUL", bytecode.MUL},
		{"DIV", bytecode.DIV},
		{"MOD", bytecode.MOD},
		{"POW", bytecode.POW},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// op A=2 B=0 C=1 + RETURN A=2 B=2(`function(x,y) return x+y end`
			// 形态:R(2) = R(0) <op> R(1); return R(2))
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(tc.op, 2, 0, 1),
					bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
				},
			}
			c := New()
			host := newMockP4Host()
			c.SetHostState(host)
			expected := uint64(value.NumberValue(42))
			host.arithResult = expected
			gc, err := c.Compile(proto, nil)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}
			defer tryDispose(t, gc)
			stack := make([]uint64, 8)
			if status := gc.Run(stack, 0); status != 0 {
				t.Errorf("Run status = %d, want 0", status)
			}
			if host.arithCalls != 1 {
				t.Errorf("Arith called %d times, want 1", host.arithCalls)
			}
			if host.lastArithOp != int32(tc.op) {
				t.Errorf("Arith op = %d, want %d (%s)", host.lastArithOp, int32(tc.op), tc.name)
			}
			if host.lastArithA != 2 || host.lastArithB != 0 || host.lastArithC != 1 {
				t.Errorf("Arith ABC = (%d,%d,%d), want (2,0,1)", host.lastArithA, host.lastArithB, host.lastArithC)
			}
			got, ok := host.regs[2]
			if !ok {
				t.Fatal("Arith OK 路径:SetReg(2, result) 应被调用(mock 经 arithResult 写)")
			}
			if got != expected {
				t.Errorf("R(2) = 0x%x, want 0x%x(Arith result)", got, expected)
			}
			if host.doReturnCalls != 1 {
				t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
			}
		})
	}
}

// TestPJ7_ArithForm_HostRoute_Err ADD + RETURN 形态 Arith 错误路径:helper
// 返 1 → Run 返 1(ERR)→ enterGibbous 取 pendingErr 冒泡。本测验 Run 不
// 调 DoReturn(ERR 路径绕过弹帧,由 enterGibbous 兜底)。
//
// **背景**:算术族 prelude 引入错误路径(perform arithmetic on
// string/table 等 raise),与 LOADK/MOVE/GETUPVAL 三档「永不错」不同。
// 本测断言错误冒泡路径机械:status != 0 → DoReturn 跳过。
func TestPJ7_ArithForm_HostRoute_Err(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.ADD, 0, 0, 1),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	host.arithRetCode = 1 // 模拟 helper raise
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1(ERR 冒泡)", status)
	}
	if host.arithCalls != 1 {
		t.Errorf("Arith called %d times, want 1", host.arithCalls)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn 应跳过(ERR 路径不经 RETURN),got %d", host.doReturnCalls)
	}
	if _, ok := host.regs[0]; ok {
		t.Errorf("Arith ERR 路径不应写 R(A),got regs=%v", host.regs)
	}
}

// TestPJ7_UnaryForm_HostRoute_OK UNM/LEN A B + RETURN A 2 形态:Run 调
// host.Unm/Len helper,OK 路径写 R(A)。
//
// **背景**:UNM(`function(x) return -x end`)/ LEN(`function(s)
// return #s end`)是 unary 算术 + 长度运算族,host helper 签名 (base, pc,
// b, a) int32 与 Arith 的 (base, pc, op, b, c, a) 不同;analyzeShape 走
// 独立 case + Run 走独立 host helper 接口方法。
//
// **prove-the-path 命中证据**:mock host unaryCalls 计数 + lastUnaryOp tag
// 验证 UNM 调 Unm / LEN 调 Len(不混调)。
func TestPJ7_UnaryForm_HostRoute_OK(t *testing.T) {
	cases := []struct {
		name      string
		op        bytecode.OpCode
		expectTag int32 // mock 内 tag:1=Unm / 2=Len
	}{
		{"UNM", bytecode.UNM, 1},
		{"LEN", bytecode.LEN, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// op A=1 B=0 + RETURN A=1 B=2(`function(x) return op(x) end`
			// 形态:R(1) = op R(0); return R(1))
			proto := &bytecode.Proto{
				Code: []bytecode.Instruction{
					bytecode.EncodeABC(tc.op, 1, 0, 0),
					bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
				},
			}
			c := New()
			host := newMockP4Host()
			c.SetHostState(host)
			expected := uint64(value.NumberValue(99))
			host.unaryResult = expected
			gc, err := c.Compile(proto, nil)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}
			defer tryDispose(t, gc)
			stack := make([]uint64, 4)
			if status := gc.Run(stack, 0); status != 0 {
				t.Errorf("Run status = %d, want 0", status)
			}
			if host.unaryCalls != 1 {
				t.Errorf("unary helper called %d times, want 1", host.unaryCalls)
			}
			if host.lastUnaryOp != tc.expectTag {
				t.Errorf("called wrong unary helper:tag = %d, want %d(%s)",
					host.lastUnaryOp, tc.expectTag, tc.name)
			}
			if host.lastUnaryA != 1 || host.lastUnaryB != 0 {
				t.Errorf("unary BA = (%d,%d), want (0,1)", host.lastUnaryB, host.lastUnaryA)
			}
			got, ok := host.regs[1]
			if !ok {
				t.Fatal("OK 路径:SetReg(1, result) 应被调用")
			}
			if got != expected {
				t.Errorf("R(1) = 0x%x, want 0x%x", got, expected)
			}
			if host.doReturnCalls != 1 {
				t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
			}
		})
	}
}

// TestPJ7_UnaryForm_HostRoute_Err UNM 错误路径(helper 返 1)。
func TestPJ7_UnaryForm_HostRoute_Err(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.UNM, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	host.unaryRetCode = 1
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1(ERR)", status)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn 应跳过(ERR), got %d", host.doReturnCalls)
	}
}

// TestPJ7_NotForm_Rejected NOT 形态当前 P4 不接,analyzeShape 应拒
// (preludeOp 不填,SupportsAllOpcodes 返 false)。承 analyzeShape NOT
// 注释:GetReg 接口未实装,NOT 留 P3 / 解释器处理。本测断言 NOT 形态
// 经 SupportsAllOpcodes 是 false——防回归。
func TestPJ7_NotForm_Rejected(t *testing.T) {
	c := New()
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.NOT, 1, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 1, 2, 0),
		},
	}
	if c.SupportsAllOpcodes(proto) {
		t.Error("NOT 形态当前 P4 不接(GetReg 接口未实装),SupportsAllOpcodes 应返 false")
	}
}

// TestPJ7_NewTableForm_HostRoute NEWTABLE A B C + RETURN A 2 形态:Run 调
// host.NewTable helper,永不 raise。
func TestPJ7_NewTableForm_HostRoute(t *testing.T) {
	// NEWTABLE A=0 B=0 C=0 + RETURN A=0 B=2(`function() return {} end`)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.NEWTABLE, 0, 0, 0),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	expected := uint64(0x1234567890abcdef) // 模拟 NaN-box table ref
	host.tableResult = expected
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.tableCalls != 1 {
		t.Errorf("NewTable called %d times, want 1", host.tableCalls)
	}
	if host.lastTableOp != 1 {
		t.Errorf("called wrong helper:tag = %d, want 1(NewTable)", host.lastTableOp)
	}
	if host.lastTableA != 0 {
		t.Errorf("NewTable A = %d, want 0", host.lastTableA)
	}
	got, ok := host.regs[0]
	if !ok || got != expected {
		t.Errorf("R(0) = 0x%x (ok=%v), want 0x%x", got, ok, expected)
	}
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
}

// TestPJ7_GetTableForm_HostRoute_OK GETTABLE A B C + RETURN A 2 形态:Run 调
// host.GetTable helper,OK 路径写 R(A)。
func TestPJ7_GetTableForm_HostRoute_OK(t *testing.T) {
	// GETTABLE A=2 B=0 C=1 + RETURN A=2 B=2(`function(t, k) return t[k] end`)
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETTABLE, 2, 0, 1),
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	expected := uint64(value.NumberValue(123))
	host.tableResult = expected
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 8)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.tableCalls != 1 || host.lastTableOp != 2 {
		t.Errorf("GetTable not called(calls=%d / tag=%d, want 1/2)", host.tableCalls, host.lastTableOp)
	}
	if host.lastTableA != 2 || host.lastTableB != 0 || host.lastTableC != 1 {
		t.Errorf("GetTable ABC = (%d,%d,%d), want (2,0,1)", host.lastTableA, host.lastTableB, host.lastTableC)
	}
	got, ok := host.regs[2]
	if !ok || got != expected {
		t.Errorf("R(2) = 0x%x (ok=%v), want 0x%x", got, ok, expected)
	}
}

// TestPJ7_GetTableForm_HostRoute_Err GETTABLE 错误路径(helper 返 1)。
func TestPJ7_GetTableForm_HostRoute_Err(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.GETTABLE, 2, 0, 1),
			bytecode.EncodeABC(bytecode.RETURN, 2, 2, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	host.tableRetCode = 1
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 8)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1(ERR)", status)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn 应跳过(ERR), got %d", host.doReturnCalls)
	}
}

// TestPJ7_GetGlobalForm_HostRoute_OK GETGLOBAL A Bx + RETURN A 2 形态。
func TestPJ7_GetGlobalForm_HostRoute_OK(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABx(bytecode.GETGLOBAL, 0, 5),
			bytecode.EncodeABC(bytecode.RETURN, 0, 2, 0),
		},
		Consts:       make([]value.Value, 10), // 占位长度,Bx=5 不越界
		StringLitIdx: make([]int32, 10),
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	expected := uint64(value.NumberValue(7))
	host.tableResult = expected
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 4)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.tableCalls != 1 || host.lastTableOp != 4 {
		t.Errorf("DoGetGlobal not called(calls=%d / tag=%d, want 1/4)", host.tableCalls, host.lastTableOp)
	}
	if host.lastTableA != 0 || host.lastTableB != 5 {
		t.Errorf("DoGetGlobal A/Bx = (%d,%d), want (0,5)", host.lastTableA, host.lastTableB)
	}
	got, ok := host.regs[0]
	if !ok || got != expected {
		t.Errorf("R(0) = 0x%x (ok=%v), want 0x%x", got, ok, expected)
	}
}

// TestPJ7_SetTableForm_HostRoute_OK SETTABLE A B C + RETURN A 1 setter 形态。
// 验证 retB=1 走 prelude(之前 retB>=2 守卫会拦下,新拆分守卫修复)。
func TestPJ7_SetTableForm_HostRoute_OK(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SETTABLE, 0, 1, 2),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0), // setter retB=1
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 8)
	if status := gc.Run(stack, 0); status != 0 {
		t.Errorf("Run status = %d, want 0", status)
	}
	if host.tableCalls != 1 || host.lastTableOp != 3 {
		t.Errorf("SetTable not called(calls=%d / tag=%d, want 1/3)", host.tableCalls, host.lastTableOp)
	}
	if host.lastTableA != 0 || host.lastTableB != 1 || host.lastTableC != 2 {
		t.Errorf("SetTable ABC = (%d,%d,%d), want (0,1,2)", host.lastTableA, host.lastTableB, host.lastTableC)
	}
	// setter 不写 R(A)
	if _, ok := host.regs[0]; ok {
		t.Errorf("setter 不应写 R(A), got regs=%v", host.regs)
	}
	// retB=1 → DoReturn 仍调(弹帧)
	if host.doReturnCalls != 1 {
		t.Errorf("DoReturn 应调 1 次, got %d", host.doReturnCalls)
	}
}

// TestPJ7_SetTableForm_HostRoute_Err SETTABLE 错误路径。
func TestPJ7_SetTableForm_HostRoute_Err(t *testing.T) {
	proto := &bytecode.Proto{
		Code: []bytecode.Instruction{
			bytecode.EncodeABC(bytecode.SETTABLE, 0, 1, 2),
			bytecode.EncodeABC(bytecode.RETURN, 0, 1, 0),
		},
	}
	c := New()
	host := newMockP4Host()
	c.SetHostState(host)
	host.tableRetCode = 1
	gc, err := c.Compile(proto, nil)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	defer tryDispose(t, gc)
	stack := make([]uint64, 8)
	status := gc.Run(stack, 0)
	if status != 1 {
		t.Errorf("Run status = %d, want 1(ERR)", status)
	}
	if host.doReturnCalls != 0 {
		t.Errorf("DoReturn 应跳过(ERR), got %d", host.doReturnCalls)
	}
}
