//go:build wangshu_p3 && wangshu_profile

// PW2-d 端到端验收(docs/design/p3-wasm-tier/04-trampoline.md §2 + 08 §V):
// crescent doCall 检测到 Proto 已升 gibbous(wazero wasm)时经 trampoline
// 跳 wazero 执行,返回值经共见值栈(arena=linear memory,VS0-c)回填,与
// 解释器执行逐字节一致。
//
// 仅 wangshu_p3 && wangshu_profile build 跑:p3 提供真 gibbous Compiler +
// 收养 wazero memory;profile 启用 considerPromotion 升层路径。
//
// **升层驱动**:compile 期 AnalyzeProto 无 P3 注入恒标 NotCompilable(见
// frontend/compile/analyze_on.go);运行期自动重分析留后续(需 AST 保留)。
// 本测试按 analyze_on.go §对 PB7 影响 所述手工 SetCompilability(模拟「真
// P3 下 F7 放行」)+ 驱动 OnEnter 越阈值触发真升层,测真 trampoline 路径。
package crescent

import (
	"testing"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/frontend/compile"
	"github.com/Liam0205/wangshu/internal/frontend/lex"
	"github.com/Liam0205/wangshu/internal/frontend/parse"
	"github.com/Liam0205/wangshu/internal/object"
	"github.com/Liam0205/wangshu/internal/value"
)

// loadFn 编译 src 为 Program,装载主 chunk,返回 State + 主 closure。
func loadFn(t *testing.T, src string) (*State, value.Value) {
	t.Helper()
	lx := lex.New([]byte(src), "pw2d")
	block, err := parse.Parse(lx, "pw2d")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mainID, protos, err := compile.Compile(block, "pw2d")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := New()
	cl := st.LoadProgram(mainID, protos)
	return st, value.MakeGC(value.TagFunction, cl)
}

// promoteProto 手工驱动一个 Proto 走真升层路径(SetCompilability + OnEnter
// 越阈值 → considerPromotion → 真 gibbous Compile + installGibbous)。
// 返回是否成功升层(SupportsAllOpcodes 不支持的形状会 Stuck,返 false)。
func promoteProto(st *State, pid uint32) bool {
	proto := st.protos[pid]
	b := st.bridge
	b.SetCompilability(proto, bridge.CompCompilable, 0)
	for i := uint32(0); i < bridge.HotEntryThreshold+1; i++ {
		b.OnEnter(proto, true)
	}
	return b.GibbousCodeOf(proto) != nil
}

// TestPW2d_IdentityReturn 端到端:`local function id(x) return x end` 升 gibbous
// 后,id(v) 经 trampoline 跳 wazero 执行,返回值与解释器逐字节一致。
func TestPW2d_IdentityReturn(t *testing.T) {
	src := `
local function id(x) return x end
return id
`
	st, mainCl := loadFn(t, src)
	// 跑主 chunk 拿到 id closure(主 chunk 返回 id)。
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	idVal := rets[0]
	if value.Tag(idVal) != value.TagFunction {
		t.Fatalf("expected function, got %v", idVal)
	}
	idProto := object.ClosureProtoID(st.arena, value.GCRefOf(idVal))

	// 升层前先记解释器结果(byte-equal 基线)。
	want := float64(12345)
	base, e := st.Call(value.GCRefOf(idVal), []value.Value{value.NumberValue(want)}, 1)
	if e != nil {
		t.Fatalf("interp call: %v", e)
	}
	if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != want {
		t.Fatalf("interp id(%v) = %v, want %v", want, base[0], want)
	}

	// 驱动真升层。
	if !promoteProto(st, idProto) {
		t.Skip("id proto not supported by current gibbous whitelist (SupportsAllOpcodes false)")
	}

	// 升层后调用:经 trampoline 跳 wazero。结果须 byte-equal。
	got, e2 := st.Call(value.GCRefOf(idVal), []value.Value{value.NumberValue(want)}, 1)
	if e2 != nil {
		t.Fatalf("gibbous call: %v", e2)
	}
	if len(got) != 1 || !value.IsNumber(got[0]) || value.AsNumber(got[0]) != want {
		t.Errorf("gibbous id(%v) = %v, want %v (byte-equal with interp)", want, got[0], want)
	}

	// 多次调用:wazero module 复用稳定(共见值栈每次 base 偏移正确)。
	for _, v := range []float64{0, -1, 3.14, 1e9} {
		r, e := st.Call(value.GCRefOf(idVal), []value.Value{value.NumberValue(v)}, 1)
		if e != nil {
			t.Fatalf("gibbous id(%v): %v", v, e)
		}
		if !value.IsNumber(r[0]) || value.AsNumber(r[0]) != v {
			t.Errorf("gibbous id(%v) = %v, want %v", v, r[0], v)
		}
	}
}

