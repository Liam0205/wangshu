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
		b.OnEnter(proto)
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
