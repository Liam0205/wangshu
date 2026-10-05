package regression

import (
	"strconv"
	"strings"
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestHostOnlyAllocationTriggersGC covers #297: a host function charges what it allocates to the GC
// but never collects, and the only automatic safepoints were allocating opcodes. A loop whose only
// allocations happen inside host functions (here string.rep, whose result is dropped at once)
// therefore never collected and grew until the arena limit; lua5.1 peaks at ~1.2 MB here. Each host
// call now ends in a safepoint once its results are on the stack.
func TestHostOnlyAllocationTriggersGC(t *testing.T) {
	const src = `local peak = 0
for i = 1, 400 do
  local s = string.rep("x", i * 1000)
  local c = collectgarbage("count") if c > peak then peak = c end
end
print(peak < 8 * 1024)`
	if got := printedBy(t, wangshu.Options{}, src); got != "true\n" {
		t.Errorf("got %q, want the count to stay under 8 MB", got)
	}
}

// TestEmbedderHostFunctionAllocationTriggersGC is #297 through a function registered by the
// embedder: each call returns a distinct 64 KB string (equal ones would be interned once) that the
// loop drops.
func TestEmbedderHostFunctionAllocationTriggersGC(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	pad := strings.Repeat("y", 64*1024)
	n := 0
	st.Register("blob", func(*wangshu.State, []wangshu.Value) ([]wangshu.Value, error) {
		n++
		return []wangshu.Value{wangshu.String(strconv.Itoa(n) + pad)}, nil
	})
	prog, err := wangshu.Compile([]byte(`local peak = 0
for i = 1, 2000 do
  local s = blob()
  local c = collectgarbage("count") if c > peak then peak = c end
end
return peak`), "@x")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if peak := r[0].Number(); peak > 8*1024 {
		t.Errorf("count peaked at %.0f KB over 2000 calls of a 64 KB host function, want at most 8 MB", peak)
	}
}
