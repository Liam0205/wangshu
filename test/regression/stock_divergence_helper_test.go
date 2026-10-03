package regression

import (
	"testing"

	wangshu "github.com/Liam0205/wangshu"
)

// printedBy runs src as a chunk named "@x" on a fresh State and returns what it printed, so a case can
// be compared with lua5.1 running the same source as a file named "x". A compile or run error fails
// the test. Shared by the #281-#288 tests.
func printedBy(t *testing.T, opts wangshu.Options, src string) string {
	t.Helper()
	prog, err := wangshu.Compile([]byte(src), "@x")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return captureStdout(t, func() {
		if _, err := prog.Run(wangshu.NewState(opts)); err != nil {
			t.Errorf("run: %v", err)
		}
	})
}
