// fuzz_forensics_test.go — post-mortem forensics for silent fuzz worker
// deaths (the unreproducible concat-storm family, #123/#144/#145/#150-
// #152/#156/#157/#159/#162: workers die with "exit status 2" after
// millions of execs, the minimized input replays clean, and the log
// carries no stack).
//
// Two facts make this file work:
//   - "exit status 2" is the Go runtime's own fatal-exit code (panic /
//     fatal error), NOT an OS SIGKILL ("signal: killed") — the dying
//     worker almost certainly printed a full stack trace;
//   - internal/fuzz's coordinator spawns workers with cmd.Stderr left
//     nil, which os/exec wires to /dev/null — so that stack trace is
//     discarded every time.
//
// Mechanism A (autopsy): when this binary runs as a fuzz worker
// (-test.fuzzworker in os.Args), dup the worker's fd 2 onto a per-PID
// log file under fuzz-forensics/ so the runtime's fatal output lands
// on disk, and raise the traceback level to "all".
//
// Mechanism B (flight recorder): each fuzz-target execution overwrites
// a fixed-size per-PID record (seq, timestamp, input length + content)
// with a single pwrite. After a silent death the record answers "which
// worker was running which input, started when" — the mutated input
// itself is preserved (mutations that don't crash are never written to
// any corpus, so without this the killer input is unrecoverable).
//
// The directory is gitignored; scripts/go-fuzz.sh clears it per run and
// dumps non-trivial worker logs into the CI log stream on failure; the
// nightly workflow uploads it as an artifact.
package wangshu_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"
)

const forensicsDir = "fuzz-forensics"

// flightRecordSize is the fixed on-disk size of the flight record: one
// pwrite at offset 0 fully replaces the previous record, so a reader
// never sees a longer stale tail behind a shorter new record.
const flightRecordSize = 8 << 10

var (
	fuzzWorkerMode bool
	// stderrFile keeps the dup'd file reachable so its finalizer can
	// never close the underlying descriptor while fd 2 aliases it.
	stderrFile *os.File
	flightFile *os.File
	flightSeq  atomic.Uint64
)

func TestMain(m *testing.M) {
	for _, a := range os.Args[1:] {
		if a == "-test.fuzzworker" {
			fuzzWorkerMode = true
			break
		}
	}
	if fuzzWorkerMode {
		setupForensics()
	}
	os.Exit(m.Run())
}

// setupForensics is best-effort by design: forensics must never turn a
// healthy fuzz run into a failing one, so every error is swallowed.
func setupForensics() {
	if err := os.MkdirAll(forensicsDir, 0o755); err != nil {
		return
	}
	pid := os.Getpid()
	if f, err := os.Create(filepath.Join(forensicsDir,
		fmt.Sprintf("worker-%d-stderr.log", pid))); err == nil {
		fmt.Fprintf(f, "fuzz worker pid=%d started=%s\n",
			pid, time.Now().Format(time.RFC3339))
		if dupFD(int(f.Fd()), 2) == nil {
			stderrFile = f
		} else {
			f.Close()
		}
	}
	// "all" so a runtime fatal (e.g. out of memory) dumps every
	// goroutine — the allocating goroutine is not always the dying one.
	debug.SetTraceback("all")
	if f, err := os.Create(filepath.Join(forensicsDir,
		fmt.Sprintf("worker-%d-input.log", pid))); err == nil {
		flightFile = f
	}
}

// recordFuzzExec is called at the top of every root-package fuzz
// target. One pwrite per exec (~1-2us against the page cache; the same
// pages are rewritten, so there is no I/O accumulation) — measured in
// the single-digit-percent range at nightly exec rates, an acceptable
// price for recovering otherwise-unrecoverable mutated inputs.
func recordFuzzExec(target, src string) {
	if flightFile == nil {
		return
	}
	seq := flightSeq.Add(1)
	buf := make([]byte, 0, flightRecordSize)
	buf = fmt.Appendf(buf, "seq=%d target=%s started=%s len=%d\n",
		seq, target, time.Now().Format(time.RFC3339Nano), len(src))
	room := flightRecordSize - len(buf) - len("\n<TRUNCATED>\n")
	if len(src) > room {
		buf = append(buf, src[:room]...)
		buf = append(buf, "\n<TRUNCATED>\n"...)
	} else {
		buf = append(buf, src...)
		buf = append(buf, '\n')
	}
	for len(buf) < flightRecordSize {
		buf = append(buf, ' ')
	}
	_, _ = flightFile.WriteAt(buf, 0)
}
