//go:build wangshu_p4

package peroptranslator

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
)

var debugShimTrace = os.Getenv("PJ10_SHIM_TRACE") != ""

// A cheap ring buffer we can inspect from tests.
var shimTraceCount atomic.Int64
var shimTrace [256][8]int64

var shimTraceFileOnce sync.Once
var shimTraceFile *os.File

func shimTraceFileEnsure() {
	shimTraceFileOnce.Do(func() {
		p := os.Getenv("PJ10_SHIM_TRACE_FILE")
		if p == "" {
			return
		}
		f, err := os.Create(p)
		if err == nil {
			shimTraceFile = f
		}
	})
}

// printShimArith logs to the shim trace buffer. Uses no lock (goroutine
// races are fine for a diagnostic).
func printShimArith(base, pc, op, b, c, a int32) {
	i := shimTraceCount.Add(1) - 1
	if i < int64(len(shimTrace)) {
		shimTrace[i][0] = int64(base)
		shimTrace[i][1] = int64(pc)
		shimTrace[i][2] = int64(op)
		shimTrace[i][3] = int64(b)
		shimTrace[i][4] = int64(c)
		shimTrace[i][5] = int64(a)
	}
	shimTraceFileEnsure()
	if shimTraceFile != nil {
		fmt.Fprintf(shimTraceFile, "shimArith[%d]: base=%d pc=%d op=%d b=%d c=%d a=%d\n",
			i, base, pc, op, b, c, a)
		shimTraceFile.Sync()
	}
	if debugShimTrace {
		fmt.Fprintf(os.Stderr, "shimArith[%d]: base=%d pc=%d op=%d b=%d c=%d a=%d\n",
			i, base, pc, op, b, c, a)
	}
}

// ShimTraceDump returns a text dump of the trace buffer.
func ShimTraceDump() string {
	n := shimTraceCount.Load()
	if n > int64(len(shimTrace)) {
		n = int64(len(shimTrace))
	}
	out := fmt.Sprintf("shim trace (%d entries):\n", n)
	for i := int64(0); i < n; i++ {
		out += fmt.Sprintf("  [%d] base=%d pc=%d op=%d b=%d c=%d a=%d\n",
			i, shimTrace[i][0], shimTrace[i][1], shimTrace[i][2],
			shimTrace[i][3], shimTrace[i][4], shimTrace[i][5])
	}
	return out
}
