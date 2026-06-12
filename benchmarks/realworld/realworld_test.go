// Package realworld benchmarks benchmark-game 经典脚本(fib / binary-trees /
// spectral-norm / fannkuch / n-body)on Wangshu vs gopher-lua,并以官方
// lua5.1 输出为正确性 oracle(TestRealWorld_OracleParity)。
//
// 与 baseline 三档微基准互补:这些是调用/分配/浮点/表操作的真实负载混合,
// 回应「微基准 ≠ 真实负载」的性能故事缺口。运行:`go test -bench . ./benchmarks/realworld`。
package realworld

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lua "github.com/yuin/gopher-lua"

	"github.com/Liam0205/wangshu"
)

var scripts = []string{"fib", "binarytrees", "spectralnorm", "fannkuch", "nbody"}

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

// TestRealWorld_OracleParity 与官方 lua5.1 对拍每个脚本的返回值。
func TestRealWorld_OracleParity(t *testing.T) {
	oracle, err := exec.LookPath("lua5.1")
	if err != nil {
		t.Skip("lua5.1 oracle not found; skipping parity check")
	}
	for _, name := range scripts {
		t.Run(name, func(t *testing.T) {
			src := loadScript(t, name)
			// oracle 侧:把 return 值 print 出来(\t join 与 wangshu 侧一致)
			wrapped := "local function __chunk() " + string(src) + "\nend\n" +
				"print(__chunk())"
			cmd := exec.Command(oracle, "-")
			cmd.Stdin = bytes.NewReader([]byte(wrapped))
			var out bytes.Buffer
			cmd.Stdout = &out
			if err := cmd.Run(); err != nil {
				t.Fatalf("oracle: %v", err)
			}
			want := strings.TrimRight(out.String(), "\n")
			got := runWangshu(t, src, name)
			if got != want {
				t.Errorf("parity diff:\n  wangshu: %q\n  oracle:  %q", got, want)
			}
		})
	}
}

func benchVM(b *testing.B, name string, wangshuSide bool) {
	src := loadScript(b, name)
	if wangshuSide {
		prog, err := wangshu.Compile(src, name)
		if err != nil {
			b.Fatalf("compile: %v", err)
		}
		st := wangshu.NewState(wangshu.Options{})
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := prog.Run(st); err != nil {
				b.Fatalf("run: %v", err)
			}
		}
		return
	}
	L := lua.NewState()
	defer L.Close()
	fn, err := L.LoadString(string(src))
	if err != nil {
		b.Fatalf("gopher compile: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		L.Push(fn)
		if err := L.PCall(0, lua.MultRet, nil); err != nil {
			b.Fatalf("gopher run: %v", err)
		}
		L.SetTop(0)
	}
}

func BenchmarkFib_Wangshu(b *testing.B)          { benchVM(b, "fib", true) }
func BenchmarkFib_Gopher(b *testing.B)           { benchVM(b, "fib", false) }
func BenchmarkBinaryTrees_Wangshu(b *testing.B)  { benchVM(b, "binarytrees", true) }
func BenchmarkBinaryTrees_Gopher(b *testing.B)   { benchVM(b, "binarytrees", false) }
func BenchmarkSpectralNorm_Wangshu(b *testing.B) { benchVM(b, "spectralnorm", true) }
func BenchmarkSpectralNorm_Gopher(b *testing.B)  { benchVM(b, "spectralnorm", false) }
func BenchmarkFannkuch_Wangshu(b *testing.B)     { benchVM(b, "fannkuch", true) }
func BenchmarkFannkuch_Gopher(b *testing.B)      { benchVM(b, "fannkuch", false) }
func BenchmarkNBody_Wangshu(b *testing.B)        { benchVM(b, "nbody", true) }
func BenchmarkNBody_Gopher(b *testing.B)         { benchVM(b, "nbody", false) }
