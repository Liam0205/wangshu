//go:build wangshu_oracle_cgo && cgo

// fuzz_oracle_test.go -- differential fuzz against the process-embedded
// official Lua 5.1.5 (internal/oracle). Complements test/difftest:
// difftest feeds well-formed generator scripts through a fork-per-
// script lua5.1 oracle; this target lets go-fuzz feed ARBITRARY
// mutated sources through the in-process oracle at native fuzz rates.
//
// Comparison contract (design notes: internal/oracle godocs):
//   - both sides run the same Lua prelude (capture + stubs + guards +
//     whitelist trim + sorted iteration) before the fuzz input;
//   - resource limits on either side => skip (the two engines'
//     budgets are deliberately not comparable);
//   - otherwise the outcome CLASS must match (ran-to-completion vs
//     errored), and captured print/io.write output must be byte-equal
//     after address/NaN-sign normalization. Error TEXT is not
//     compared here -- that is difftest/errmsg's job at generator
//     granularity; at fuzz granularity it would drown in wording
//     deltas.
package wangshu_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu"
	"github.com/Liam0205/wangshu/internal/oracle"
)

// enumerateGlobals builds the oracle whitelist from a LIVE wangshu
// State: top-level global names plus the key sets of table-valued
// globals. Enumerating (rather than hand-listing) means stdlib growth
// flows into the oracle trim automatically.
func enumerateGlobals(t testing.TB) oracle.GlobalSet {
	st := wangshu.NewState(wangshu.Options{})
	prog, err := wangshu.Compile([]byte(`
local top, nested = {}, {}
for k, v in pairs(_G) do
  if type(k) == "string" then
    top[#top+1] = k
    if type(v) == "table" and k ~= "_G" then
      local keys = {}
      for k2 in pairs(v) do
        if type(k2) == "string" then keys[#keys+1] = k2 end
      end
      nested[k] = table.concat(keys, ",")
    end
  end
end
local out = {}
for i = 1, #top do
  local name = top[i]
  out[#out+1] = name .. "=" .. (nested[name] or "")
end
return table.concat(out, ";")
`), "enum")
	if err != nil {
		t.Fatalf("enumerate globals compile: %v", err)
	}
	res, err := prog.Run(st)
	if err != nil || len(res) == 0 {
		t.Fatalf("enumerate globals run: %v", err)
	}
	gs := oracle.GlobalSet{Nested: map[string][]string{}}
	for _, ent := range strings.Split(res[0].Str(), ";") {
		name, keys, _ := strings.Cut(ent, "=")
		if name == "" {
			continue
		}
		gs.Top = append(gs.Top, name)
		if keys != "" {
			ks := strings.Split(keys, ",")
			sort.Strings(ks)
			gs.Nested[name] = ks
		}
	}
	sort.Strings(gs.Top)
	return gs
}

// runWangshuSide executes prelude+src on a fresh wangshu State and
// classifies the outcome with the same three-state verdict the shim
// uses. Output is read back via the prelude's __oracle_readout.
func runWangshuSide(t *testing.T, src, prelude string) (verdict oracle.Verdict, output string, nanEvidence bool, errMsg string) {
	st := wangshu.NewState(wangshu.Options{
		// Cap the arena well below the Go fuzz worker's GOMEMLIMIT:
		// arena exhaustion must classify as a skip, not kill a worker.
		MaxArenaBytes: 64 << 20,
	})

	preProg, err := wangshu.Compile([]byte(prelude), "=prelude")
	if err != nil {
		t.Fatalf("prelude must compile on wangshu: %v", err)
	}
	if _, err := preProg.Run(st); err != nil {
		t.Fatalf("prelude must run on wangshu: %v", err)
	}

	// Budget arms AFTER the prelude, mirroring the shim's hook order.
	// 1<<22 back-edges is comparable coverage to the oracle's 50M
	// instruction default at wangshu's back-edge counting granularity.
	st.SetStepBudget(1 << 22)

	verdict = oracle.VerdictOK
	prog, err := wangshu.Compile([]byte(src), "fuzz")
	if err != nil {
		verdict, errMsg = oracle.VerdictError, err.Error()
	} else if _, rerr := prog.Run(st); rerr != nil {
		verdict, errMsg = oracle.VerdictError, rerr.Error()
	}
	if verdict == oracle.VerdictError && oracle.WangshuLimitError(errMsg) {
		verdict = oracle.VerdictLimit
	}

	// Read the accumulator back even after an error (output-before-
	// error compares). Disarm the budget first: readout is harness
	// code and must not be charged against the fuzz input's budget.
	st.SetStepBudget(0)
	ro := st.GetGlobal("__oracle_readout")
	if !ro.IsFunction() {
		// The fuzz script clobbered the readout global; output is
		// unrecoverable. Treat as limit (not comparable).
		return oracle.VerdictLimit, "", false, "readout clobbered"
	}
	res, roErr := st.Call(ro)
	if roErr != nil || len(res) == 0 || !res[0].IsString() {
		return oracle.VerdictLimit, "", false, "readout failed"
	}
	output, nanEvidence, ok := oracle.DecodeOutput(res[0].Str())
	if !ok {
		return oracle.VerdictLimit, "", false, "invalid readout"
	}
	return verdict, output, nanEvidence, errMsg
}

