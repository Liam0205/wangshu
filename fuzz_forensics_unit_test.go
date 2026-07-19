// Unit tests for the forensics flight recorder (review findings on PR
// #165: capacity must cover the largest gated input byte-exactly, and
// the hot path must not allocate).
package wangshu_test

import (
	"strings"
	"testing"
	"time"
)

// TestFlightRecordMaxInputRecoverable: the largest input any target's
// length gate admits (16 KiB) must be recoverable from the record
// byte-for-byte — an undersized record silently truncates exactly the
// bytes the recorder exists to preserve.
func TestFlightRecordMaxInputRecoverable(t *testing.T) {
	src := strings.Repeat("\x00\xffab", 4<<10)[:1<<14] // 16 KiB, arbitrary bytes
	rec := appendFlightRecord(nil, 42, "FuzzCompileRun", time.Now(), src)
	if len(rec) != flightRecordSize {
		t.Fatalf("record size = %d, want %d", len(rec), flightRecordSize)
	}
	s := string(rec)
	if strings.Contains(s, flightTruncMark) {
		t.Fatalf("max gated input (16 KiB) got truncated — flightRecordSize too small")
	}
	nl := strings.IndexByte(s, '\n')
	body := s[nl+1:]
	if got := body[:len(src)]; got != src {
		t.Fatalf("recovered input differs from original (first divergence at %d)",
			firstDiff(got, src))
	}
	if rest := strings.TrimRight(body[len(src):], " "); rest != "\n" {
		t.Fatalf("unexpected bytes after input: %q", rest[:min(len(rest), 40)])
	}
}

// TestFlightRecordOversizeTruncates: inputs beyond the record's room
// (unreachable via the gated targets, but the formatter must stay
// safe) carry the explicit truncation mark.
func TestFlightRecordOversizeTruncates(t *testing.T) {
	src := strings.Repeat("x", flightRecordSize+1)
	rec := appendFlightRecord(nil, 1, "T", time.Now(), src)
	if len(rec) != flightRecordSize {
		t.Fatalf("record size = %d, want %d", len(rec), flightRecordSize)
	}
	if !strings.Contains(string(rec), flightTruncMark) {
		t.Fatal("oversize input not marked truncated")
	}
}

// TestFlightRecordZeroAllocs pins the hot path at zero heap
// allocations: the recorder runs once per fuzz exec inside the exact
// workload whose GC pressure is under investigation, so any per-exec
// garbage is signal pollution (review finding: the fmt-based version
// allocated 88 B/op).
func TestFlightRecordZeroAllocs(t *testing.T) {
	src := strings.Repeat("s", 1<<10)
	buf := make([]byte, 0, flightRecordSize)
	now := time.Now()
	allocs := testing.AllocsPerRun(100, func() {
		buf = appendFlightRecord(buf[:0], 7, "FuzzCompileRun", now, src)
	})
	if allocs != 0 {
		t.Fatalf("appendFlightRecord allocates %v times per run, want 0", allocs)
	}
}

func firstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}
