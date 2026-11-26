//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package tiering_test

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// TestGibbousEntries pins what State.GibbousEntries counts, on both compiled tiers.
//
// The probe exists because PromotionCount is not evidence that compiled code ran. The four scripts
// below promote the same kernel; they differ only in who invokes it:
//
//   - a Lua CALL: the kernel runs compiled;
//   - pcall(kernel): the host function invokes it and the interpreter runs its frame, every time;
//   - pcall(wrapper), where the wrapper CALLs the kernel: compiled again;
//   - pcall(wrapper), where the wrapper TAIL-calls the kernel. On P3 a tail call does not enter
//     compiled code; P4 enters it. Either way it must not be the form a tier test relies on, so the
//     case only records which it is and checks nothing about the count.
//
// A test that wraps its kernel directly in pcall therefore sees PromotionCount > 0 while exercising
// no compiled code, and a mutation of the compiled tier's error path leaves it green.
func TestGibbousEntries(t *testing.T) {
	const kernel = `local function kernel(x) local y = x * 2 + 1 return y - x end
`
	for _, tc := range []struct {
		name, body string
		wantRun    string // "yes", "no", or "" for not asserted
	}{
		{"lua call", `local s = 0 for i = 1, 40 do s = s + kernel(i) end return s`, "yes"},
		{"host invokes the kernel", `local s = 0 for i = 1, 40 do local _, v = pcall(kernel, i) s = s + v end return s`, "no"},
		{"host invokes a wrapper that calls it", `local function call(x) local v = kernel(x) return v end
local s = 0 for i = 1, 40 do local _, v = pcall(call, i) s = s + v end return s`, "yes"},
		{"host invokes a wrapper that tail-calls it", `local function call(x) return kernel(x) end
local s = 0 for i = 1, 40 do local _, v = pcall(call, i) s = s + v end return s`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := wangshu.Compile([]byte(kernel+tc.body), "entries")
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			st := wangshu.NewState(wangshu.Options{})
			st.SetForceAllPromote(true)
			res, err := prog.Run(st)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := res[0].Display(); got != "860" {
				t.Fatalf("result = %s, want 860", got)
			}
			if st.PromotionCount() == 0 {
				t.Fatalf("nothing was promoted; the case no longer says anything")
			}
			entries := st.GibbousEntries()
			t.Logf("promoted %d protos, entered compiled code %d times", st.PromotionCount(), entries)
			switch tc.wantRun {
			case "yes":
				if entries == 0 {
					t.Errorf("the kernel was promoted but compiled code was never entered")
				}
			case "no":
				if entries != 0 {
					t.Errorf("compiled code was entered %d times; a function invoked by a host function is expected to run "+
						"in the interpreter. If that changed, pcall(kernel) has become a valid way to test a tier and "+
						"the wrappers in TestTiered_StdlibNames are no longer needed", entries)
				}
			}
		})
	}

	// With promotion out of reach nothing is compiled and nothing is entered.
	prog, err := wangshu.Compile([]byte(kernel+`local s = 0 for i = 1, 40 do s = s + kernel(i) end return s`), "entries")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	st.SetHotThresholds(^uint32(0), ^uint32(0))
	if _, err := prog.Run(st); err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.PromotionCount() != 0 || st.GibbousEntries() != 0 {
		t.Errorf("interpreter-only run: promoted %d, entered %d, want 0 and 0", st.PromotionCount(), st.GibbousEntries())
	}
}
