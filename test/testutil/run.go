// Package testutil holds test helpers shared by more than one package
// under test/. A helper belongs here only once a second package needs
// it; single-consumer helpers stay next to their tests.
package testutil

import (
	"testing"

	"github.com/Liam0205/wangshu"
)

// RunOne compiles src, runs it on a fresh default State, and returns the
// first result (Nil when the chunk returns nothing). Compile and run
// errors fail the test.
func RunOne(t *testing.T, src string) wangshu.Value {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "test")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	st := wangshu.NewState(wangshu.Options{})
	results, err := prog.Run(st)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) == 0 {
		return wangshu.Nil()
	}
	return results[0]
}
