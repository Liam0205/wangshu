package regression

import (
	"testing"
	"time"

	wangshu "github.com/Liam0205/wangshu"
)

// TestBulkBuildersChargeTheStepBudget covers #222 and the operators the #166 guide had named as the
// next candidates in that family.
//
// The concat-storm work (#123-#167) established that the failure mode is CPU wall-clock, not memory:
// go-fuzz kills a worker whose single input runs past a 10-second watchdog, surfacing as
// "hung or terminated unexpectedly: exit status 2". CONCAT was fixed by charging its bytes to the
// step budget, and the guide recorded that "string.rep / string.format / table.concat 尚未按工作量
// 记账,是同类风险的候选".
//
// They were, measurably: inside a 1<<20 step budget and WITHOUT tripping it, a tight loop of each
// ran 21s, 20s and 53s respectively -- so one prog.Run already exceeded the watchdog, and the
// FuzzAutoPromote harness runs four per input. Each now charges its produced bytes on the same meter
// as CONCAT, so the budget is one definition of "bulk work" rather than several thresholds that can
// drift apart.
//
// No in-test deadline: the failure mode is "runs far too long", judged by the package-level
// go test -timeout, per llmdoc/guides/unreproducible-crasher-triage.md.
func TestBulkBuildersChargeTheStepBudget(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"string.rep", `local out for i=1,1000000 do out=string.rep("abcdefgh",4096) end return 1`},
		{"string.format", `local s=string.rep("x",4096) for i=1,1000000 do local o=string.format("%s%s%s%s",s,s,s,s) end return 1`},
		{"table.concat", `local t={} for i=1,256 do t[i]=string.rep("y",2048) end for i=1,100000 do local o=table.concat(t) end return 1`},
		// The remaining five, found by an audit pointing at the repo's OWN evidence: the oracle
		// prelude already charged upper/lower/reverse/sub, so leaving the engine unbudgeted was
		// both a hole and an asymmetry between the two differential sides. Measured at
		// 23s/12s/12s/12s/11s before charging, each without touching the budget.
		{"string.upper", `local s=string.rep("a",1048576) for i=1,20000 do s:upper() end return 1`},
		{"string.lower", `local s=string.rep("A",1048576) for i=1,20000 do s:lower() end return 1`},
		{"string.reverse", `local s=string.rep("a",1048576) for i=1,20000 do s:reverse() end return 1`},
		{"string.sub", `local s=string.rep("a",1048576) for i=1,20000 do s:sub(1,1048576) end return 1`},
		{"string.gsub", `local s=string.rep("a",200) local r=string.rep("b",4096) for i=1,200000 do s:gsub("a",r) end return 1`},
		// A FUNCTION or TABLE replacement's length cannot be known before the loop, which is why
		// gsub charges per match rather than estimating: a closure returning 200 KB per match ran
		// 15 seconds untripped when the pre-loop estimate fell back to charging only the subject.
		{"gsub with a function replacement",
			`local s=string.rep("a",200) local big=string.rep("z",204800) for i=1,400 do s:gsub("a",function() return big end) end return 1`},
		// SINGLE-match amplification. The audit noted the cases above all use many small matches,
		// where a per-match charge fires often enough to bound things -- so they could not catch a
		// charge placed AFTER the replacement is built. One match expanding 20000 back-references
		// ran 46 seconds that way; the charge is now incremental inside the %n expansion.
		{"gsub one match, many back-references",
			`local s=string.rep("a",1048576) local r=string.rep("%1",20000) local o=s:gsub("(a+)",r) return 1`},
		// Same shape for format: one call, thousands of verbs, charged inside the verb loop.
		{"format one call, many verbs",
			`local s=string.rep("x",1048576) local f=string.rep("%s",7000) local a={} for i=1,7000 do a[i]=s end local o=string.format(f,unpack(a)) return 1`},
		{"gsub with a table replacement",
			`local s=string.rep("a",200) local big=string.rep("z",204800) local m={a=big} for i=1,400 do s:gsub("a",m) end return 1`},
	} {
		prog, err := wangshu.Compile([]byte(tc.src), "r")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetStepBudget(1 << 20)
		start := time.Now()
		_, rerr := prog.Run(st)
		elapsed := time.Since(start)

		if rerr == nil {
			t.Errorf("%s: ran to completion without tripping the step budget; a byte-heavy loop must "+
				"be bounded by it", tc.name)
		}
		// The point is the budget trips EARLY. Before charging, these took 20-53 seconds; the
		// bound here is loose enough for a slow shared runner and still an order of magnitude
		// below the 10-second watchdog that kills a fuzz worker.
		if elapsed > 5*time.Second {
			t.Errorf("%s: took %v inside the budget; four of these per fuzz input would pass the "+
				"10s watchdog", tc.name, elapsed.Round(time.Millisecond))
		}
	}
}

