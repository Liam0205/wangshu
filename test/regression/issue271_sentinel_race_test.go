package regression

import (
	"sync"
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestYieldSentinelNotWrittenAcrossStates covers a race the #271 review found. With
// __lt = coroutine.yield the handler returns the package-level yield sentinel, one object shared by
// every State, and two code paths wrote it: the metamethod call boundary reset its argNarg (already on
// master, reached by a plain A < B) and table.sort's default comparator marked it annotated. Two
// States doing that on two goroutines is a data race, which -race reports; without -race this only
// checks the runs finish.
//
// Since #272 such a yield is rejected in State.Yield before any sentinel exists (the coroutine is
// inside a host->Lua reentry), so this script no longer produces the sentinel at all; it stays as a
// guard for both: the boundary error must be raised without a race, and the two sentinel-skipping
// writes remain in place for any path that does still return it.
func TestYieldSentinelNotWrittenAcrossStates(t *testing.T) {
	const src = `
local m = {__lt = coroutine.yield}
local A, B = setmetatable({}, m), setmetatable({}, m)
for k = 1, 200 do
  local co = coroutine.create(function() table.sort({A, B, A}) return "done" end)
  coroutine.resume(co) coroutine.resume(co)
  co = coroutine.create(function() return A < B end)
  coroutine.resume(co) coroutine.resume(co)
end
`
	prog, err := wangshu.Compile([]byte(src), "i271race")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := wangshu.NewState(wangshu.Options{})
			if _, err := prog.Run(st); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
