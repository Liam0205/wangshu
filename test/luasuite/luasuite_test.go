// Package luasuite runs the runnable subset of the official Lua 5.1.5 test
// suite (lua.org/tests).
//
// The official suite consists of semantic assertions written by the language
// authors; it is more authoritative than hand-written probes. The 13 files in
// testdata/ are copied verbatim from lua-5.1-tests (README: "main goal is to
// try to crash Lua") with no modifications. Files that cannot pass in full
// register an **exemption line** in the stopAt table (from that line on they
// depend on exempted features: setfenv/getfenv, debug.*, io.tmpfile,
// os.setlocale, string.dump, require -- all recorded in the exemption registry
// in corners_test.go); execution is truncated before the exemption line, and
// the prefix must pass entirely.
//
// Files not listed in stopAt must pass in full. Exemption lines may only move
// forward (as more features are implemented), never backward.
package luasuite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Liam0205/wangshu"
)

// stopAt: file -> exemption line (1-based line number, executes lines [1, stopAt)).
// 0 = run the whole file. The reason must point to an entry in the exemption registry.
var stopAt = map[string]int{
	// Passes in full
	"vararg.lua": 0,
	"sort.lua":   0,
	"pm.lua":     0,

	// getmetatable(io.stdin): io object model not implemented (io exemption)
	"errors.lua": 110,
	// debug.getinfo / string.dump / require (exempt: debug advanced surface / string.dump / require)
	"calls.lua": 165,
	// setfenv (events.lua:5 onward depends on it for the whole file; exempt: getfenv/setfenv)
	"events.lua": 4,
	// debug library (constructs.lua:189)
	"constructs.lua": 187,
	// end-of-line test section embeds a prog depending on debug.getinfo (literals.lua:112; exempt: debug)
	"literals.lua": 98,
	// getfenv/setfenv (globals test section from locals.lua:58; exempt: getfenv/setfenv)
	"locals.lua": 54,
	// io.tmpfile (math.lua:124)
	"math.lua": 122,
	// setfenv (nextvar.lua:238 onward; exempt: getfenv/setfenv)
	"nextvar.lua": 233,
	// os.setlocale (strings.lua:150)
	"strings.lua": 147,
	// setfenv (closure.lua:165)
	"closure.lua": 163,
	// Real incremental step-stepping semantics (gc.lua:111 dosteps asserts
	// completion over multiple steps; a STW full GC's step always completes in
	// one step, 10 §13 exempt: collectgarbage step/setstepmul). The first 110
	// lines (table/string/function churn, gcinfo settle loop) must pass entirely.
	"gc.lua": 111,
}

func TestOfficialSuite(t *testing.T) {
	files, err := filepath.Glob("testdata/*.lua")
	if err != nil || len(files) == 0 {
		t.Fatalf("no testdata files: %v", err)
	}
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			limit, known := stopAt[name]
			if !known {
				t.Fatalf("%s not registered in stopAt table (add 0 for full run or an exemption line)", name)
			}
			body := string(src)
			if limit > 0 {
				lines := strings.Split(body, "\n")
				if limit > len(lines) {
					limit = len(lines)
				}
				body = strings.Join(lines[:limit-1], "\n")
			}
			runWithTimeout(t, name, body)
		})
	}
}

func runWithTimeout(t *testing.T, name, src string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		prog, err := wangshu.Compile([]byte(src), "@"+name)
		if err != nil {
			done <- err
			return
		}
		st := wangshu.NewState(wangshu.Options{})
		_, err = prog.Run(st)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			msg := err.Error()
			if i := strings.IndexByte(msg, '\n'); i >= 0 {
				msg = msg[:i]
			}
			t.Errorf("%s: %s", name, msg)
		}
	case <-time.After(120 * time.Second):
		t.Fatalf("%s: timeout (likely interpreter hang)", name)
	}
}
