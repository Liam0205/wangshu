// 公共面 Table API 测试——issue #2:pineapple common-mode mixed-type list
// 投喂、GetGlobal/SetGlobal/State.Call 复合值 round-trip、嵌套 table 与
// function、Len 语义、跨 State 与 Release 后误用。
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestNewTable_Empty(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	if !tv.IsTable() {
		t.Fatalf("NewTable not table: %s", tv.Display())
	}
	tbl := tv.AsTable()
	if tbl.Len() != 0 {
		t.Errorf("empty Len = %d, want 0", tbl.Len())
	}
	if v := tbl.GetIndex(1); !v.IsNil() {
		t.Errorf("missing key = %s, want nil", v.Display())
	}
}

func TestTable_ScalarsRoundTrip(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tbl := tv.AsTable()
	mustSet(t, tbl.Set(wangshu.String("name"), wangshu.String("alice")))
	mustSet(t, tbl.Set(wangshu.String("age"), wangshu.Number(30)))
	mustSet(t, tbl.Set(wangshu.String("admin"), wangshu.Bool(true)))
	mustSet(t, tbl.Set(wangshu.Number(1), wangshu.Number(100)))
	mustSet(t, tbl.SetIndex(2, wangshu.Number(200)))
	mustSet(t, tbl.SetIndex(3, wangshu.Number(300)))

	if v := tbl.Get(wangshu.String("name")); !v.IsString() || v.Str() != "alice" {
		t.Errorf("name = %s", v.Display())
	}
	if v := tbl.Get(wangshu.String("age")); !v.IsNumber() || v.Number() != 30 {
		t.Errorf("age = %s", v.Display())
	}
	if v := tbl.Get(wangshu.String("admin")); !v.IsBool() || !v.Bool() {
		t.Errorf("admin = %s", v.Display())
	}
	if v := tbl.GetIndex(2); !v.IsNumber() || v.Number() != 200 {
		t.Errorf("[2] = %s", v.Display())
	}
	if tbl.Len() != 3 {
		t.Errorf("Len = %d, want 3", tbl.Len())
	}
}

func TestTable_MixedTypeListPineapple(t *testing.T) {
	// pineapple common-mode 形态:mixed-type []any list,允许 nil 占位、
	// number 和 string 混在同一 list。issue #2 验收用例。
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tbl := tv.AsTable()
	mustSet(t, tbl.SetIndex(1, wangshu.Number(42)))
	mustSet(t, tbl.SetIndex(2, wangshu.String("hello")))
	mustSet(t, tbl.SetIndex(3, wangshu.Nil()))
	mustSet(t, tbl.SetIndex(4, wangshu.Bool(true)))

	// 装进 globals,脚本可以遍历
	st.SetGlobal("items", tv)
	prog, _ := wangshu.Compile([]byte(`
local s = ""
for i = 1, 4 do
  s = s .. tostring(items[i]) .. ","
end
return s
`), "mix")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Str() != "42,hello,nil,true," {
		t.Errorf("script saw %q", r[0].Str())
	}
}

func TestSetGlobal_TableScriptVisible(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tbl := tv.AsTable()
	mustSet(t, tbl.Set(wangshu.String("k"), wangshu.Number(7)))
	st.SetGlobal("cfg", tv)

	prog, _ := wangshu.Compile([]byte(`return cfg.k * 2`), "cfg")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 14 {
		t.Errorf("got %s", r[0].Display())
	}
}

