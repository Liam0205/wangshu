//go:build wangshu_oracle_cgo && cgo

package oracle

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const issue255Input = `s="00"for A=0,577777770 do s=s..0 end A()`

func TestExec_Issue255DefaultWallTime(t *testing.T) {
	r := Exec(issue255Input, Prelude(testKeep), Limits{})
	if r.Verdict != VerdictLimit || !strings.Contains(r.Err, "ORACLE_LIMIT: wall time budget") {
		t.Fatalf("verdict=%v err=%q", r.Verdict, r.Err)
	}
}

func TestExec_WallTimePropagation(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"plain", `while true do end`},
		{"pcall", `pcall(function() while true do end end) print("survived")`},
		{"xpcall", `xpcall(function() while true do end end, function() return "hidden" end) print("survived")`},
		{"resume", `local c=coroutine.create(function() while true do end end) coroutine.resume(c) print("survived")`},
		{"wrap", `coroutine.wrap(function() while true do end end)() print("survived")`},
		{"concat", issue255Input},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Exec(tc.src, Prelude(testKeep), Limits{Budget: -1, WallTime: 10 * time.Millisecond})
			if r.Verdict != VerdictLimit || !strings.Contains(r.Err, "ORACLE_LIMIT: wall time budget") {
				t.Fatalf("verdict=%v err=%q output=%q", r.Verdict, r.Err, r.Output)
			}
		})
	}
}

func TestExec_WallTimeConfigurations(t *testing.T) {
	prelude := Prelude(testKeep)
	t.Run("submillisecond", func(t *testing.T) {
		r := Exec(`while true do end`, prelude, Limits{Budget: 100000, WallTime: time.Nanosecond})
		if r.Verdict != VerdictLimit || !strings.Contains(r.Err, "wall time budget") {
			t.Fatalf("verdict=%v err=%q", r.Verdict, r.Err)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		r := Exec(`while true do end`, prelude, Limits{Budget: 1000, WallTime: -1})
		if r.Verdict != VerdictLimit || !strings.Contains(r.Err, "instruction budget") {
			t.Fatalf("verdict=%v err=%q", r.Verdict, r.Err)
		}
	})
	t.Run("large", func(t *testing.T) {
		r := Exec(`print(42)`, prelude, Limits{WallTime: time.Duration(1<<63 - 1)})
		if r.Verdict != VerdictOK || r.Output != "42\n" {
			t.Fatalf("verdict=%v err=%q output=%q", r.Verdict, r.Err, r.Output)
		}
	})
}

func TestExec_InstructionBudgetBoundary(t *testing.T) {
	for _, budget := range []int{1, 999, 1000, 1001, 1500, 2001} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			const src = `n=0 while true do n=n+1 end`
			const prelude = `function __oracle_readout() return tostring(n) end`
			legacy := Exec(src, prelude, Limits{Budget: budget, WallTime: -1})
			withTime := Exec(src, prelude, Limits{Budget: budget, WallTime: time.Hour})
			if legacy.Verdict != VerdictLimit || withTime.Verdict != VerdictLimit ||
				!strings.Contains(withTime.Err, "instruction budget") || legacy.Output != withTime.Output {
				t.Fatalf("budget=%d legacy=%+v withTime=%+v", budget, legacy, withTime)
			}
		})
	}
}

func TestExec_LimitStateIsolation(t *testing.T) {
	for i := 0; i < 4; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			prelude := Prelude(testKeep)
			limited := Exec(`while true do end`, prelude, Limits{Budget: -1, WallTime: time.Millisecond})
			if limited.Verdict != VerdictLimit || !strings.Contains(limited.Err, "wall time budget") {
				t.Fatalf("limited=%+v", limited)
			}
			normal := Exec(`print(6*7)`, prelude, Limits{})
			if normal.Verdict != VerdictOK || normal.Output != "42\n" {
				t.Fatalf("normal=%+v", normal)
			}
		})
	}
}
