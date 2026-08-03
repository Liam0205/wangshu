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
		// Cost in bytes CONSUMED rather than produced -- the fourth audit round's shape. Each of
		// these copies a megabyte per call while producing little or nothing, so a charge based on
		// output cannot see them.
		{"tostring of a large string",
			`local s=string.rep("a",1048576) for i=1,100000 do tostring(s) end return 1`},
		{"format with a truncating precision",
			`local s=string.rep("a",1048576) for i=1,200000 do string.format("%.1s",s) end return 1`},
		{"format with zero precision",
			`local s=string.rep("a",1048576) for i=1,200000 do string.format("%.0s",s) end return 1`},
		// tonumber parses proportionally to input length while producing a number.
		// LITERAL format text, no verbs. The per-iteration increment advances one byte here, and
		// chargeBulkWork floored bytes>>6 to zero, so a 1 MiB literal format string ran 25 seconds
		// with the budget untouched. The meter now carries the sub-64-byte remainder.
		{"format of a large literal",
			`local f=string.rep("x",1048576) for i=1,4000 do string.format(f) end return 1`},
		{"format of many percent escapes",
			`local f=string.rep("%%",524288) for i=1,20000 do string.format(f) end return 1`},
		// The VM-side twins of charges added above. Charging tonumber but not the coercion the VM
		// does for `s+1` left the identical parse free -- an asymmetry this branch created.
		// find/match scan the subject and return a couple of integers, so no produced-bytes charge
		// can see them. Round 5 removed find's needless copy but never added a charge, leaving only
		// the surrounding loop as a bound -- which a few iterations over a huge subject evade.
		{"plain find over a huge subject",
			`local s=string.rep("a",4194304) for i=1,500 do s:find("b",1,true) end return 1`},
		{"pattern match over a large subject",
			`local s=string.rep("a",1048576) for i=1,20000 do s:match("a*b") end return 1`},
		{"VM numeric coercion of a long numeral",
			`local s=string.rep("9",100000) local x for i=1,50000 do x=s+1 end return 1`},
		{"error's position prefix copy",
			`local s=string.rep("a",1048576) for i=1,20000 do pcall(function() error(s) end) end return 1`},
		{"tonumber of a long numeral",
			`local s=string.rep("9",100000) for i=1,50000 do tonumber(s) end return 1`},
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
		{"literal format", `return string.format("hello")`, "hello"},
		{"percent escape", `return string.format("100%%")`, "100%"},
		{"format several verbs", `return string.format("%s-%d-%5.2f","a",7,3.14159)`, "a-7- 3.14"},
		{"truncating precision", `return string.format("%.1s","hello")`, "h"},
		{"zero precision", `return string.format("%.0s","hello").."|"`, "|"},
		{"tostring passthrough", `return tostring("s")`, "s"},
		// sub used to copy the WHOLE subject before slicing, so one byte out of 1 MiB cost a
		// megabyte per call (43s over 300000 calls). It works on the byte view now, and a genuine
		// full-length extraction still charges its bytes.
		{"one byte from a large string",
			`local s=string.rep("a",1048576) local r for i=1,2000 do r=s:sub(1,1) end return r`, "a"},
		{"find", `return tostring(("hello"):find("l"))`, "3"},
		{"find plain", `return tostring(("hello"):find("l",1,true))`, "3"},
		{"find misses", `return tostring(("hello"):find("z"))`, "nil"},
		{"match", `return tostring(("hello"):match("l+"))`, "ll"},
		{"match misses", `return tostring(("hello"):match("z"))`, "nil"},
		{"string arithmetic", `return tostring("10"+5)`, "15"},
		{"string arithmetic hex", `return tostring("0x10"+0)`, "16"},
		{"numeric for over strings", `for i="1","3" do end return "ok"`, "ok"},
		{"error with a prefix", `local ok,e=pcall(function() error("m") end) return tostring(e)`,
			`[string "r"]:1: m`},
		{"error without a prefix", `local ok,e=pcall(function() error("m",0) end) return tostring(e)`, "m"},
		{"tonumber decimal", `return tostring(tonumber("42"))`, "42"},
		{"tonumber hex literal", `return tostring(tonumber("0x1f"))`, "31"},
		{"tonumber with a base", `return tostring(tonumber("ff",16))`, "255"},
		{"tonumber rejects", `return tostring(tonumber("zz"))`, "nil"},
		{"tostring number", `return tostring(1.5)`, "1.5"},
		{"tostring metamethod",
			`local t=setmetatable({},{__tostring=function() return "M" end}) return tostring(t)`, "M"},
		{"64 KiB tostring", `local s=string.rep("q",65536) return tostring(#tostring(s))`, "65536"},
		// Several references to one capture must not re-intern it per reference.
		{"repeated capture references", `return ("abc"):gsub("(a)(b)","%0/%1/%2")`, "ab/a/bc"},
		{"swapped captures", `return ("aXbXc"):gsub("(%a)X(%a)","%2-%1")`, "b-aXc"},
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