// TestPW2d_ConstReturn `local function k() return 42 end`:LOADK + RETURN
// 升 gibbous 后返回数字常量,byte-equal。
func TestPW2d_ConstReturn(t *testing.T) {
	src := `
local function k() return 42 end
return k
`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	kVal := rets[0]
	kProto := object.ClosureProtoID(st.arena, value.GCRefOf(kVal))

	if !promoteProto(st, kProto) {
		t.Skip("k proto not supported by gibbous whitelist")
	}
	got, e := st.Call(value.GCRefOf(kVal), nil, 1)
	if e != nil {
		t.Fatalf("gibbous k(): %v", e)
	}
	if len(got) != 1 || !value.IsNumber(got[0]) || value.AsNumber(got[0]) != 42 {
		t.Errorf("gibbous k() = %v, want 42", got[0])
	}
}

// TestPW2d_PromotionHappened 验证升层真发生(TierGibbous + GibbousCode 装载),
// 否则上面两个测试可能因 Skip 假阳性通过。
func TestPW2d_PromotionHappened(t *testing.T) {
	src := `
local function id(x) return x end
return id
`
	st, mainCl := loadFn(t, src)
	rets, _ := st.Call(value.GCRefOf(mainCl), nil, 1)
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(rets[0]))
	if !promoteProto(st, pid) {
		t.Fatal("id(x) return x 应能升 gibbous(单 BB + RETURN),但 SupportsAllOpcodes 拒了")
	}
	if st.bridge.GibbousCodeOf(st.protos[pid]) == nil {
		t.Fatal("升层后 GibbousCodeOf 应返回非 nil")
	}
}

// TestPW3_ArithE2E `local function f(a,b) return a+b end`:ADD 双 number 快路径
// 升 gibbous 后经 trampoline 跳 wazero,结果与解释器逐字节一致。
func TestPW3_ArithE2E(t *testing.T) {
	cases := []struct {
		name string
		src  string
		args []value.Value
		want float64
	}{
		{"add", `local function f(a,b) return a+b end; return f`,
			[]value.Value{value.NumberValue(3), value.NumberValue(4)}, 7},
		{"sub", `local function f(a,b) return a-b end; return f`,
			[]value.Value{value.NumberValue(10), value.NumberValue(3)}, 7},
		{"muladd", `local function f(a,b) return a*b end; return f`,
			[]value.Value{value.NumberValue(6), value.NumberValue(7)}, 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, mainCl := loadFn(t, tc.src)
			rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
			if err != nil {
				t.Fatalf("run main: %v", err)
			}
			fVal := rets[0]
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))

			// 解释器基线。
			base, e := st.Call(value.GCRefOf(fVal), tc.args, 1)
			if e != nil {
				t.Fatalf("interp call: %v", e)
			}
			if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp f = %v, want %v", base[0], tc.want)
			}

			if !promoteProto(st, pid) {
				t.Skipf("%s proto not supported by gibbous whitelist", tc.name)
			}
			// 升层后:经 trampoline 跳 wazero,byte-equal。
			got, e2 := st.Call(value.GCRefOf(fVal), tc.args, 1)
			if e2 != nil {
				t.Fatalf("gibbous call: %v", e2)
			}
			if len(got) != 1 || !value.IsNumber(got[0]) || value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous f = %v, want %v (byte-equal)", got[0], tc.want)
			}
		})
	}
}

