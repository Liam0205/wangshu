// benchlua runs a self-timing Lua benchmark script under wangshu.
//
// The same .lua file is fed to the luajit binary by
// scripts/bench-vs-luajit.sh, so all engines execute identical source;
// each script prints its own "name<TAB>iters<TAB>us_per_iter" lines
// via os.clock (see benchmarks/vsluajit/*.lua).
//
// Tier selection is a build/flag matter, not a script matter:
//   - default build            -> P1 interpreter column
//   - -tags wangshu_p4/profile -> P4-auto column (natural heat)
//   - same build with -force   -> P4-force column (force-all promote)
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Liam0205/wangshu"
)

func main() {
	force := flag.Bool("force", false, "SetForceAllPromote(true) before running")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: benchlua [-force] script.lua")
		os.Exit(2)
	}
	path := flag.Arg(0)
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "benchlua: %v\n", err)
		os.Exit(1)
	}
	prog, err := wangshu.Compile(src, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "benchlua: compile: %v\n", err)
		os.Exit(1)
	}
	st := wangshu.NewState(wangshu.Options{})
	if *force {
		st.SetForceAllPromote(true)
	}
	if _, err := prog.Run(st); err != nil {
		fmt.Fprintf(os.Stderr, "benchlua: run: %v\n", err)
		os.Exit(1)
	}
}
