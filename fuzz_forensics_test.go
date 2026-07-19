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
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// forensicsDir is where per-PID worker logs land. scripts/go-fuzz.sh
// overrides it per target via WANGSHU_FUZZ_FORENSICS_DIR so a later
// script invocation in the same job (the p1 leg runs native fuzz THEN
// oracle fuzz) cannot delete an earlier target's post-mortem files
// before the artifact upload (review finding 3).
var forensicsDir = "fuzz-forensics"

// flightRecordSize is the fixed on-disk size of the flight record: one
// pwrite at offset 0 fully replaces the previous record, so a reader
// never sees a longer stale tail behind a shorter new record. Sized to
// hold the LARGEST input any target accepts (16 KiB length gates in
// FuzzCompileRun/FuzzAutoPromote/FuzzP4ForceAllPromote) plus header —
// an 8 KiB record silently truncated 8-16 KiB killers, losing exactly
// the bytes the recorder exists to recover (review finding 1).
const flightRecordSize = 20 << 10

// flightTruncMark flags a record whose input exceeded the (should-be-
// unreachable, given the size math above) remaining room.
const flightTruncMark = "\n<TRUNCATED>\n"

var (
	fuzzWorkerMode bool
	// stderrFile keeps the dup'd file reachable so its finalizer can
	// never close the underlying descriptor while fd 2 aliases it.
	stderrFile *os.File
	flightFile *os.File
	flightSeq  atomic.Uint64
	// flightBuf is reused across execs (fuzz callbacks are serial
	// within one worker process): a fresh 8 KiB allocation per exec
	// would add steady GC churn to the exact workload whose memory
	// pressure we are investigating (review finding).
	flightBuf = make([]byte, 0, flightRecordSize)
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
	if d := os.Getenv("WANGSHU_FUZZ_FORENSICS_DIR"); d != "" {
		forensicsDir = d
	}
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
// target, AFTER its length gate (recorded inputs are therefore always
// fully recoverable — over-long inputs are skipped unexecuted and
// cannot be the killer). One pwrite per exec (~1-2us against the page
// cache; the same pages are rewritten, so there is no I/O
// accumulation), zero heap allocations (review finding 2: fmt +
// time.Format allocated ~88 B/op — steady GC churn added to the exact
// workload whose memory pressure is under investigation; asserted at
// zero by TestFlightRecordZeroAllocs).
func recordFuzzExec(target, src string) {
	if flightFile == nil {
		return
	}
	flightBuf = appendFlightRecord(flightBuf[:0], flightSeq.Add(1),
		target, time.Now(), src)
	_, _ = flightFile.WriteAt(flightBuf, 0)
}

// appendFlightRecord formats one fixed-size flight record into buf
// using only alloc-free append primitives (strconv.Append*,
// Time.AppendFormat — no fmt, no intermediate strings).
func appendFlightRecord(buf []byte, seq uint64, target string, now time.Time, src string) []byte {
	buf = append(buf, "seq="...)
	buf = strconv.AppendUint(buf, seq, 10)
	buf = append(buf, " target="...)
	buf = append(buf, target...)
	buf = append(buf, " started="...)
	buf = now.AppendFormat(buf, time.RFC3339Nano)
	buf = append(buf, " len="...)
	buf = strconv.AppendInt(buf, int64(len(src)), 10)
	buf = append(buf, '\n')
	room := flightRecordSize - len(buf) - len(flightTruncMark)
	if len(src) > room {
		buf = append(buf, src[:room]...)
		buf = append(buf, flightTruncMark...)
	} else {
		buf = append(buf, src...)
		buf = append(buf, '\n')
	}
	for len(buf) < flightRecordSize {
		buf = append(buf, ' ')
	}
	return buf
}
