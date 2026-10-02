package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestXpcallHandlerResultIsNotKeptAlive checks that the value an xpcall message handler returns is
// collectable once the xpcall that caught the error has returned it and nothing refers to it. The
// handler runs at the raise point, so its result is rooted while the error unwinds to xpcall; that
// root used to stay set until the next handler ran, keeping the last result (here a table of 200000
// numbers) alive for as long as the State lived. The xpcalls are statements in functions that have
// returned before the collection, so no register of a live frame can hold the result either; the
// second one runs inside another handler. lua5.1 prints "false false".
func TestXpcallHandlerResultIsNotKeptAlive(t *testing.T) {
	const src = `collectgarbage() collectgarbage()
local base = collectgarbage("count")
local function big() local t = {} for i = 1, 200000 do t[i] = i end return t end
local function direct() xpcall(function() error("x") end, big) end
local function nested()
  xpcall(function() error("x") end, function(m) xpcall(function() error("y") end, big) return m end)
end
direct()
collectgarbage() collectgarbage()
local first = collectgarbage("count") - base > 1000
nested()
collectgarbage() collectgarbage()
OUT = tostring(first) .. " " .. tostring(collectgarbage("count") - base > 1000)`
	st := wangshu.NewState(wangshu.Options{})
	prog, err := wangshu.Compile([]byte(src), "@x")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := st.GetGlobal("OUT").Str(); got != "false false" {
		t.Errorf("got %q, want %q", got, "false false")
	}
}