// TestPW3_ArithSlowPathE2E 混合类型(string coercion)走慢路径助手 h_arith,
// 与解释器 doArithSlow 逐字节一致。
func TestPW3_ArithSlowPathE2E(t *testing.T) {
	// "10" + 5:string coercion → 15(Lua 5.1 算术 coercion)
	src := `local function f(a,b) return a+b end; return f`
	st, mainCl := loadFn(t, src)
	rets, _ := st.Call(value.GCRefOf(mainCl), nil, 1)
	fVal := rets[0]
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	if !promoteProto(st, pid) {
		t.Skip("proto not supported")
	}
	// "10" 是字符串:gibbous ADD 快路径 IsNumber 失败 → h_arith 慢路径 coercion。
	strV := value.MakeGC(value.TagString, st.gc.Intern([]byte("10")))
	got, e := st.Call(value.GCRefOf(fVal), []value.Value{strV, value.NumberValue(5)}, 1)
	if e != nil {
		t.Fatalf("gibbous slow-path call: %v", e)
	}
	if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != 15 {
		t.Errorf(`gibbous f("10",5) = %v, want 15 (string coercion via h_arith)`, got[0])
	}
}

// TestPW5a_GlobalIC PW5-a:GETGLOBAL/SETGLOBAL inline IC 快照固化。
// 升层前跑解释器基线填 IC(NodeHit)→ 升层 inline 快路径(同 gen 直达 node 槽
// 跳哈希)→ byte-equal。失效路径:新增全局触发 globals rehash → gen bump →
// inline gen 校验失败 → 走 h_getglobal/h_setglobal 助手仍正确。
func TestPW5a_GlobalIC(t *testing.T) {
	t.Run("getglobal-hit", func(t *testing.T) {
		src := `local function f() return gx end; return f`
		st, mainCl := loadFn(t, src)
		st.SetGlobal("gx", value.NumberValue(99))
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal := rets[0]
		pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))

		// 解释器基线(同时填充 GETGLOBAL 的 IC slot 为 NodeHit)。
		base, e := st.Call(value.GCRefOf(fVal), nil, 1)
		if e != nil {
			t.Fatalf("interp: %v", e)
		}
		if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != 99 {
			t.Fatalf("interp f() = %v, want 99", base[0])
		}

		if !promoteProto(st, pid) {
			t.Skip("f proto not supported by gibbous whitelist")
		}
		// inline IC 快路径:同 gen 直达 node 槽。
		got, e2 := st.Call(value.GCRefOf(fVal), nil, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != 99 {
			t.Errorf("gibbous f() = %v, want 99 (inline IC hit)", got[0])
		}

		// 改既有全局值(改值不 bump gen,IC 持续命中)。
		st.SetGlobal("gx", value.NumberValue(7))
		got2, _ := st.Call(value.GCRefOf(fVal), nil, 1)
		if !value.IsNumber(got2[0]) || value.AsNumber(got2[0]) != 7 {
			t.Errorf("gibbous f() after value change = %v, want 7", got2[0])
		}

		// 失效路径:新增大量全局触发 rehash → gen bump → inline 校验失败 → 助手仍正确。
		for i := 0; i < 32; i++ {
			st.SetGlobal("pad"+string(rune('a'+i)), value.NumberValue(float64(i)))
		}
		got3, e3 := st.Call(value.GCRefOf(fVal), nil, 1)
		if e3 != nil {
			t.Fatalf("gibbous after rehash: %v", e3)
		}
		if !value.IsNumber(got3[0]) || value.AsNumber(got3[0]) != 7 {
			t.Errorf("gibbous f() after rehash = %v, want 7 (helper fallback)", got3[0])
		}
	})

	t.Run("setglobal-hit", func(t *testing.T) {
		src := `local function f(v) gy = v; return gy end; return f`
		st, mainCl := loadFn(t, src)
		st.SetGlobal("gy", value.NumberValue(0)) // 预存键(SETGLOBAL 改值快路径要求键存在)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal := rets[0]
		pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))

		base, e := st.Call(value.GCRefOf(fVal), []value.Value{value.NumberValue(11)}, 1)
		if e != nil {
			t.Fatalf("interp: %v", e)
		}
		if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != 11 {
			t.Fatalf("interp f(11) = %v, want 11", base[0])
		}

		if !promoteProto(st, pid) {
			t.Skip("f proto not supported")
		}
		got, e2 := st.Call(value.GCRefOf(fVal), []value.Value{value.NumberValue(22)}, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != 22 {
			t.Errorf("gibbous f(22) = %v, want 22 (inline SETGLOBAL+GETGLOBAL hit)", got[0])
		}
	})
}

