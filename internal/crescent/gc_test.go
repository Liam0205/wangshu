// GC pressure test — 验证主循环在分配频繁时不会因 GC 误回收活跃对象产生
// byte-different 结果(M10 验收)。
package crescent

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/value"
)

// TestGC_StressClosure 在 closure 频繁分配下检查闭包仍能正确读取捕获的局部。
func TestGC_StressClosure(t *testing.T) {
	src := `
local function makeAdder(x)
  return function(y) return x + y end
end
local a = makeAdder(10)
local sum = 0
for i = 1, 200 do
  -- 每次创建新闭包,触发若干次 NEWTABLE 那种分配压力的代用品
  local f = makeAdder(i)
  sum = sum + f(1)
end
result = sum + a(5)
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	// sum_{i=1..200}(i+1) + (10+5) = (sum 1..200 + 200) + 15 = 20100 + 200 + 15 = 20315
	if !value.IsNumber(v) || value.AsNumber(v) != 20315 {
		t.Errorf("result = %v, want 20315", debugVal(st, v))
	}
}

// TestGC_StressTable 在表频繁创建下检查表数据正确。
func TestGC_StressTable(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("local total = 0\n")
	for i := 0; i < 50; i++ {
		sb.WriteString("local t")
		sb.WriteString("a = { 1, 2, 3 }\n")
		sb.WriteString("total = total + ta[1] + ta[2] + ta[3]\n")
	}
	sb.WriteString("result = total\n")
	st := runLua(t, sb.String())
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	want := float64(50 * (1 + 2 + 3))
	if !value.IsNumber(v) || value.AsNumber(v) != want {
		t.Errorf("result = %v, want %v", debugVal(st, v), want)
	}
}

// TestGC_StressConcat 在 CONCAT 反复 intern 字符串下验证不误回收。
func TestGC_StressConcat(t *testing.T) {
	src := `
local s = "x"
for i = 1, 100 do
  s = s .. "y"
end
result = s
`
	st := runLua(t, src)
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if value.Tag(v) != value.TagString {
		t.Fatalf("result is not string: %v", debugVal(st, v))
	}
	got, _ := st.toStringBytes(v)
	want := "x" + strings.Repeat("y", 100)
	if string(got) != want {
		t.Errorf("result len=%d, want len=%d", len(got), len(want))
	}
}

// TestGC_DirectCollect 显式触发一次 Collect,验证活跃栈不被错误回收。
func TestGC_DirectCollect(t *testing.T) {
	src := `
local t = { 100, 200, 300 }
result = t[1] + t[2] + t[3]
`
	st := runLua(t, src)
	// 触发一次 Collect
	st.gc.Collect()
	v, _ := st.tableGet(st.globals, st.makeStringValue("result"))
	if !value.IsNumber(v) || value.AsNumber(v) != 600 {
		t.Errorf("result = %v, want 600", debugVal(st, v))
	}
}