func FuzzOracleDiff(f *testing.F) {
	keep := enumerateGlobals(f)
	prelude := oracle.Prelude(keep)

	seeds := []string{
		`print(1 + 2 * 3, 7 % 3, 2 ^ 10, 7 / 2)`,
		`print("v=" .. 42, "abc" < "abd", nil == false)`,
		`local t = {} for i = 1, 10 do t[i] = i * i end print(#t, t[7])`,
		`local s = 0 for i = 10, 1, -2 do s = s + i end print(s)`,
		`local function fib(n) if n < 2 then return n end return fib(n-1) + fib(n-2) end print(fib(15))`,
		`print(("abc"):upper(), string.rep("xy", 3), ("hello"):sub(2, 4))`,
		`print(string.format("%d %s %.2f", 42, "x", 1.5))`,
		`print(string.find("hello world", "o w"), string.match("k=v", "(%w+)=(%w+)"))`,
		`local ok, e = pcall(function() error("x") end) print(ok, e)`,
		`local t = { b = 2, a = 1, [3] = "x" } for k, v in pairs(t) do print(k, v) end`,
		`local co = coroutine.create(function(a) local b = coroutine.yield(a + 1) print("in", b) end)
print(coroutine.resume(co, 10)) print(coroutine.resume(co, 20))`,
		`print(math.floor(1.5), math.max(1, 2), math.huge, -math.huge)`,
		`print(0/0 ~= 0/0, 1/0, -1/0)`,
		`print(select("#", 1, 2), select(2, "a", "b", "c"))`,
		`print(tostring(nil), tostring(true), tostring(1e100))`,
		`local t = setmetatable({}, {__index = function(_, k) return k .. "!" end}) print(t.foo)`,
		`local t = setmetatable({}, {__tostring = function() return "MT" end}) print(t)`,
		`print(unpack({1, "two", nil, 4}, 1, 4))`,
		`local f = loadstring("return 1 + 1") print(f and f())`,
		`io.write("a", 1.5, "b") print()`,
		`x = 1 do local x = 2 print(x) end print(x)`,
		`print(#"bytes", ("q"):byte(), string.char(97, 98))`,
		`local t = {5, 2, 8, 1} table.sort(t) print(unpack(t))`,
		`print(tonumber("0x10"), tonumber("  42  "), tonumber("z"), tonumber("10", 2))`,
		`print(rawequal({}, {}), rawget({a=1}, "a"), type(next))`,
		// PUC/x86/libc and wangshu intentionally retain different NaN sign
		// spellings. This seed proves the known-difference path stays live.
		`print(string.format("value=[%10E]", -(0/0)))`,
		// Conversions may touch ordinary format text on either side.
		`print(string.format("%E0", -(0/0)),
		      string.format("0%E", -(0/0)))`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 4<<10 {
			// Small cap (vs FuzzCompileRun's 16 KiB): bounds PUC-side
			// C-stack shapes the instruction hook cannot see.
			t.Skip("input too large")
		}
		// NUL bytes: wangshu's lexer accepts them inside strings, but
		// they cannot survive the C string boundary comparison anyway;
		// PUC accepts them via luaL_loadbuffer. Cheaper to skip than
		// to argue about a byte no sane script contains.
		if strings.ContainsRune(src, 0) {
			t.Skip("NUL byte")
		}
		recordFuzzExec("FuzzOracleDiff", src)

		or := oracle.Exec(src, prelude, oracle.Limits{})
		if or.Verdict == oracle.VerdictLimit {
			t.Skip("oracle limit: " + or.Err)
		}
		wv, wout, wnan, werr := runWangshuSide(t, src, prelude)
		if wv == oracle.VerdictLimit {
			t.Skip("wangshu limit: " + werr)
		}
		// Depth/complexity guards trip at implementation-specific
		// points near the shared nominal thresholds; when EITHER side
		// reports one, class equality is not meaningful.
		if (or.Verdict == oracle.VerdictError && oracle.SkipClassError(or.Err)) ||
			(wv == oracle.VerdictError && oracle.SkipClassError(werr)) {
			t.Skip("impl-constant guard tripped")
		}

		if or.Verdict != wv {
			t.Fatalf("verdict class diverged: oracle=%v (err=%q) wangshu=%v (err=%q)\n--- script ---\n%s",
				or.Verdict, or.Err, wv, werr, src)
		}

		switch oracle.CompareOutput(or.Output, wout, or.KnownNaNSign, wnan) {
		case oracle.OutputEqual:
			return
		case oracle.OutputKnownNaNSign:
			t.Skip("known platform difference: NaN sign spelling (#173)")
		case oracle.OutputDifferent:
			oOut := oracle.NormalizeOutput(or.Output)
			wOut := oracle.NormalizeOutput(wout)
			t.Fatalf("output diverged:\n  oracle:  %q\n  wangshu: %q\n--- script ---\n%s",
				oOut, wOut, src)
		default:
			t.Fatalf("unknown oracle output comparison")
		}
	})
}