// TestPW5b_TableIC PW5-b:GETTABLE/SETTABLE inline IC(键匹配)。
// const-key NodeHit(t.x)/ register-key ArrayHit(t[1])命中 inline 跳哈希;
// 升层前跑解释器基线填 IC + byte-equal 对拍。
func TestPW5b_TableIC(t *testing.T) {
	run := func(t *testing.T, src string, setup func(*State) []value.Value, want float64) {
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal := rets[0]
		pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
		args := setup(st)
		base, e := st.Call(value.GCRefOf(fVal), args, 1)
		if e != nil {
			t.Fatalf("interp: %v", e)
		}
		if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != want {
			t.Fatalf("interp = %v, want %v", base[0], want)
		}
		if !promoteProto(st, pid) {
			t.Skip("proto not supported")
		}
		got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != want {
			t.Errorf("gibbous = %v, want %v (byte-equal)", got[0], want)
		}
	}

	t.Run("gettable-field", func(t *testing.T) {
		run(t, `local function f(t) return t.x end; return f`, func(st *State) []value.Value {
			tv := st.newTableArg(map[string]float64{"x": 42}, nil)
			return []value.Value{tv}
		}, 42)
	})
	t.Run("gettable-array", func(t *testing.T) {
		run(t, `local function f(t) return t[1] end; return f`, func(st *State) []value.Value {
			tv := st.newTableArg(nil, []float64{7, 8, 9})
			return []value.Value{tv}
		}, 7)
	})
	t.Run("settable-field", func(t *testing.T) {
		run(t, `local function f(t) t.x = 5; return t.x end; return f`, func(st *State) []value.Value {
			tv := st.newTableArg(map[string]float64{"x": 0}, nil)
			return []value.Value{tv}
		}, 5)
	})
}

// TestPW5d_NewTableSetList PW5-d:NEWTABLE/SETLIST 经助手(分配+批量写+GC 助手内)。
// 表构造 {10,20,30} 后取元素,升 gibbous byte-equal。
func TestPW5d_NewTableSetList(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want float64
	}{
		// NEWTABLE + LOADK×3(数字)+ SETLIST(B=3,C=1)+ GETTABLE t[2]
		{"array-ctor", `local function f() local t={10,20,30} return t[2] end; return f`, 20},
		// 数组求和(NEWTABLE + SETLIST + for 遍历)
		{"array-sum", `local function f() local t={1,2,3,4} local s=0 for i=1,4 do s=s+t[i] end return s end; return f`, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, mainCl := loadFn(t, tc.src)
			st.SetGCStressMode(true) // 分配密集:freelist 复用暴露漏根/残值
			rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
			if err != nil {
				t.Fatalf("run main: %v", err)
			}
			fVal := rets[0]
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
			base, e := st.Call(value.GCRefOf(fVal), nil, 1)
			if e != nil {
				t.Fatalf("interp: %v", e)
			}
			if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp = %v, want %v", base[0], tc.want)
			}
			if !promoteProto(st, pid) {
				t.Skipf("%s proto not supported", tc.name)
			}
			got, e2 := st.Call(value.GCRefOf(fVal), nil, 1)
			if e2 != nil {
				t.Fatalf("gibbous: %v", e2)
			}
			if !value.IsNumber(got[0]) || value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous = %v, want %v (byte-equal)", got[0], tc.want)
			}
		})
	}
}

// newTableArg 构造一个测试表(string→number 字段 + 数组段),返回其 value。
func (st *State) newTableArg(fields map[string]float64, arr []float64) value.Value {
	asz := uint32(len(arr))
	t := st.allocTable(asz, roundUpPow2(uint32(len(fields))))
	for i, v := range arr {
		st.tableSetInt(t, uint32(i+1), value.NumberValue(v))
	}
	for k, v := range fields {
		st.SetTableField(t, k, value.NumberValue(v))
	}
	return value.MakeGC(value.TagTable, t)
}

