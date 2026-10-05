package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// arenaCapAfter runs src in a fresh State and returns the arena capacity in KiB at the end.
func arenaCapAfter(t *testing.T, src string) float64 {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "@x")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	return st.ArenaCapKB()
}

// TestArenaConcatLoopStaysBounded covers #296: a string built by repeated concatenation leaves a
// trail of dead LARGE strings, each a little bigger than the last. They were never merged, so no
// freed block fit the next string and every one came from bump; each collection then shrank the
// slab to bump and the next round doubled it again. Live data is ~160 KB (lua5.1 peaks at ~212 KB);
// the arena reached hundreds of MB. Freed neighbours now merge, a free block at the top lowers bump,
// and Compact keeps headroom, so the arena stays at a few MB.
func TestArenaConcatLoopStaysBounded(t *testing.T) {
	const src = `for i = 1, 30 do
  local s = ""
  for j = 1, 300 do s = s .. string.rep("x", 500 + (i % 7)) end
  collectgarbage()
end`
	if kb := arenaCapAfter(t, src); kb > 8*1024 {
		t.Errorf("arena capacity %.0f KB after the loop, want at most 8 MB", kb)
	}
}

// TestArenaDeepCoroutineStacksStayBounded covers #296's second pattern: coroutines whose stacks grow
// by doubling, then are dropped. The freed stack segments were never merged, so each new coroutine's
// growing stack needed fresh bump space and the arena kept growing (to ~28 MB here; lua5.1 stays
// under 3 MB).
func TestArenaDeepCoroutineStacksStayBounded(t *testing.T) {
	const src = `local function rec(n) if n == 0 then coroutine.yield() return 0 end return 1 + rec(n - 1) end
for i = 1, 200 do
  local co = coroutine.create(function() rec(5000) end)
  coroutine.resume(co)
end`
	if kb := arenaCapAfter(t, src); kb > 8*1024 {
		t.Errorf("arena capacity %.0f KB after the loop, want at most 8 MB", kb)
	}
}
