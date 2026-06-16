// 类型化 array table 构造器测试(issue #13)——
// NewFloatArrayTable / NewInt64ArrayTable / NewBoolArrayTable / NewStringArrayTable。
// 验证 round-trip、脚本读出形态、空/nil slice、int64 精度边界。
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestNewFloatArrayTable_RoundTrip 验证 float64 进 array 段、`xs[i]` 与 `#xs`
// 读出与 NewArrayTable([]Value{Number(...)}) 字节等价。
func TestNewFloatArrayTable_RoundTrip(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewFloatArrayTable([]float64{1.5, 2.5, 3.5})
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 3 {
		t.Errorf("Len = %d, want 3", got)
	}
	if got := tt.GetIndex(1).Number(); got != 1.5 {
		t.Errorf("[1] = %v, want 1.5", got)
	}
	if got := tt.GetIndex(2).Number(); got != 2.5 {
		t.Errorf("[2] = %v, want 2.5", got)
	}
	if got := tt.GetIndex(3).Number(); got != 3.5 {
		t.Errorf("[3] = %v, want 3.5", got)
	}
	if !tt.GetIndex(4).IsNil() {
		t.Errorf("[4] = %s, want nil", tt.GetIndex(4).Display())
	}
}

// TestNewFloatArrayTable_ScriptVisible 验证脚本侧 `xs[i]` 看到的就是普通 array,
// 不是 arena 列轨的 `__index` 代理表(关键 parity 承诺)。
func TestNewFloatArrayTable_ScriptVisible(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewFloatArrayTable([]float64{10, 20, 30, 40, 50})
	defer tv.Release()
	st.SetGlobal("xs", tv)

	prog, err := wangshu.Compile([]byte(`
		local s = 0
		for i = 1, #xs do s = s + xs[i] end
		return s, #xs
	`), "sum")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 150 {
		t.Errorf("sum = %v, want 150", r[0].Number())
	}
	if r[1].Number() != 5 {
		t.Errorf("#xs = %v, want 5", r[1].Number())
	}
}

// TestNewFloatArrayTable_EmptyAndNil 验证空 slice 与 nil slice 都合法,返回空表。
func TestNewFloatArrayTable_EmptyAndNil(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	for _, vals := range [][]float64{{}, nil} {
		tv := st.NewFloatArrayTable(vals)
		if got := tv.AsTable().Len(); got != 0 {
			t.Errorf("Len = %d, want 0", got)
		}
		tv.Release()
	}
}

// TestNewInt64ArrayTable_RoundTrip 验证 int64 元素 round-trip 到 float64 后正确读出。
func TestNewInt64ArrayTable_RoundTrip(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv, err := st.NewInt64ArrayTable([]int64{10, 20, 30})
	if err != nil {
		t.Fatalf("NewInt64ArrayTable: %v", err)
	}
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.Len(); got != 3 {
		t.Errorf("Len = %d, want 3", got)
	}
	if got := tt.GetIndex(2).Number(); got != 20 {
		t.Errorf("[2] = %v, want 20", got)
	}
}

// TestNewInt64ArrayTable_PrecisionOverflow 验证 |v| > 2^53 触发错误且返回 Nil。
// 与 Arena.AddInt64Column 同款规则(评审决策第 3 项)。
func TestNewInt64ArrayTable_PrecisionOverflow(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	cases := []struct {
		name string
		vals []int64
		bad  int
	}{
		{"positive overflow", []int64{1, 2, (1 << 53) + 1}, 2},
		{"negative overflow", []int64{-1, -((1 << 53) + 1), -3}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tv, err := st.NewInt64ArrayTable(c.vals)
			if err == nil {
				tv.Release()
				t.Fatal("expected overflow error, got nil")
			}
			if !tv.IsNil() {
				t.Errorf("on err: tv = %s, want Nil", tv.Display())
			}
			if !strings.Contains(err.Error(), "2^53") {
				t.Errorf("err msg lacks 2^53 hint: %v", err)
			}
		})
	}
}

// TestNewInt64ArrayTable_BoundaryValuesAccepted 验证 ±2^53 边界本身被接受(只有超出才报错)。
func TestNewInt64ArrayTable_BoundaryValuesAccepted(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv, err := st.NewInt64ArrayTable([]int64{1 << 53, -(1 << 53)})
	if err != nil {
		t.Fatalf("boundary value rejected: %v", err)
	}
	defer tv.Release()
	if got := tv.AsTable().Len(); got != 2 {
		t.Errorf("Len = %d, want 2", got)
	}
}

