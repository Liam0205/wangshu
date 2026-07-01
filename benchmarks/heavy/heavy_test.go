// Package heavy benchmarks 升层档(P3 wasm / P4 jit)友好型纯算术热点脚本:
// heavy_arith(单函数 + 长 FORLOOP + 纯浮点)/ heavy_recursion(尾递归 collatz)/
// heavy_floatloop(平面浮点核 + 嵌套 FORLOOP)。
//
// 与 benchmarks/realworld 互补:realworld 5 脚本是 benchmark-game 经典负载,
// 以表/字符串/CALL 为主,经 P3/P4 helper 编排实际工作在 Go 侧执行,升层档收益
// 不明显;heavy 这 3 脚本故意去掉表/字符串/CALL 数学库依赖,只压「单函数大热点
// + 长 BB + 算术」— 让升层档真正能发挥的形态浮现。
//
// **P4 形态子集覆盖率**(承 docs/design/p4-method-jit/implementation-progress.md
// PJ7 范围):当前 P4 PJ7 真接入子集是「单 BB 值产生 + RETURN / FORLOOP 字节级
// inline / 表 IC 直达 / CALL void / SELF method」,heavy 三脚本的内层 kernel 形态
// (FORLOOP body 含多算术 + 累加 / while 单条件 + 浮点 / 自递归 + 算术)均**不在**
// 现 PJ7 形态白名单内。**预期 P4 升 0**,数字 ≈ P1 解释器 — 这是诚实的覆盖率
// 暴露,标 P4 PJ7+ 扩 SAO 白名单的 followup;不修脚本去迁就 P4 子集。
//
// P3 wasm 子集广(MOVE/算术/比较/FORLOOP/CALL 已全开,详 internal/gibbous/wasm/
// compiler.go NewCompiler),三脚本均能升 1 个内层 kernel Proto,数字反映 wazero
// 字节码翻译收益。
//
// 本文件无 build tag — `TestHeavy_OracleParity` 在 p1/p3/p4 三 build 下都需要
// (验证 wangshu 自身行为对位官方 5.1.5,与 build variant 无关)。
// Benchmark 部分(`_Wangshu` / `_Gopher`)拆到 `heavy_bench_test.go`,带
// `!wangshu_p3 && !wangshu_p4` 避免 wangshu_profile 采样钩污染新月数字。
// `_Gibbous` 在 `heavy_gibbous_test.go` 里(wangshu_p3 + wangshu_profile),
// `_GibbousJIT` 在 `heavy_gibbous_jit_test.go` 里(wangshu_p4 + wangshu_profile)。
package heavy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

var scripts = []string{"heavy_arith", "heavy_recursion", "heavy_floatloop"}

func loadScript(tb testing.TB, name string) []byte {
	tb.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", name+".lua"))
	if err != nil {
		tb.Fatalf("read %s: %v", name, err)
	}
	return src
}

func runWangshu(tb testing.TB, src []byte, name string) string {
	tb.Helper()
	prog, err := wangshu.Compile(src, name)
	if err != nil {
		tb.Fatalf("wangshu compile %s: %v", name, err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		tb.Fatalf("wangshu run %s: %v", name, err)
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = r.Display()
	}
	return strings.Join(parts, "\t")
}

func runLua51(tb testing.TB, src []byte, name string) (string, bool) {
	tb.Helper()
	bin, err := exec.LookPath("lua5.1")
	if err != nil {
		return "", false
	}
	tmp := filepath.Join(tb.TempDir(), name+".lua")
	if err := os.WriteFile(tmp, src, 0o644); err != nil {
		tb.Fatalf("write tmp: %v", err)
	}
	cmd := exec.Command(bin, tmp)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		tb.Fatalf("lua5.1 %s: %v\n%s", name, err, out.String())
	}
	return strings.TrimRight(out.String(), "\n"), true
}

// TestHeavy_OracleParity:wangshu vs lua5.1 官方解释器逐字节(数字会有最后一位
// 浮点差异,本测试用 print 语义对比,不强求 byte-equal;只看主路径不崩 + 结果
// 数量级一致)。
//
// 脚本内 `return` 的是数字,需配合 lua5.1 解释器侧手动 print 包装;这里改为
// 各脚本自带 `print(...)` 包装,oracle 和 wangshu 走同形态。
//
// 实测目的:防御性确认这 3 个 heavy 脚本在 wangshu 三档(p1/p3/p4 build)与
// lua5.1 oracle 都能跑通,不崩。
func TestHeavy_OracleParity(t *testing.T) {
	for _, name := range scripts {
		t.Run(name, func(t *testing.T) {
			src := loadScript(t, name)
			got := runWangshu(t, src, name)
			// Wrap with print(...) on lua5.1 side: `return X` → `print((function() return X end)())`.
			oracleSrc := []byte("print((function()\n")
			oracleSrc = append(oracleSrc, src...)
			oracleSrc = append(oracleSrc, []byte("\nend)())\n")...)
			want, ok := runLua51(t, oracleSrc, name)
			if !ok {
				t.Skip("lua5.1 not in PATH; skipping oracle parity check")
				return
			}
			if got != want {
				t.Errorf("oracle mismatch for %s:\nwangshu: %q\nlua5.1 : %q", name, got, want)
			}
		})
	}
}
