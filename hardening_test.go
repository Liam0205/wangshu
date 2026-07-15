// Embedded hardening boundary regression -- scripts must not trigger a host OOM crash
// via stdlib (12 §4.9: keeping the host process alive takes priority over byte identity).
// These inputs OOM-crash the process under PUC 5.1.5 / gopher-lua; wangshu proactively
// fail-fasts and returns a Lua error.
package wangshu_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

func TestHardening_StringRepOverflow(t *testing.T) {
	st := wangshu.NewState(wangshu.Options{})
	// string.rep("k", 1e14) would allocate 100T bytes on a byte-for-byte backend, OOM crash
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
	// within a reasonable range it works normally
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
	// %.99999999999d would make fmt.Sprintf allocate a huge number of bytes
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
	// normal width/precision works
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
	// j = 1e14 makes the concat loop exhaust memory
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
	// NaN range: NaN-X=NaN bypasses the range check, and Go int(NaN)=MIN_INT64 differs
	// from PUC int(NaN)=0. After normalizing NaN→0 it takes the normal "invalid value at
	// index" path (matching PUC behavior), without crashing or bypassing.
	st := wangshu.NewState(wangshu.Options{})
	prog, _ := wangshu.Compile([]byte(`return pcall(table.concat, {1,2,3}, ",", 0/0)`), "h")
	r, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// NaN→0, t[0] is nil → invalid value (matching PUC); should not be OOM or range too large
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
	// `t={} t[3333170000]=""` triggers rehash → the `for (1<<b) < u` loop in countIntKey
	// spins forever (uint32(1)<<32=0, so b stays < u). Looks like OOM but is actually a CPU
	// infinite loop.
	// Fix: add a b<31 guard to the loop + cap bestASize at 1<<24 (consistent with the
	// mainline hardening threshold).
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
	// uint32 boundary value 4294967295 = 2^32-1, confirming the b=31 guard does not leak + no infinite loop.
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