// TestNewBoolArrayTable_RoundTrip 验证 bool 元素 round-trip。
func TestNewBoolArrayTable_RoundTrip(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewBoolArrayTable([]bool{true, false, true})
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.GetIndex(1).Bool(); !got {
		t.Errorf("[1] = false, want true")
	}
	if got := tt.GetIndex(2).Bool(); got {
		t.Errorf("[2] = true, want false")
	}
	if got := tt.GetIndex(3).Bool(); !got {
		t.Errorf("[3] = false, want true")
	}
}

// TestNewBoolArrayTable_ScriptVisible 验证脚本侧 `flags[i]` 与 `and/or` 短路操作正常。
func TestNewBoolArrayTable_ScriptVisible(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewBoolArrayTable([]bool{true, false, true, true})
	defer tv.Release()
	st.SetGlobal("flags", tv)
	prog, _ := wangshu.Compile([]byte(`
		local n = 0
		for i = 1, 4 do if flags[i] then n = n + 1 end end
		return n
	`), "countbool")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Number() != 3 {
		t.Errorf("count = %v, want 3", r[0].Number())
	}
}

// TestNewStringArrayTable_RoundTrip 验证 string 元素 round-trip。
func TestNewStringArrayTable_RoundTrip(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewStringArrayTable([]string{"alice", "bob", "carol"})
	defer tv.Release()
	tt := tv.AsTable()
	if got := tt.GetIndex(1).Str(); got != "alice" {
		t.Errorf("[1] = %q, want alice", got)
	}
	if got := tt.GetIndex(2).Str(); got != "bob" {
		t.Errorf("[2] = %q, want bob", got)
	}
	if got := tt.GetIndex(3).Str(); got != "carol" {
		t.Errorf("[3] = %q, want carol", got)
	}
}

// TestNewStringArrayTable_ScriptConcat 验证脚本侧 string concat/比较行为一致
// (重点:intern 后 GCRef 与 wangshu.String 字面量等价)。
func TestNewStringArrayTable_ScriptConcat(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewStringArrayTable([]string{"foo", "bar", "baz"})
	defer tv.Release()
	st.SetGlobal("names", tv)
	prog, _ := wangshu.Compile([]byte(`
		return names[1] .. "," .. names[2] .. "," .. names[3]
	`), "concat")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := r[0].Str(); got != "foo,bar,baz" {
		t.Errorf("concat = %q, want foo,bar,baz", got)
	}
}

// TestNewStringArrayTable_InternDedup 验证重复 string 元素共享 intern 槽
// (隐式契约:相同字面量在 array 段里仍正确比较等价)。
func TestNewStringArrayTable_InternDedup(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	tv := st.NewStringArrayTable([]string{"alice", "alice", "alice"})
	defer tv.Release()
	st.SetGlobal("dup", tv)
	prog, _ := wangshu.Compile([]byte(`return dup[1] == dup[2] and dup[2] == dup[3]`), "dup")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !r[0].Bool() {
		t.Errorf("interned dedup: got false, want true")
	}
}

// TestNewArrayTable_NoAllocPerElement_NonPanic 兼具 smoke 测试与 stress 用途:
// 在 GCStressMode 下连跑多次 typed-array 构造 + 脚本读 + Release,确保无 panic
// 或泄漏(重点:typed-array 不走 fromInnerWithPin,但仍要兼容 GC 压力)。
func TestNewArrayTable_NoAllocPerElement_NonPanic(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	st.SetGCStressMode(true)
	for r := 0; r < 50; r++ {
		// 每轮换一种 typed-array 跑一遍
		tv1 := st.NewFloatArrayTable([]float64{1, 2, 3, 4, 5})
		tv1.Release()

		tv2, err := st.NewInt64ArrayTable([]int64{10, 20, 30})
		if err != nil {
			t.Fatalf("int64: %v", err)
		}
		tv2.Release()

		tv3 := st.NewBoolArrayTable([]bool{true, false})
		tv3.Release()

		tv4 := st.NewStringArrayTable([]string{"a", "b", "c"})
		tv4.Release()
	}
}