// TestPW4_ControlFlowE2E PW4 relooper:含分支/循环的函数升 gibbous 后经
// trampoline 跳 wazero,与解释器逐字节一致。
func TestPW4_ControlFlowE2E(t *testing.T) {
	cases := []struct {
		name string
		src  string
		args []value.Value
		want float64
	}{
		// 数值 for 循环(FORPREP/FORLOOP + 回边 safepoint)
		{"sum-for", `local function f(n) local s=0 for i=1,n do s=s+i end return s end; return f`,
			[]value.Value{value.NumberValue(100)}, 5050},
		// if-then-else(TEST/JMP 比较 + 分支)
		{"abs-pos", `local function f(x) if x<0 then return -x else return x end end; return f`,
			[]value.Value{value.NumberValue(7)}, 7},
		{"abs-neg", `local function f(x) if x<0 then return -x else return x end end; return f`,
			[]value.Value{value.NumberValue(-7)}, 7},
		// 比较 LT 快路径 + 分支
		{"max", `local function f(a,b) if a<b then return b else return a end end; return f`,
			[]value.Value{value.NumberValue(3), value.NumberValue(8)}, 8},
		// 嵌套 for(PW4b 形态,可能 Skip 若 relooper 不支持)
		{"nested-for", `local function f(n) local s=0 for i=1,n do for j=1,n do s=s+1 end end return s end; return f`,
			[]value.Value{value.NumberValue(10)}, 100},
		// while 循环
		{"while", `local function f(n) local s=0 local i=1 while i<=n do s=s+i i=i+1 end return s end; return f`,
			[]value.Value{value.NumberValue(10)}, 55},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, mainCl := loadFn(t, tc.src)
			rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
			if err != nil {
				t.Fatalf("run main: %v", err)
			}
			fVal := rets[0]
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))

			// 解释器基线。
			base, e := st.Call(value.GCRefOf(fVal), tc.args, 1)
			if e != nil {
				t.Fatalf("interp call: %v", e)
			}
			if !value.IsNumber(base[0]) || value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp f = %v, want %v", base[0], tc.want)
			}

			if !promoteProto(st, pid) {
				t.Skipf("%s proto not supported by gibbous relooper (fallback interp)", tc.name)
			}
			// 升层后:经 trampoline 跳 wazero,byte-equal。
			got, e2 := st.Call(value.GCRefOf(fVal), tc.args, 1)
			if e2 != nil {
				t.Fatalf("gibbous call: %v", e2)
			}
			if len(got) != 1 || !value.IsNumber(got[0]) || value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous f = %v, want %v (byte-equal with interp)", got[0], tc.want)
			}
		})
	}
}

// TestPW6a_CallDispatch PW6-a:gibbous 帧内 CALL 三向分派 byte-equal。
// 外层 f 升 gibbous,调用 ① 未升层 crescent helper ② 另一升层 gibbous ③ host。
func TestPW6a_CallDispatch(t *testing.T) {
	t.Run("call-crescent", func(t *testing.T) {
		// f 调未升层 helper(crescent fresh reentry 路径)
		src := `
local function helper(x) return x * 2 end
local function f(a) return helper(a) + 1 end
return f`
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal := rets[0]
		pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
		args := []value.Value{value.NumberValue(20)}
		base, e := st.Call(value.GCRefOf(fVal), args, 1)
		if e != nil {
			t.Fatalf("interp: %v", e)
		}
		if value.AsNumber(base[0]) != 41 {
			t.Fatalf("interp f(20) = %v, want 41", base[0])
		}
		if !promoteProto(st, pid) {
			t.Skip("f not supported")
		}
		got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if value.AsNumber(got[0]) != 41 {
			t.Errorf("gibbous f(20) = %v, want 41 (call crescent helper byte-equal)", got[0])
		}
	})

	t.Run("call-gibbous", func(t *testing.T) {
		// f 与 helper 都升层(gibbous→gibbous 经 h_call 再 enterGibbous)
		src := `
local function helper(x) return x * 2 end
local function f(a) return helper(a) + 1 end
return f, helper`
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 2)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		fVal, hVal := rets[0], rets[1]
		fPid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
		hPid := object.ClosureProtoID(st.arena, value.GCRefOf(hVal))
		args := []value.Value{value.NumberValue(20)}
		base, _ := st.Call(value.GCRefOf(fVal), args, 1)
		if value.AsNumber(base[0]) != 41 {
			t.Fatalf("interp f(20) = %v, want 41", base[0])
		}
		if !promoteProto(st, hPid) || !promoteProto(st, fPid) {
			t.Skip("f/helper not supported")
		}
		got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
		if e2 != nil {
			t.Fatalf("gibbous: %v", e2)
		}
		if value.AsNumber(got[0]) != 41 {
			t.Errorf("gibbous→gibbous f(20) = %v, want 41", got[0])
		}
	})
}

