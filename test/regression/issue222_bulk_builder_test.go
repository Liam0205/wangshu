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
