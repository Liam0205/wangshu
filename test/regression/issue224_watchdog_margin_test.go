//go:build wangshu_p4

package regression

import (
	"github.com/Liam0205/wangshu/internal/fuzzbudget"
	"testing"
	"time"

	wangshu "github.com/Liam0205/wangshu"
)

// TestConcatStormKeepsWatchdogMargin covers #224 and #225, the seventh and eighth filings of the
// concat-storm family, and pins the quantity those two were actually about.
//
// Neither seed crashes or diverges, and both are correctly bounded by the byte accounting -- on the
// pre-#222 commit too, so nothing in that round is what fixed them. What was wrong is the MARGIN. A
// 1<<20 step budget permits about 64 MiB of concat, which cost 0.7-1.3s per fuzz subtest locally,
// and CI runners are roughly 10x slower (see chargeBulkWork's note in internal/crescent/state.go).
// The slowest family seeds therefore landed at 12-13s against go-fuzz's 10-second per-input
// watchdog, and the nightly log says exactly that: "panic: deadlocked".
//
// So a budget that merely BOUNDS the work is not enough when the bound itself sits within one order
// of magnitude of a wall-clock watchdog on the machine that matters. The fuzz targets now use
// fuzzbudget.Steps (1<<16), which costs no promotion coverage -- these shapes trip either way, the fuzzer walks
// the same paths and stops sooner -- and restores about 1.8x margin at CI speed.
//
// This test measures the budget's cost at the harness's own setting rather than the corpus timing,
// so it fails if the budget is raised back or if the per-byte rate is loosened.
func TestConcatStormKeepsWatchdogMargin(t *testing.T) {
	// The family's shape: a ~90-byte literal concatenated in a very long loop. Both #224 and #225
	// are this, with the target of the assignment misspelled so the accumulator never grows -- which
	// is why the minimized seeds are light and replay clean. Both forms are covered.
	for _, tc := range []struct{ name, src string }{
		{"accumulator never grows",
			`local function cat(i) return string.rep("x",90) .. i end
			 local out="" for i=1,777777776 do qut = out .. cat(i) end return out`},
		{"accumulator grows quadratically",
			`local function cat(i) return string.rep("x",90) .. i end
			 local out="" for i=1,777777776 do out = out .. cat(i) end return out`},
		// The shapes that actually BIND the budget. The two above are the filed seeds, but they are
		// cheap enough to pass at 1<<19 too, so a test using only them could not tell 1<<16 from
		// 1<<19 -- which an audit demonstrated. These three are the most expensive reachable
		// neighbours: at 1<<19 they projected to 14s, 18s and 41s for four Runs at 10x CI speed.
		//
		// gsub is the binding one because it bills roughly twice its subject per call while doing
		// considerably more work than an equally-billed concat: equal billing is not equal
		// wall-clock, which is why the budget has to be set by the worst BILLED-EQUAL shape rather
		// than by the shape that happened to be filed.
		{"plain concat loop",
			`local out="" for i=1,777777776 do out=out.."x" end return out`},
		{"table with string keys",
			`local t={} for i=1,777777776 do t[tostring(i)]=i end return 1`},
		{"gsub loop",
			`local s=string.rep("a",4096) for i=1,777777776 do s:gsub("%a","x") end return 1`},
		// Shapes that did NOT go through ChargeBulkWork at all, found because the audit asked for
		// expensive operators OUTSIDE the billing entry point -- the blind spot of enumerating along
		// that entry point, which is what this round's own method recommends. Each does O(n) work per
		// call while the loop's back edge bills one step, so lowering the budget scaled the iteration
		// count and not the per-iteration cost: head insert/remove projected to over two minutes and
		// a sort loop to 56s, at the budget that had just been chosen.
		{"insert at the head",
			`local t={} for i=1,4000 do t[i]=i end for k=1,777777776 do table.insert(t,1,0) table.remove(t) end return 1`},
		{"remove at the head",
			`local t={} for i=1,4000 do t[i]=i end for k=1,777777776 do table.remove(t,1) t[#t+1]=1 end return 1`},
		{"sort loop",
			`local t={} for i=1,999 do t[i]=1000-i end for k=1,777777776 do table.sort(t) end return 1`},
		// Five more found by sweeping for the same blind spot myself, rather than waiting for a third
		// audit round: everything that moves O(n) values per call without reaching ChargeBulkWork.
		// They projected to 73-129s at the chosen budget.
		{"unpack a wide table",
			`local a={} for i=1,4000 do a[i]=i end for k=1,777777776 do local _=select("#",unpack(a)) end return 1`},
		{"select at a high index",
			`local a={} for i=1,4000 do a[i]=i end for k=1,777777776 do local _=select(3999,unpack(a)) end return 1`},
		{"string.char with many args",
			`local a={} for i=1,4000 do a[i]=65 end for k=1,777777776 do string.char(unpack(a)) end return 1`},
		{"string.byte over a range",
			`local s=string.rep("a",4000) for k=1,777777776 do s:byte(1,4000) end return 1`},
		{"table.maxn",
			`local t={} for i=1,4000 do t[i]=i end for k=1,777777776 do table.maxn(t) end return 1`},
		// The two hash-probe shapes (maxn and unpack over non-integer keys) are asserted for
		// boundedness only, in TestChargeBoundsHashProbeShapes -- their machine-to-machine variance
		// exceeds the margin being asserted here.
		{"loadstring over a large source",
			`local src=string.rep("local x=1 ",20000) for k=1,777777776 do loadstring(src) end return 1`},
		// Positions the charge originally excluded. pos<1 shifts the WHOLE table (5.1's most
		// expensive insert), and a position that narrows into range from 2^32+1 skipped the charge
		// while the shift ran -- the charge read the raw float where the shift reads the narrowed
		// int, so the two disagreed about which call they were describing.
		{"insert below the start",
			`local t={} for i=1,4000 do t[i]=i end for k=1,777777776 do table.insert(t,-100,0) table.remove(t) end return 1`},
		// A SINGLE insert just under the shift cap. Clamping the charged position to 1 discarded the
		// span below index 1 -- exactly what makes a negative position expensive -- and gating the
		// charge on the table being non-empty hid it completely, since the loop's span depends on
		// pos and not on the table's length. One call walked ~134 million iterations in 2.4s with
		// the budget untouched.
		{"single insert just under the shift cap",
			`local t={} table.insert(t,-134217726,0) return 1`},
		{"insert at a wrapped position",
			`local t={} for i=1,4000 do t[i]=i end for k=1,777777776 do table.insert(t,4294967297,0) end return 1`},
		{"collectgarbage over a live heap",
			`local keep={} for i=1,20000 do keep[i]={i} end for k=1,777777776 do collectgarbage() end return 1`},
	} {
		prog, err := wangshu.Compile([]byte(tc.src), "r")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 64 << 20})
		st.SetStepBudget(fuzzbudget.Steps)
		start := time.Now()
		_, rerr := prog.Run(st)
		elapsed := time.Since(start)

		if rerr == nil {
			t.Errorf("%s: ran to completion; a 777-million-iteration concat loop must trip the budget",
				tc.name)
		}
		// One Run must stay under 10s / 10x / 4 Runs = 250ms.
		//
		// The first version of this test used a 1-second bound and an audit showed it passed with
		// the budget restored to 1<<20 -- the exact regression it claimed to guard. Its own comment
		// derived 250ms and then used four times that, and it compared a four-Run projection
		// against a one-Run measurement. The bound is now the derived number.
		if elapsed > watchdogMarginBound {
			t.Errorf("%s: one Run took %v at the fuzz budget; four of these on a 10x-slower CI "+
				"runner would pass go-fuzz's 10s per-input watchdog, which is how #224/#225 were "+
				"filed", tc.name, elapsed.Round(time.Millisecond))
		}
	}
}

