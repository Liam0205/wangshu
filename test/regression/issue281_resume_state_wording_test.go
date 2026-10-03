package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// TestResumeNamesTheCoroutineState covers #281: luaB_coresume and auxwrap check costatus before
// lua_resume and report "cannot resume running coroutine" / "cannot resume normal coroutine"
// (statnames). wangshu reported lua_resume's "cannot resume non-suspended coroutine" for both,
// which is 5.2's wording at the library level. Expectations are lua5.1's.
func TestResumeNamesTheCoroutineState(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"resume itself (running)",
			`local co
co = coroutine.create(function() return coroutine.resume(co) end)
print(coroutine.resume(co))
`,
			`true	false	cannot resume running coroutine
`},
		{"resume an outer coroutine (normal)",
			`local outer, inner
inner = coroutine.create(function() return coroutine.resume(outer) end)
outer = coroutine.create(function() return coroutine.resume(inner) end)
print(coroutine.resume(outer))
`,
			`true	true	false	cannot resume normal coroutine
`},
		{"a wrap function calling itself",
			`local w
w = coroutine.wrap(function() return w() end)
print(pcall(w))
`,
			`false	x:2: cannot resume running coroutine
`},
		{"two wrap functions calling each other",
			`local w2
local w1 = coroutine.wrap(function() return w2() end)
w2 = coroutine.wrap(function() return w1() end)
print(pcall(w1))
`,
			`false	x:2: x:3: cannot resume normal coroutine
`},
		{"resume coroutine.running() from inside",
			`print(coroutine.resume(coroutine.create(function() return coroutine.resume(coroutine.running()) end)))
`,
			`true	false	cannot resume running coroutine
`},
		{"a dead coroutine keeps its wording",
			`local d = coroutine.create(function() end) coroutine.resume(d)
print(coroutine.resume(d))
local dw = coroutine.wrap(function() end) dw()
print(pcall(dw))
`,
			`false	cannot resume dead coroutine
false	cannot resume dead coroutine
`},
	} {
		if got := printedBy(t, wangshu.Options{}, tc.src); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
