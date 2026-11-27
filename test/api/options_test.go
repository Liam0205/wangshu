package api_test

import (
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestOptions_FieldsNotRead pins what NewState does with three fields of Options today: nothing
// (issue #356). The collector's pause ratio starts at 200 whatever GCPause says, and the two
// depth limits are the compile-time ones whatever MaxCallDepth and MaxCCalls say. The godoc of
// Options and section 1.2 of the embedding design document say so; when one of these fields is
// wired in, this test fails and both must follow.
func TestOptions_FieldsNotRead(t *testing.T) {
	run := func(t *testing.T, opts wangshu.Options, src string) string {
		t.Helper()
		prog, err := wangshu.Compile([]byte(src), "options")
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		results, err := prog.Run(wangshu.NewState(opts))
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		parts := make([]string, len(results))
		for i, r := range results {
			parts[i] = r.Display()
		}
		return strings.Join(parts, " ")
	}

	// The ratio a script reads back is 200 whatever GCPause says.
	const pause = `return collectgarbage("setpause", 200)`
	for _, p := range []int{0, 150, -1, 100000} {
		if got := run(t, wangshu.Options{GCPause: p}, pause); got != "200" {
			t.Errorf("Options.GCPause = %d: got %q, want %q (the field is not read)", p, got, "200")
		}
	}

	// 5000 nested Lua calls succeed under MaxCallDepth 16.
	const depth = `
local function f(n) if n == 0 then return 0 end return 1 + f(n - 1) end
return pcall(f, 5000)`
	if got := run(t, wangshu.Options{MaxCallDepth: 16}, depth); got != "true 5000" {
		t.Errorf("Options.MaxCallDepth = 16: got %q, want %q (the field is not read)", got, "true 5000")
	}

	// 150 nested pcalls, each a call from a host function back into Lua, succeed under MaxCCalls 8.
	const reentry = `
local function g(n) if n == 0 then return 0 end return 1 + select(2, pcall(g, n - 1)) end
return pcall(g, 150)`
	if got := run(t, wangshu.Options{MaxCCalls: 8}, reentry); got != "true 150" {
		t.Errorf("Options.MaxCCalls = 8: got %q, want %q (the field is not read)", got, "true 150")
	}
}
