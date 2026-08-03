package regression

import (
	"testing"
	"time"

	wangshu "github.com/Liam0205/wangshu"
)

func runCharged(t *testing.T, src string) (time.Duration, bool, bool) {
	prog, err := wangshu.Compile([]byte(src), "r")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
	st.SetStepBudget(1 << 20)
	done := make(chan bool, 1)
	start := time.Now()
	go func() { _, e := prog.Run(st); done <- e != nil }()
	select {
	case tr := <-done:
		return time.Since(start), tr, true
	case <-time.After(12 * time.Second):
		return 12 * time.Second, false, false
	}
}

// TestEveryChargePairedBothDirections is the check that would have caught three consecutive audit
// findings, so it is pinned rather than left as a one-off sweep.
//
// Every bulk charge needs a PAIR of cases: one legitimate shape that must NOT be rejected, and one
// abusive shape that MUST be bounded. Validating one half lets the other regress -- round 8 checked
// only abusive shapes and rejected the standard anchored lexer; round 9 checked only legitimate ones
// and billed a subject-consuming match zero; round 10 had both errors in the same two lines.
//
// A failure here names which direction broke, which is the information that took a full audit round
// to recover each time.
//
// SENSITIVITY, measured rather than assumed: mutating chargeBulkWork globally, this catches a 64x
// error in either direction (64x undercharge fails gsub/find/tonumber/coerce/toNumberStr; 64x
// overcharge fails rep/upper) but NOT an 8x one. That band is inherent to a trip-or-not oracle --
// the pass/fail signal only reports which side of the budget a workload landed on, not by how much.
// So this test guards against a charge being removed, misplaced, or wrong in KIND; the documented
// per-operator rates in 10 section 3.1a, measured by budget bisection, are what pin the magnitudes.
func TestEveryChargePairedBothDirections(t *testing.T) {
	pairs := []struct{ name, legit, abuse string }{
		{"rep", `local o for i=1,2000 do o=string.rep("ab",512) end return 1`,
			`local o for i=1,1000000 do o=string.rep("abcdefgh",4096) end return 1`},
		{"format", `local p={} for i=1,2000 do p[i]=string.format("%d:%s",i,"v") end return 1`,
			`local s=string.rep("x",1048576) for i=1,200000 do string.format("%s",s) end return 1`},
		{"concat", `local t={} for i=1,10000 do t[i]=i end local o=table.concat(t,",") return 1`,
			`local t={} for i=1,256 do t[i]=string.rep("y",2048) end for i=1,100000 do table.concat(t) end return 1`},
		{"upper", `local s=string.rep("q",4096) for i=1,2000 do s:upper() end return 1`,
			`local s=string.rep("a",1048576) for i=1,20000 do s:upper() end return 1`},
		{"sub", `local s=string.rep("q",65536) for i=1,20000 do s:sub(1,10) end return 1`,
			`local s=string.rep("a",1048576) for i=1,20000 do s:sub(1,1048576) end return 1`},
		{"gsub", `local s=string.rep("a b ",2000) for i=1,50 do s:gsub("%s+"," ") end return 1`,
			`local s=string.rep("a",200) local r=string.rep("b",4096) for i=1,200000 do s:gsub("a",r) end return 1`},
		{"find", `local src=string.rep("ab ",8000) local pos,n=1,0 while pos<=#src do local a,b=src:find("^%a+",pos) if not a then pos=pos+1 else pos=b+1 n=n+1 end end return 1`,
			`local s=string.rep("a",4194304) for i=1,500 do s:find("b",1,true) end return 1`},
		{"match", `local t={} for w in string.rep("aa bb ",2000):gmatch("%a+") do t[w]=1 end return 1`,
			`local s=string.rep("a",1048576) for i=1,8000 do s:match("a*") end return 1`},
		{"tostring", `for i=1,20000 do tostring(i) end return 1`,
			`local s=string.rep("a",1048576) for i=1,100000 do tostring(s) end return 1`},
		{"tonumber", `for i=1,20000 do tonumber("123.5") end return 1`,
			`local s=string.rep("9",100000) for i=1,50000 do tonumber(s) end return 1`},
		{"coerce", `local x for i=1,20000 do x="12"+i end return 1`,
			`local s=string.rep("9",100000) local x for i=1,50000 do x=s+1 end return 1`},
		{"toNumberStr", `for i=1,20000 do math.floor("3.7") end return 1`,
			`local s=string.rep("9",100000) for i=1,60000 do math.floor(s) end return 1`},
	}
	for _, p := range pairs {
		ld, ltr, lok := runCharged(t, p.legit)
		ad, atr, aok := runCharged(t, p.abuse)
		if ltr || !lok {
			t.Errorf("%s: a LEGITIMATE shape was rejected (tripped=%v completed=%v after %v); "+
				"over-charging silently drops differential coverage, since a limit error reads as skip",
				p.name, ltr, lok, ld.Round(time.Millisecond))
		}
		if !atr || !aok {
			t.Errorf("%s: an ABUSIVE shape was NOT bounded (tripped=%v completed=%v after %v); "+
				"a single unbounded prog.Run passes go-fuzz's 10s watchdog",
				p.name, atr, aok, ad.Round(time.Millisecond))
		}
	}
}