// TestPW6a_CallBaseRefresh PW6-a 核心:被调帧深递归撑爆初始栈(64 槽)触发
// growStack 段重定位后,gibbous 续算用刷新后的 base 读对寄存器。GC stress 下
// 若 base 没刷新会读到已 Free 旧段 → 值错乱/UAF。
func TestPW6a_CallBaseRefresh(t *testing.T) {
	// helper 递归深度 100(每层一个 Lua 帧,撑爆初始 64 槽必 growStack);
	// f 调 helper 后再 + 7,验返回后 f 的寄存器(base 相对)仍读对。
	src := `
local function helper(n) if n <= 0 then return 0 else return helper(n-1) + 1 end end
local function f(a) local r = helper(100) return r + a end
return f`
	st, mainCl := loadFn(t, src)
	st.SetGCStressMode(true)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	fVal := rets[0]
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	args := []value.Value{value.NumberValue(7)}
	base, e := st.Call(value.GCRefOf(fVal), args, 1)
	if e != nil {
		t.Fatalf("interp: %v", e)
	}
	if value.AsNumber(base[0]) != 107 {
		t.Fatalf("interp f(7) = %v, want 107 (helper(100)=100 + 7)", base[0])
	}
	if !promoteProto(st, pid) {
		t.Skip("f not supported")
	}
	got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
	if e2 != nil {
		t.Fatalf("gibbous: %v", e2)
	}
	if value.AsNumber(got[0]) != 107 {
		t.Errorf("gibbous f(7) = %v, want 107 (base 刷新后读对 a=7;若 base 陈旧则错)", got[0])
	}
}

// TestPW6b_TailCall PW6-b:gibbous 帧内 TAILCALL byte-equal + 栈深度恒定。
// 尾递归 f(n,acc):升层后经 h_tailcall 复用帧,proper tail call O(1) 栈
// (executeFrom 在解释器内迭代尾调用链),深度 1e5 不溢出。
//
// 注:深尾递归 baseline 会触发自然升层(回边越阈)→ 编译期 NotCompilable →
// TierStuck 吸收态,使后续 promoteProto 失效。故 oracle 与 gibbous 各用独立
// State:gibbous State 在任何深递归运行前先 promoteProto。
func TestPW6b_TailCall(t *testing.T) {
	src := `
local function f(n, acc)
  if n == 0 then return acc else return f(n-1, acc+n) end
end
return f`
	loadF := func() (*State, value.Value) {
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		return st, rets[0]
	}
	args := []value.Value{value.NumberValue(1000), value.NumberValue(0)}
	// oracle:独立 State 跑解释器。
	stO, fO := loadF()
	base, e := stO.Call(value.GCRefOf(fO), args, 1)
	if e != nil {
		t.Fatalf("interp: %v", e)
	}
	if value.AsNumber(base[0]) != 500500 {
		t.Fatalf("interp f(1000,0) = %v, want 500500", base[0])
	}
	// gibbous:独立 State,深递归前先 promoteProto(避免自然升层 stuck)。
	st, fVal := loadF()
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	if !promoteProto(st, pid) {
		t.Skip("f not supported")
	}
	got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
	if e2 != nil {
		t.Fatalf("gibbous: %v", e2)
	}
	if value.AsNumber(got[0]) != 500500 {
		t.Errorf("gibbous f(1000,0) = %v, want 500500 (tail call byte-equal)", got[0])
	}

	// 深尾递归(1e5):proper tail call 不溢出(若误当普通 CALL 会 stack overflow)。
	deep := []value.Value{value.NumberValue(100000), value.NumberValue(0)}
	gotDeep, e3 := st.Call(value.GCRefOf(fVal), deep, 1)
	if e3 != nil {
		t.Fatalf("gibbous deep tail recursion: %v (proper tail call 应 O(1) 栈)", e3)
	}
	if value.AsNumber(gotDeep[0]) != 5000050000 {
		t.Errorf("gibbous f(1e5,0) = %v, want 5000050000", gotDeep[0])
	}
}