// watchdogMarginBound is the ceiling for one Run: 10s watchdog / 10x CI slowdown / 4 Runs per input.
//
// Relaxed under -race, which instruments every memory access and ran these shapes 3-9x slower -- CI's
// p4 job runs `go test -race ... ./...`, so a fixed bound failed there while passing without it. Same
// mechanism as bulkChargeBound in issue222_bulk_builder_test.go, and the same lesson as that one: a
// wall-clock bound has to be expressed relative to the build it runs in.
var watchdogMarginBound = func() time.Duration {
	if bulkRaceBuild {
		return 2500 * time.Millisecond
	}
	return 250 * time.Millisecond
}()

// TestChargesDoNotRejectOrdinaryWork is the other half of the pair: every charge added for #224/#225
// must leave programs lua5.1 finishes in milliseconds alone.
//
// Three of the eight charges I added first were wrong in this direction, all by substituting a
// container's size for the work actually done: string.byte billed the whole subject rather than the
// requested range, so a per-character scan paid for the string on every call; insert/remove billed the
// whole table whenever a position was passed, including an append that shifts nothing; and sort billed
// n*log2(n)*8, treating each comparison as a word copy, which made one 500000-element sort cost
// seventeen times the entire budget.
func TestChargesDoNotRejectOrdinaryWork(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"per-character byte scan",
			`local s=string.rep("a",4200) local n=0 for i=1,#s do n=n+s:byte(i) end return tostring(n)`, "407400"},
		{"append via insert with a position",
			`local t={} for i=1,4000 do table.insert(t,#t+1,i) end return tostring(#t)`, "4000"},
		{"remove the last element repeatedly",
			`local t={} for i=1,4000 do t[i]=i end for i=1,3999 do table.remove(t,#t) end return tostring(#t)`, "1"},
		{"unpack a single value from a wide table",
			`local a={} for i=1,4000 do a[i]=i end return tostring(select("#",unpack(a,1,1)))`, "1"},
		{"maxn on a sparse table", `local t={1,2,3} t[10]=1 return tostring(table.maxn(t))`, "10"},
		// byte's range must be CLAMPED before charging: an out-of-range j asks for a million reads
		// and delivers three, and charging the unclamped span had it rejected even at the oracle
		// harness's larger budget, so FuzzOracleDiff skipped the input as a wangshu limit.
		{"byte with an out-of-range end", `return tostring(("abc"):byte(1,1000000))`, "97"},
		// A wrapped end index narrows to 1 in lua5.1 and reads one value. Charging the RAW float
		// billed about 4 billion elements and rejected it -- the charge stood before the narrowing
		// while the read stands after, so they described different calls.
		{"unpack with a wrapped end index",
			`return tostring(select("#",unpack({42},1,4294967297)))`, "1"},
		{"unpack an explicit small range",
			`local a={} for i=1,4000 do a[i]=i end return tostring(select("#",unpack(a,1,1)))`, "1"},
		{"loadstring a small chunk", `local f=loadstring("return 7") return tostring(f())`, "7"},
		{"collectgarbage once", `collectgarbage() return "ok"`, "ok"},
	} {
		prog, err := wangshu.Compile([]byte(tc.src), "r")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 256 << 20})
		st.SetStepBudget(fuzzbudget.Steps)
		res, rerr := prog.Run(st)
		if rerr != nil {
			t.Errorf("%s: REJECTED at the fuzz budget (%v); lua5.1 runs this in milliseconds, and in a "+
				"differential harness a limit error reads as skip, so over-charging silently drops coverage",
				tc.name, rerr)
			continue
		}
		if len(res) == 0 || res[0].Str() != tc.want {
			t.Errorf("%s: got %v, want %q", tc.name, res, tc.want)
		}
	}
}

