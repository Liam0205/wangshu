// 嵌入式 hardening 边界回归——脚本不得经 stdlib 触发宿主 OOM crash
// (12 §4.9:宿主进程不可崩优先于字节一致)。这些输入在 PUC 5.1.5 /
// gopher-lua 会 OOM crash 进程,wangshu 主动 fail-fast 返 Lua 错误。
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestHardening_StringRepOverflow(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// string.rep("k", 1e14) 在对位 backend 会分配 100T 字节 OOM crash
	prog, _ := wangshu.Compile([]byte(`return pcall(string.rep, "k", 100000000000000)`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Bool() != false {
		t.Errorf("pcall ok=%s, want false (should fail-fast)", r[0].Display())
	}
	if !strings.Contains(r[1].Str(), "string length overflow") {
		t.Errorf("err = %q, want 'string length overflow'", r[1].Str())
	}
}

func TestHardening_StringRepWithinLimit(t *testing.T) {
	// 合理范围正常工作
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`return string.rep("ab", 3)`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Str() != "ababab" {
		t.Errorf("got %q", r[0].Str())
	}
}

func TestHardening_StringFormatWidthOverflow(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// %.99999999999d 会让 fmt.Sprintf 分配巨量字节
	prog, _ := wangshu.Compile([]byte(`return pcall(string.format, "%.99999999999d", 1)`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Bool() != false {
		t.Errorf("pcall ok=%s, want false", r[0].Display())
	}
	if !strings.Contains(r[1].Str(), "precision") && !strings.Contains(r[1].Str(), "width") {
		t.Errorf("err = %q, want width/precision overflow", r[1].Str())
	}
}

func TestHardening_StringFormatNormalWidth(t *testing.T) {
	// 正常 width/precision 工作
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`return string.format("%5.2f", 3.14159)`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Str() != " 3.14" {
		t.Errorf("got %q", r[0].Str())
	}
}

func TestHardening_TableConcatRangeOverflow(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// j = 1e14 让 concat 循环耗尽内存
	prog, _ := wangshu.Compile([]byte(`return pcall(table.concat, {1,2,3}, ",", 1, 100000000000000)`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Bool() != false {
		t.Errorf("pcall ok=%s, want false", r[0].Display())
	}
	if !strings.Contains(r[1].Str(), "range too large") {
		t.Errorf("err = %q, want 'range too large'", r[1].Str())
	}
}

func TestHardening_TableConcatNaNRange(t *testing.T) {
	// NaN range:NaN-X=NaN 绕过范围检查 + Go int(NaN)=MIN_INT64 与 PUC
	// int(NaN)=0 不一致。规范化 NaN→0 后走正常 "invalid value at index" 路径
	// (对齐 PUC 行为),不崩溃不绕过。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`return pcall(table.concat, {1,2,3}, ",", 0/0)`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// NaN→0,t[0] 是 nil → invalid value(对齐 PUC);不应是 OOM 或 range too large
	if r[0].Bool() != false {
		t.Errorf("pcall ok=%s, want false", r[0].Display())
	}
	if !strings.Contains(r[1].Str(), "invalid value") {
		t.Errorf("err = %q, want 'invalid value' (NaN→0 path)", r[1].Str())
	}
}

func TestHardening_TableConcatNormal(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`return table.concat({"a","b","c"}, "-")`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Str() != "a-b-c" {
		t.Errorf("got %q", r[0].Str())
	}
}

func TestHardening_TableLargeIntKey(t *testing.T) {
	// fuzz corpus testdata/fuzz/FuzzCompileRun/5095a0fd13d76273:
	// `t={} t[3333170000]=""` 触发 rehash → countIntKey 内 `for (1<<b) < u`
	// 死循环(uint32(1)<<32=0,b 永远 < u)。表面像 OOM 实为 CPU 死循环。
	// 修复:循环加 b<31 守卫 + bestASize 封顶 1<<24(与主线 hardening 阈值口径一致)。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`local t={} t[3333170000]="" return "ok"`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Str() != "ok" {
		t.Errorf("got %q, want 'ok' (大整数 key 应正常落 hash 段)", r[0].Str())
	}
}

func TestHardening_TableUint32MaxKey(t *testing.T) {
	// uint32 边界值 4294967295 = 2^32-1,确认 b=31 守卫不漏 + 不死循环。
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`local t={} t[4294967295]="x" return t[4294967295]`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if r[0].Str() != "x" {
		t.Errorf("got %q, want 'x'", r[0].Str())
	}
}