// TestPW6c_ErrorCrossesGibbous PW6-c:错误穿越 gibbous 帧冒泡到 pcall 边界
// byte-equal(错误消息 + 是否被捕获)。
func TestPW6c_ErrorCrossesGibbous(t *testing.T) {
	// f 调 helper,helper 对 nil 做算术报错;错误经 h_call status 链冒泡出 f,
	// 再经 ProtectedCall 边界捕获。gibbous 与解释器错误消息逐字节一致。
	src := `
local function helper(x) return x + 1 end   -- helper(nil) → 对 nil 算术报错
local function f(a) return helper(a) end
return f`
	loadF := func() (*State, value.Value) {
		st, mainCl := loadFn(t, src)
		rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
		if err != nil {
			t.Fatalf("run main: %v", err)
		}
		return st, rets[0]
	}
	badArg := []value.Value{value.Nil}

	// oracle:解释器跑 f(nil),经 ProtectedCall 捕获错误消息。
	stO, fO := loadF()
	_, eO := stO.ProtectedCall(fO, badArg)
	if eO == nil {
		t.Fatal("interp f(nil) 应报错(对 nil 算术)")
	}
	wantMsg := eO.Msg

	// gibbous:f 升层后,错误经 h_call status 链冒泡,ProtectedCall 捕获,消息同款。
	st, fVal := loadF()
	pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
	if !promoteProto(st, pid) {
		t.Skip("f not supported")
	}
	_, eG := st.ProtectedCall(fVal, badArg)
	if eG == nil {
		t.Fatal("gibbous f(nil) 应报错(错误经 status 链穿越 gibbous 帧冒泡)")
	}
	if eG.Msg != wantMsg {
		t.Errorf("gibbous 错误消息 = %q, want %q (byte-equal traceback)", eG.Msg, wantMsg)
	}
}

// TestPW7a_Closure PW7-a:gibbous 帧内 CLOSURE 造闭包 byte-equal。
// 外层 f 升 gibbous,内部 CLOSURE 造 g 捕获局部 x(MOVE 伪指令),调用 g。
func TestPW7a_Closure(t *testing.T) {
	cases := []struct {
		name string
		src  string
		arg  float64
		want float64
	}{
		// CLOSURE 捕获栈局部 x(MOVE 伪指令)
		{"capture-local", `
local function f(a)
  local x = a + 10
  local function g() return x * 2 end
  return g()
end
return f`, 5, 30},
		// 嵌套捕获:g 捕获 x,h 经 g 的 upvalue 捕获 x(GETUPVAL 伪指令)
		{"capture-upval", `
local function f(a)
  local x = a
  local function g()
    local function h() return x + 1 end
    return h()
  end
  return g()
end
return f`, 7, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loadF := func() (*State, value.Value) {
				st, mainCl := loadFn(t, tc.src)
				rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
				if err != nil {
					t.Fatalf("run main: %v", err)
				}
				return st, rets[0]
			}
			args := []value.Value{value.NumberValue(tc.arg)}
			stO, fO := loadF()
			base, e := stO.Call(value.GCRefOf(fO), args, 1)
			if e != nil {
				t.Fatalf("interp: %v", e)
			}
			if value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp = %v, want %v", base[0], tc.want)
			}
			st, fVal := loadF()
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
			if !promoteProto(st, pid) {
				t.Skipf("%s f not supported", tc.name)
			}
			got, e2 := st.Call(value.GCRefOf(fVal), args, 1)
			if e2 != nil {
				t.Fatalf("gibbous: %v", e2)
			}
			if value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous = %v, want %v (CLOSURE byte-equal)", got[0], tc.want)
			}
		})
	}
}

