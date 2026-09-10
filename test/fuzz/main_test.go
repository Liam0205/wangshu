// main_test.go — TestMain for the fuzz package. The only job here is to
// arm the worker forensics before any target runs; everything else
// (fd-2 autopsy, flight recorder, worker-mode detection) lives in
// internal/fuzzforensics.
//
// This TestMain MUST stay in the package that hosts the Fuzz* targets:
// the coordinator re-executes THIS test binary as each worker, so a
// TestMain in any other package never runs inside a worker and the
// forensics silently produce nothing.
package fuzz_test

import (
	"os"
	"testing"

	"github.com/Liam0205/wangshu/internal/fuzzforensics"
)

func TestMain(m *testing.M) {
	fuzzforensics.SetupWorker()
	os.Exit(m.Run())
}