// TestChargeBoundsHashProbeShapes asserts only that these shapes are BOUNDED, without a wall-clock
// ceiling.
//
// unpack over a hash-only table does one failed lookup per index, and a hash probe's cost is dominated
// by memory latency -- the quantity that varies most between a local machine and a shared CI VM. It
// measured 191ms locally and 4.4-5.7s on three CI runners, a 23-30x spread against the ~10x the margin
// model assumes, so asserting its wall-clock measures the runner rather than the charge. CI caught
// exactly that: the shape belongs in the charged set, not in the timed set.
//
// The charge is still what is being tested -- without it these run to completion.
func TestChargeBoundsHashProbeShapes(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"unpack a range from a hash-only table",
			`local t={} for i=1,7990 do t[i+0.5]=i end for k=1,777777776 do local _=select("#",unpack(t,1,7990)) end return 1`},
		{"maxn over hash keys",
			`local t={} for i=1,4000 do t[i+0.5]=i end for k=1,777777776 do table.maxn(t) end return 1`},
	} {
		prog, err := wangshu.Compile([]byte(tc.src), "r")
		if err != nil {
			t.Fatalf("%s: compile: %v", tc.name, err)
		}
		st := wangshu.NewState(wangshu.Options{MaxArenaBytes: 256 << 20})
		st.SetStepBudget(fuzzbudget.Steps)
		if _, rerr := prog.Run(st); rerr == nil {
			t.Errorf("%s: ran to completion; the charge must bound it", tc.name)
		}
	}
}