// TestPW4b_TForLoop PW4b:gibbous 帧内 TFORLOOP 泛型 for byte-equal。
// 迭代器经 h_tforloop 跨层调(复用 callLuaFromHost);base 刷新(迭代器调用可能
// growStack)。用自定义迭代器(crescent 测试无 stdlib,ipairs/pairs 是 nil 全局;
// TFORLOOP opcode 不关心迭代器来源,均为 R(A)(R(A+1),R(A+2)),自定义迭代器走完全
// 相同的 TFORLOOP 路径)。
func TestPW4b_TForLoop(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want float64
	}{
		// 自定义范围迭代器(无状态,闭包计数)
		{"range-sum", `
local function range(n)
  local i = 0
  return function() i = i + 1; if i <= n then return i end end
end
local function f()
  local s = 0
  for x in range(5) do s = s + x end
  return s
end
return f`, 15},
		// 经典 stateless iterator(iter, state, control 三元组,模拟 ipairs)
		{"stateful-iter", `
local function iter(t, i)
  i = i + 1
  local v = t[i]
  if v ~= nil then return i, v end
end
local function f()
  local t = {10, 20, 30, 40}
  local s = 0
  for i, v in iter, t, 0 do s = s + v end
  return s
end
return f`, 100},
		// 深迭代(撑爆初始栈,验 base 刷新)
		{"deep-iter", `
local function range(n)
  local i = 0
  return function() i = i + 1; if i <= n then return i end end
end
local function f()
  local s = 0
  for x in range(2000) do s = s + x end
  return s
end
return f`, 2001000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loadF := func() (*State, value.Value) {
				st, mainCl := loadFn(t, tc.src)
				rets, err := st.Call(value.GCRefOf(mainCl), nil, 1)
				if err != nil {
					t.Fatalf("run main: %v", err)
				}
				return st, rets[0]
			}
			stO, fO := loadF()
			base, e := stO.Call(value.GCRefOf(fO), nil, 1)
			if e != nil {
				t.Fatalf("interp: %v", e)
			}
			if value.AsNumber(base[0]) != tc.want {
				t.Fatalf("interp = %v, want %v", base[0], tc.want)
			}
			st, fVal := loadF()
			st.SetGCStressMode(true) // 迭代器调用密集分配,freelist 复用暴露 base/根问题
			pid := object.ClosureProtoID(st.arena, value.GCRefOf(fVal))
			if !promoteProto(st, pid) {
				t.Skipf("%s f not supported", tc.name)
			}
			got, e2 := st.Call(value.GCRefOf(fVal), nil, 1)
			if e2 != nil {
				t.Fatalf("gibbous: %v", e2)
			}
			if value.AsNumber(got[0]) != tc.want {
				t.Errorf("gibbous = %v, want %v (TFORLOOP byte-equal)", got[0], tc.want)
			}
		})
	}
}

// TestPW8_CoroutineNoPromote PW8 线程级 tier 规则:协程线程上的执行不升层
// (07-coroutine-thread-rule §2)。协程内 hot 函数越阈值后保持 TierInterp
// (考虑升层入口的 onMain 守卫拦下);同 Proto 在主线程驱动则正常升层。
func TestPW8_CoroutineNoPromote(t *testing.T) {
	src := `
local function hot(a) return a + 1 end
local function body()
  local s = 0
  for i = 1, 100000 do s = hot(s) end   -- hot 在协程内被反复调用(越入口阈值)
  return s
end
return hot, body`
	st, mainCl := loadFn(t, src)
	rets, err := st.Call(value.GCRefOf(mainCl), nil, 2)
	if err != nil {
		t.Fatalf("run main: %v", err)
	}
	hotVal, bodyVal := rets[0], rets[1]
	hotPid := object.ClosureProtoID(st.arena, value.GCRefOf(hotVal))
	// hot 可编译(单 BB ADD + RETURN);手工标 Compilable 模拟真 P3 F7 放行。
	st.bridge.SetCompilability(st.protos[hotPid], bridge.CompCompilable, 0)

	// 在协程线程上跑 body(body 内调 hot 十万次,远超 HotEntryThreshold)。
	coID, ce := st.NewCoroutine(bodyVal)
	if ce != nil {
		t.Fatalf("NewCoroutine: %v", ce)
	}
	res, ok, re := st.Resume(coID, nil)
	if re != nil || !ok {
		t.Fatalf("Resume: ok=%v err=%v", ok, re)
	}
	if value.AsNumber(res[0]) != 100000 {
		t.Fatalf("coroutine body() = %v, want 100000", res[0])
	}
	// ★ 协程内 hot 极热,但线程级 tier 规则使其不升层(onMain 守卫拦考虑升层入口)。
	if st.bridge.GibbousCodeOf(st.protos[hotPid]) != nil {
		t.Error("协程内 hot 函数不应升层(线程级 tier 规则;onMain 守卫应拦下 considerPromotion)")
	}

	// 对照:同 Proto 在主线程驱动升层成功(证明 hot 本身可编译,上面不升层是
	// 线程门禁而非不可编译)。
	if !promoteProto(st, hotPid) {
		t.Fatal("hot 在主线程应能升层(单 BB ADD+RETURN),onMain=true 门禁放行")
	}
	if st.bridge.GibbousCodeOf(st.protos[hotPid]) == nil {
		t.Fatal("主线程升层后 GibbousCodeOf 应非 nil")
	}
}