// TestBulkBuildersLeaveOrdinaryCodeAlone pins that the charge does not disturb normal programs.
//
// The meter is one step per 64 bytes, so a legitimate 1 MiB build costs ~16K steps of a 1<<20
// budget. If this ever starts failing, the ratio was tightened too far.
func TestBulkBuildersLeaveOrdinaryCodeAlone(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"small rep", `return string.rep("ab",3)`, "ababab"},
		{"1 MiB rep", `return tostring(#string.rep("x",1048576))`, "1048576"},
		{"format", `return string.format("%s-%d","a",7)`, "a-7"},
		{"concat", `return table.concat({1,2,3},",")`, "1,2,3"},
		{"1000-element concat", `local t={} for i=1,1000 do t[i]="x" end return tostring(#table.concat(t,","))`, "1999"},
		{"10000-element concat", `local t={} for i=1,10000 do t[i]=i end return tostring(#table.concat(t,"-"))`, "48893"},
		{"upper", `return ("hello"):upper()`, "HELLO"},
		{"lower", `return ("HeLLo"):lower()`, "hello"},
		{"reverse", `return ("abc"):reverse()`, "cba"},
		{"sub", `return ("hello"):sub(2,4)`, "ell"},
		{"gsub", `return ("aaa"):gsub("a","b")`, "bbb"},
		// A realistic template substitution. The pre-loop worst-case estimate billed this ~67 MB
		// and rejected it outright, where lua5.1 simply returns the 20 KiB result.
		{"template gsub",
			`local t=string.rep("x",16384).."%BODY%" local r=string.rep("y",4096) return tostring(#(t:gsub("%%BODY%%",r)))`,
			"20480"},
		{"gsub with a function replacement",
			`return ("aaa"):gsub("a",function(c) return c:upper() end)`, "AAA"},
		{"gsub with captures", `return ("abc"):gsub("(%a)","[%1]")`, "[a][b][c]"},
		{"gsub with a percent escape", `return ("abc"):gsub("b","%%")`, "a%c"},
		{"gsub empty match", `return ("abc"):gsub("","-")`, "-a-b-c-"},
		{"gsub anchored", `return ("abc"):gsub("^a","X")`, "Xbc"},
		{"format several verbs", `return string.format("%s-%d-%5.2f","a",7,3.14159)`, "a-7- 3.14"},
		{"64 KiB upper", `local s=string.rep("q",65536) return tostring(#s:upper())`, "65536"},
		// A realistic serialization workload: 2000 formatted fields joined together.
		{"format then concat",
			`local p={} for i=1,2000 do p[i]=string.format("%d:%s",i,"v") end return tostring(#table.concat(p,","))`,
			"12892"},
	} {
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetStepBudget(1 << 20)
		prog, err := wangshu.Compile([]byte(tc.src), "r")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		res, rerr := prog.Run(st)
		if rerr != nil {
			t.Errorf("%s: ordinary code tripped the budget: %v", tc.name, rerr)
			continue
		}
		if len(res) == 0 || res[0].Str() != tc.want {
			t.Errorf("%s: got %v, want %q", tc.name, res, tc.want)
		}
	}
}
