//go:build (wangshu_p3 || wangshu_p4) && wangshu_profile

package difftest

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// tieredErrorNameCases cover #339: an error raised from compiled code names the variable involved
// and carries the line of the faulting instruction, exactly as the interpreter and lua5.1 do. The
// host helper that raises takes both from the pc the compiled code hands it, so a pc that is off
// names another variable (or none) and reports another line.
//
// Every kernel spans several lines with the faulting expression on the first, and the comparison
// keeps the line number: only the chunk name is cut off the position prefix. The kernel is called
// through a Lua wrapper that is not a tail call, and four times, because the call that compiles it
// still runs in the interpreter (see tieredStdlibNameCases).
//
// pre runs before the kernel is defined and sets up the upvalue u.
//
// compiled says whether force-all runs the kernel compiled on this build, which the test asserts
// in both directions. The kernels that call do on every build. The others read or store through a
// table access whose inline cache never holds a hit: see errorNameIndexKernelsRunCompiled.
var tieredErrorNameCases = []struct {
	name, pre, kernel string
	compiled          bool
}{
	// The callee's name when the call's results go to two or three locals: luac puts one MOVE per
	// result between the CALL and the RETURN.
	{"call_nil_upvalue_two_results", `local u = table.nosuch`, `
  local a, b = u(t)
  return a,
    b`, true},
	{"call_nil_upvalue_three_results", `local u = table.nosuch`, `
  local a, b, c = u(t)
  return a,
    b,
    c`, true},
	{"host_function_argument_error_two_results", `local u = string.rep`, `
  local a, b = u(t)
  return a,
    b`, true},
	{"call_nil_upvalue_one_result", `local u = table.nosuch`, `
  local a = u(t)
  return a`, true},
	{"call_nil_upvalue_no_result", `local u = table.nosuch`, `
  u(t)
  return 1`, true},
	// Indexing an upvalue that is nil, for reading and for writing, at an instruction that is not
	// the function's first.
	{"index_nil_upvalue_two_results", `local u = table.nosuch`, `
  local a, b = u.x
  return a,
    b`, errorNameIndexKernelsRunCompiled},
	{"index_nil_upvalue_one_result", `local u = table.nosuch`, `
  local a = u.x
  return a`, errorNameIndexKernelsRunCompiled},
	{"store_to_nil_upvalue", `local u = table.nosuch`, `
  u.x = 1
  return 1`, errorNameIndexKernelsRunCompiled},
	{"store_to_nil_upvalue_after_other_statements", `local u = table.nosuch`, `
  local n = t[1]
  local m = n
  u.x = m
  return m`, errorNameIndexKernelsRunCompiled},
	{"index_nil_field_two_results", ``, `
  local a, b = t.nosuch.x
  return a,
    b`, errorNameIndexKernelsRunCompiled},
	// A store to a global and a read of one, each reaching a metamethod of the globals table that
	// raises with level 2: the position in the message is the line of the kernel's own instruction.
	// Neither is the function's first instruction.
	{"store_to_global_reaches_newindex", `setmetatable(_G, {__newindex = function(t, k) error("newindex " .. k, 2) end})`, `
  local n = t[1]
  zz339_g = n
  return n`, errorNameIndexKernelsRunCompiled},
	{"read_of_global_reaches_index", `setmetatable(_G, {__index = function(t, k) error("index " .. k, 2) end})`, `
  local n = t[1]
  local a = zz339_nosuch
  a = n
  return a`, errorNameIndexKernelsRunCompiled},
}

func tieredErrorNameScript(pre, kernel string) string {
	return pre + `
local function kernel(t)` + kernel + `
end
local function call(t) local a, b, c = kernel(t) return a, b, c end
local out = {}
for i = 1, 4 do
  local ok, msg = pcall(call, {1, 2})
  out[#out + 1] = tostring(ok) .. " " .. tostring(msg):gsub("^.-:(%d+: )", "%1")
end
return table.concat(out, " / ")`
}

// TestTiered_ErrorNames runs each kernel three ways on this build -- interpreter only, force-all
// promoted, and under the lowered thresholds of newAutoState -- and requires all three to agree
// with each other and with lua5.1. For each kernel it requires that force-all entered compiled
// code where the case says the build compiles it, and did not where it says the build does not: a
// kernel that stops being compiled would otherwise leave its case comparing the interpreter with
// itself. The run under lowered thresholds is not required to enter compiled code, and for most
// kernels here it does not (its count is logged): it only checks that whatever heat promotes
// gives the same result.
func TestTiered_ErrorNames(t *testing.T) {
	oracle := findOracle()
	for _, c := range tieredErrorNameCases {
		t.Run(c.name, func(t *testing.T) {
			src := tieredErrorNameScript(c.pre, c.kernel)
			prog, err := wangshu.Compile([]byte(src), "errnames")
			if err != nil {
				t.Fatalf("wangshu compile: %v", err)
			}
			interp := newInterpBaselineState()
			baseline := runAutoOnce(t, prog, interp, "baseline")
			if n := interp.GibbousEntries(); n != 0 {
				t.Errorf("the interpreter-only baseline entered compiled code %d times", n)
			}
			if oracle != "" {
				if want := runOracle(t, oracle, wrapForOracle(src)); baseline != want {
					t.Errorf("interpreter vs oracle:\n  wangshu: %q\n  oracle:  %q", baseline, want)
				}
			}
			forced := wangshu.NewState(wangshu.Options{})
			forced.SetForceAllPromote(true)
			if got := runAutoOnce(t, prog, forced, "force-all"); got != baseline {
				t.Errorf("force-all diverged from the interpreter:\n  force-all:   %q\n  interpreter: %q", got, baseline)
			}
			switch n := forced.GibbousEntries(); {
			case c.compiled && n == 0:
				t.Errorf("force-all never entered compiled code (%d protos promoted): this kernel does not exercise the tier",
					forced.PromotionCount())
			case !c.compiled && n != 0:
				t.Errorf("force-all entered compiled code %d times on a build recorded as not compiling this kernel: "+
					"errorNameIndexKernelsRunCompiled is out of date", n)
			}
			auto := newAutoState()
			if got := runAutoOnce(t, prog, auto, "auto"); got != baseline {
				t.Errorf("auto diverged from the interpreter:\n  auto:        %q\n  interpreter: %q", got, baseline)
			}
			t.Logf("entries into compiled code: force-all %d, auto %d", forced.GibbousEntries(), auto.GibbousEntries())
		})
	}
}