func TestGetGlobal_TableFromScript(t *testing.T) {
	// 脚本里建表写 globals;Go 端 GetGlobal 取出并读字段。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`
result = { name = "wangshu", ver = 11, list = {10, 20, 30} }
`), "build")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	rv := st.GetGlobal("result")
	defer rv.Release()
	if !rv.IsTable() {
		t.Fatalf("result not table: %s", rv.Display())
	}
	r := rv.AsTable()
	if v := r.Get(wangshu.String("name")); v.Str() != "wangshu" {
		t.Errorf("name = %s", v.Display())
	}
	if v := r.Get(wangshu.String("ver")); v.Number() != 11 {
		t.Errorf("ver = %s", v.Display())
	}
	listv := r.Get(wangshu.String("list"))
	defer listv.Release()
	if !listv.IsTable() {
		t.Fatalf("list not table: %s", listv.Display())
	}
	list := listv.AsTable()
	if list.Len() != 3 || list.GetIndex(2).Number() != 20 {
		t.Errorf("list len=%d [2]=%s", list.Len(), list.GetIndex(2).Display())
	}
}

func TestTable_NestedTableSet(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	outer := st.NewTable()
	defer outer.Release()
	inner := st.NewTable()
	defer inner.Release()
	mustSet(t, inner.AsTable().Set(wangshu.String("x"), wangshu.Number(99)))
	mustSet(t, outer.AsTable().Set(wangshu.String("sub"), inner))
	st.SetGlobal("c", outer)
	prog, _ := wangshu.Compile([]byte(`return c.sub.x + 1`), "nest")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 100 {
		t.Errorf("got %s", r[0].Display())
	}
}

func TestTable_FunctionValue(t *testing.T) {
	// 脚本里定义函数 → GetGlobal 取出 → 装进 Table → SetGlobal → 脚本调用。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`function double(x) return x * 2 end`), "def")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("double")
	defer fn.Release()

	tv := st.NewTable()
	defer tv.Release()
	mustSet(t, tv.AsTable().Set(wangshu.String("f"), fn))
	st.SetGlobal("util", tv)

	prog2, _ := wangshu.Compile([]byte(`return util.f(21)`), "use")
	r, err := prog2.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 42 {
		t.Errorf("got %s", r[0].Display())
	}
}

func TestTable_LenBorderSemantics(t *testing.T) {
	// # 语义 = rawBorder:对带洞数组返回任意一个 border(t[n]~=nil && t[n+1]==nil)。
	// 这里用无洞数组验证基本一致性,避免 border 二分非确定性引入 flakey。
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tbl := tv.AsTable()
	for i := 1; i <= 5; i++ {
		mustSet(t, tbl.SetIndex(i, wangshu.Number(float64(i*i))))
	}
	if tbl.Len() != 5 {
		t.Errorf("Len = %d, want 5", tbl.Len())
	}
}

func TestTable_SetNilDelete(t *testing.T) {
	// 写 nil 等于删除键(Lua 表语义)。
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tbl := tv.AsTable()
	mustSet(t, tbl.Set(wangshu.String("k"), wangshu.Number(1)))
	if v := tbl.Get(wangshu.String("k")); v.Number() != 1 {
		t.Fatalf("pre-del = %s", v.Display())
	}
	mustSet(t, tbl.Set(wangshu.String("k"), wangshu.Nil()))
	if v := tbl.Get(wangshu.String("k")); !v.IsNil() {
		t.Errorf("post-del = %s, want nil", v.Display())
	}
}

func TestTable_CrossStateSet(t *testing.T) {
	// 把 state1 的 table 写入 state2 的 globals,toInner 应映射为 Nil 兜底
	// (跨 State 引用是 GCRef 跨 arena,绝不能透传)。
	st1 := wangshu.NewState(wangshu.Options{})
	st2 := wangshu.NewState(wangshu.Options{})
	tv := st1.NewTable()
	defer tv.Release()
	st2.SetGlobal("foreign", tv)
	v := st2.GetGlobal("foreign")
	defer v.Release()
	if !v.IsNil() {
		t.Errorf("cross-State table seeped through: %s", v.Display())
	}
}

func TestTable_AfterRelease(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	tbl := tv.AsTable()
	tv.Release()
	// AsTable 句柄持有的是 pinIdx;Release 后 PinnedRefAt 返回 Null → Get/Set 安全失效
	if err := tbl.Set(wangshu.String("x"), wangshu.Number(1)); err == nil ||
		!strings.Contains(err.Error(), "released") {
		t.Errorf("after release err = %v", err)
	}
	if v := tbl.Get(wangshu.String("x")); !v.IsNil() {
		t.Errorf("after release Get = %s", v.Display())
	}
	if tbl.Len() != 0 {
		t.Errorf("after release Len = %d", tbl.Len())
	}
	tv.Release() // 重复 Release 无副作用
}

func TestTable_PinSurvivesGlobalOverwrite(t *testing.T) {
	// 同 GetGlobal_PinSurvivesGlobalOverwrite 的 table 对偶:取出 → globals
	// 覆盖 → GC 压力下表仍可用(pin 表把 GCRef 当根)。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`t = { x = 42 }`), "x")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	tv := st.GetGlobal("t")
	defer tv.Release()
	st.SetGlobal("t", wangshu.Nil())
	st.SetGCStressMode(true)
	defer st.SetGCStressMode(false)
	if v := tv.AsTable().Get(wangshu.String("x")); v.Number() != 42 {
		t.Errorf("after overwrite+GC: %s", v.Display())
	}
}

func TestTable_ReturnFromState_Call(t *testing.T) {
	// 脚本函数返回 table → Go 端 state.Call 取得 → 读字段 round-trip。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`
function make(n)
  local t = {}
  for i = 1, n do t[i] = i * 10 end
  return t
end
`), "def")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("make")
	defer fn.Release()
	r, err := st.Call(fn, wangshu.Number(4))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	defer r[0].Release()
	if !r[0].IsTable() {
		t.Fatalf("returned not table: %s", r[0].Display())
	}
	tbl := r[0].AsTable()
	if tbl.Len() != 4 || tbl.GetIndex(3).Number() != 30 {
		t.Errorf("len=%d [3]=%s", tbl.Len(), tbl.GetIndex(3).Display())
	}
}

func TestTable_ForEach_MixedKeys(t *testing.T) {
	// pineapple 「return map」场景:string-key map + 整数 key 混在一表,
	// adapter 桥到 map[string]any。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`
function f() return { name = "alice", age = 30, [1] = "first" } end
`), "def")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	defer fn.Release()
	r, err := st.Call(fn)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	defer r[0].Release()

	got := map[string]any{}
	err = r[0].AsTable().ForEach(func(k, v wangshu.Value) bool {
		switch {
		case k.IsString():
			if v.IsString() {
				got[k.Str()] = v.Str()
			} else if v.IsNumber() {
				got[k.Str()] = v.Number()
			}
		case k.IsNumber():
			if v.IsString() {
				got[wangshu.Number(k.Number()).Display()] = v.Str()
			}
		}
		return true
	})
	if err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if got["name"] != "alice" || got["age"] != float64(30) || got["1"] != "first" {
		t.Errorf("got = %v", got)
	}
}

func TestTable_ForEach_EmptyTable(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	count := 0
	err := tv.AsTable().ForEach(func(_, _ wangshu.Value) bool {
		count++
		return true
	})
	if err != nil {
		t.Fatalf("ForEach empty: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestTable_ForEach_EarlyTerminate(t *testing.T) {
	// fn 返 false 提前终止;只收集前 2 项。
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tbl := tv.AsTable()
	for i := 1; i <= 5; i++ {
		mustSet(t, tbl.SetIndex(i, wangshu.Number(float64(i*10))))
	}
	seen := 0
	err := tbl.ForEach(func(_, _ wangshu.Value) bool {
		seen++
		return seen < 2 // 收到第 2 项后下次返 false
	})
	if err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if seen != 2 {
		t.Errorf("seen = %d, want 2", seen)
	}
}

func TestTable_ForEach_NestedTable(t *testing.T) {
	// fn 内拿到 nested table val,继续 ForEach 子表;复合值 Release。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`
function f() return { inner = { 100, 200, 300 } } end
`), "def")
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	fn := st.GetGlobal("f")
	defer fn.Release()
	r, _ := st.Call(fn)
	defer r[0].Release()

	var sum float64
	err := r[0].AsTable().ForEach(func(k, v wangshu.Value) bool {
		if !v.IsTable() {
			return true
		}
		defer v.Release()
		_ = v.AsTable().ForEach(func(_, inner wangshu.Value) bool {
			if inner.IsNumber() {
				sum += inner.Number()
			}
			return true
		})
		return true
	})
	if err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if sum != 600 {
		t.Errorf("sum = %v, want 600", sum)
	}
}

func TestTable_ForEach_AfterRelease(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	tbl := tv.AsTable()
	mustSet(t, tbl.Set(wangshu.String("k"), wangshu.Number(1)))
	tv.Release()
	err := tbl.ForEach(func(_, _ wangshu.Value) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "released") {
		t.Errorf("after release err = %v", err)
	}
}

func TestTable_ForEach_DeterministicOrder(t *testing.T) {
	// rawNext 序确定性:同形状下迭代序稳定(12 pairs 序口径)
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewTable()
	defer tv.Release()
	tbl := tv.AsTable()
	for i := 1; i <= 3; i++ {
		mustSet(t, tbl.SetIndex(i, wangshu.Number(float64(i))))
	}
	collect := func() []float64 {
		out := []float64{}
		_ = tbl.ForEach(func(_, v wangshu.Value) bool {
			if v.IsNumber() {
				out = append(out, v.Number())
			}
			return true
		})
		return out
	}
	a := collect()
	b := collect()
	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("序不稳:run1[%d]=%v run2[%d]=%v", i, a[i], i, b[i])
		}
	}
}

func mustSet(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
}
